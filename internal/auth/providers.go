package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

type Profile struct {
	Subject, Email, Name string
	Verified             bool
}

// Provider permits another OIDC implementation without changing session handling.
type Provider interface {
	AuthorizationURL(state, verifier, nonce string) string
	Exchange(context.Context, string, string, string) (Profile, error)
}
type OIDC struct {
	Config   oauth2.Config
	Verifier *oidc.IDTokenVerifier
	// AllowUnverifiedEmail is opt-in; Google and legacy providers still require verified email.
	AllowUnverifiedEmail bool
}

func NewOIDC(ctx context.Context, issuer, clientID, secret, callback string, clock func() time.Time) (*OIDC, error) {
	p, e := oidc.NewProvider(ctx, issuer)
	if e != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", e)
	}
	return &OIDC{Config: oauth2.Config{ClientID: clientID, ClientSecret: secret, Endpoint: p.Endpoint(), RedirectURL: callback, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}, Verifier: p.Verifier(&oidc.Config{ClientID: clientID, Now: clock})}, nil
}
func (p *OIDC) AuthorizationURL(state, verifier, nonce string) string {
	return p.Config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
}
func (p *OIDC) Exchange(ctx context.Context, code, verifier, nonce string) (Profile, error) {
	t, e := p.Config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if e != nil {
		return Profile{}, fmt.Errorf("OIDC exchange: %w", e)
	}
	raw, _ := t.Extra("id_token").(string)
	token, e := p.Verifier.Verify(ctx, raw)
	if e != nil {
		return Profile{}, fmt.Errorf("ID token: %w", e)
	}
	if subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(nonce)) != 1 {
		return Profile{}, fmt.Errorf("nonce mismatch")
	}
	var claims struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Verified bool   `json:"email_verified"`
	}
	if e = token.Claims(&claims); e != nil {
		return Profile{}, fmt.Errorf("claims: %w", e)
	}
	if !claims.Verified && !p.AllowUnverifiedEmail {
		return Profile{}, fmt.Errorf("unverified email")
	}
	return Profile{token.Subject, claims.Email, claims.Name, true}, nil
}

type GitHub struct {
	Config oauth2.Config
	APIURL string
}

func (p *GitHub) AuthorizationURL(state, verifier, _ string) string {
	return p.Config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}
func (p *GitHub) Exchange(ctx context.Context, code, verifier, _ string) (Profile, error) {
	t, e := p.Config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if e != nil {
		return Profile{}, fmt.Errorf("GitHub exchange: %w", e)
	}
	client := p.Config.Client(ctx, t)
	fetch := func(path string, target any) error {
		req, e := http.NewRequestWithContext(ctx, "GET", p.APIURL+path, nil)
		if e != nil {
			return e
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, e := client.Do(req)
		if e != nil {
			return e
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			return fmt.Errorf("GitHub profile rejected")
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target)
	}
	var u struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Login string `json:"login"`
	}
	if e = fetch("/user", &u); e != nil {
		return Profile{}, fmt.Errorf("GitHub user: %w", e)
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if e = fetch("/user/emails", &emails); e != nil {
		return Profile{}, fmt.Errorf("GitHub emails: %w", e)
	}
	for _, email := range emails {
		if email.Primary && email.Verified && u.ID > 0 {
			name := u.Name
			if name == "" {
				name = u.Login
			}
			return Profile{fmt.Sprint(u.ID), email.Email, name, true}, nil
		}
	}
	return Profile{}, fmt.Errorf("primary verified email required")
}

type flow struct {
	State, Verifier, Nonce, Provider string
	Expires                          int64
}

func (s *Service) sign(value string) string {
	h := hmac.New(sha256.New, s.SigningKey)
	_, _ = h.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) begin(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "provider")
	entry, ok := s.provider(name)
	if !ok {
		Error(w, 404, "not_found", "provider not configured")
		return
	}
	state, e := Random(40)
	if e != nil {
		Error(w, 500, "internal", "cannot start login")
		return
	}
	verifier, e := Random(64)
	if e != nil {
		Error(w, 500, "internal", "cannot start login")
		return
	}
	nonce, e := Random(40)
	if e != nil {
		Error(w, 500, "internal", "cannot start login")
		return
	}
	b, _ := json.Marshal(flow{state, verifier, nonce, name, s.Clock.Now().Add(10 * time.Minute).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: "cp_oauth", Value: payload + "." + s.sign(payload), Path: "/auth/", MaxAge: 600, HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode})
	loginURL, err := entry.provider.LoginURL(r.Context(), ext.LoginState{State: state, Verifier: verifier, Nonce: nonce})
	if err != nil {
		Error(w, 500, "internal", "cannot start login")
		return
	}
	http.Redirect(w, r, loginURL, http.StatusFound)
}
func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	c, e := r.Cookie("cp_oauth")
	http.SetCookie(w, &http.Cookie{Name: "cp_oauth", Path: "/auth/", MaxAge: -1, HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode})
	if e != nil {
		Error(w, 400, "validation_failed", "invalid OAuth state")
		return
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 || !hmac.Equal([]byte(parts[1]), []byte(s.sign(parts[0]))) {
		Error(w, 400, "validation_failed", "invalid OAuth state")
		return
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	var f flow
	if e != nil || json.Unmarshal(b, &f) != nil || f.Expires <= s.Clock.Now().Unix() || f.Provider != chi.URLParam(r, "provider") || subtle.ConstantTimeCompare([]byte(f.State), []byte(r.URL.Query().Get("state"))) != 1 {
		Error(w, 400, "validation_failed", "invalid OAuth state")
		return
	}
	entry, ok := s.provider(f.Provider)
	if !ok {
		Error(w, 400, "validation_failed", "invalid provider")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ctx = ext.WithLoginState(ctx, ext.LoginState{State: f.State, Verifier: f.Verifier, Nonce: f.Nonce})
	identity, e := entry.provider.HandleCallback(ctx, r)
	profile := Profile{Subject: identity.Subject, Email: identity.Email, Name: identity.Name, Verified: identity.EmailVerified}
	if e != nil {
		Error(w, 403, "forbidden", "provider login rejected")
		return
	}
	u, e := s.Account(ctx, f.Provider, profile)
	if e != nil {
		// A browser lands here, so explain policy refusals on the sign-in page.
		switch {
		case errors.Is(e, ErrSignupClosed):
			http.Redirect(w, r, "/login?error=signup_closed", http.StatusSeeOther)
		case errors.Is(e, ErrDomainNotAllowed):
			http.Redirect(w, r, "/login?error=domain_not_allowed", http.StatusSeeOther)
		default:
			Error(w, 403, "forbidden", "account access refused")
		}
		return
	}
	if entry.scope.WorkspaceID != "" {
		if _, err := s.Store.ControlReader().WorkspaceMember(ctx, sqlitegen.WorkspaceMemberParams{UserID: u.ID, WorkspaceID: entry.scope.WorkspaceID}); err != nil {
			Error(w, 403, "forbidden", "workspace membership required")
			return
		}
	}
	if e = s.Login(r.Context(), w, r, u.ID); e != nil {
		Error(w, 500, "internal", "cannot create session")
		return
	}
	s.Sessions.Put(r.Context(), "provider", f.Provider)
	http.Redirect(w, r, "/", http.StatusFound)
}
