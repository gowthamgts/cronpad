// Package auth implements verified identities, sessions, API keys and tenancy.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

type Identity struct {
	UserID, WorkspaceID, KeyID string
	Scopes                     []string
}
type identityKey struct{}

func WithIdentity(ctx context.Context, i Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, i)
}
func Current(ctx context.Context) Identity { i, _ := ctx.Value(identityKey{}).(Identity); return i }
func Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
func Random(n int) (string, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		x, e := rand.Int(rand.Reader, big.NewInt(62))
		if e != nil {
			return "", fmt.Errorf("random: %w", e)
		}
		b[i] = alphabet[x.Int64()]
	}
	return string(b), nil
}

type Service struct {
	Store      store.Store
	Clock      clockwork.Clock
	Config     config.ConfigAuth
	Sessions   *scs.SessionManager
	Registry   *Registry
	Authz      *Authorization
	Providers  map[string]Provider
	SigningKey []byte
	Secure     bool
	Hosted     bool
	// CookieDomain is config.SharedCookieDomain: where the signed-in hint cookie is written ("" = no hint).
	CookieDomain string
	Limits       *limits.Registry
	touched      touchCache
	onTouch      func() // test hook: called for every issued last_used_at UPDATE
}

const (
	touchInterval = time.Minute
	touchCacheMax = 4096
)

// touchCache remembers when each API key last issued a last_used_at UPDATE so
// authenticated requests do not each take the single control write lock.
// DECISION: a plain map cleared when full; entries older than touchInterval are
// useless anyway, so eviction only costs one extra (guarded) UPDATE.
type touchCache struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// due reports whether key id should be touched at now and records the attempt.
func (c *touchCache) due(id string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if at, ok := c.last[id]; ok && now.Sub(at) < touchInterval {
		return false
	}
	if c.last == nil || len(c.last) >= touchCacheMax {
		clear(c.last)
		if c.last == nil {
			c.last = make(map[string]time.Time)
		}
	}
	c.last[id] = now
	return true
}

type sessionStore struct {
	ctx   context.Context
	s     store.Store
	clock clockwork.Clock
}

func (s *sessionStore) Delete(token string) error { return s.s.Control().DeleteSession(s.ctx, token) }
func (s *sessionStore) Find(token string) ([]byte, bool, error) {
	b, e := s.s.ControlReader().FindSession(s.ctx, sqlitegen.FindSessionParams{Token: token, Expiry: s.clock.Now().UnixMilli()})
	if errors.Is(e, sql.ErrNoRows) {
		return nil, false, nil
	}
	return b, e == nil, e
}
func (s *sessionStore) Commit(token string, b []byte, expiry time.Time) error {
	return s.s.Control().CommitSession(s.ctx, sqlitegen.CommitSessionParams{Token: token, Data: b, Expiry: expiry.UnixMilli()})
}
func New(ctx context.Context, s store.Store, clk clockwork.Clock, c config.Config, key []byte) *Service {
	sessions := scs.New()
	sessions.Store = &sessionStore{ctx, s, clk}
	sessions.Lifetime = c.Auth.SessionLifetime.Duration()
	sessions.Cookie.Name = "cp_session"
	sessions.Cookie.HttpOnly = true
	sessions.Cookie.SameSite = http.SameSiteLaxMode
	sessions.Cookie.Secure = strings.HasPrefix(c.Server.PublicURL, "https://")
	sessions.Cookie.Persist = true
	return &Service{Store: s, Clock: clk, Config: c.Auth, Sessions: sessions, Registry: &Registry{}, Authz: &Authorization{}, Providers: map[string]Provider{}, SigningKey: key, Secure: sessions.Cookie.Secure, Hosted: c.Mode == "hosted", CookieDomain: c.SharedCookieDomain()}
}

// signedInHint is a non-secret cookie on the parent domain shared with the public site. It only tells
// the site "probably signed in" so it can show a Dashboard link; it is never used for authorization,
// and a stale hint just leads to the dashboard's sign-in page.
const signedInHint = "cronpad_signed_in"

func (s *Service) setSignedInHint(w http.ResponseWriter) {
	if s.CookieDomain == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: signedInHint, Value: "1", Domain: s.CookieDomain, Path: "/", Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: int(s.Sessions.Lifetime.Seconds()), Expires: s.Clock.Now().Add(s.Sessions.Lifetime)})
}

// clearSignedInHint removes the hint; with a request, only when the browser actually sent one.
func (s *Service) clearSignedInHint(w http.ResponseWriter, r *http.Request) {
	if s.CookieDomain == "" {
		return
	}
	if r != nil {
		if _, e := r.Cookie(signedInHint); e != nil {
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: signedInHint, Domain: s.CookieDomain, Path: "/", Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}
func (s *Service) SignedIn(r *http.Request) bool {
	return s.Sessions.GetString(r.Context(), "user_id") != ""
}
func (s *Service) Middleware(next http.Handler) http.Handler {
	return s.middleware(next)
}

// APIKeyMiddleware authenticates MCP transport requests without granting tool scopes.
func (s *Service) APIKeyMiddleware(next http.Handler) http.Handler {
	authenticated := s.middleware(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			Error(w, 401, "unauthorized", "API key bearer required")
			return
		}
		authenticated.ServeHTTP(w, r)
	})
}
func (s *Service) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := Identity{}
		if h := r.Header.Get("Authorization"); h != "" {
			if !strings.HasPrefix(h, "Bearer ") {
				Error(w, 401, "unauthorized", "invalid API key")
				return
			}
			raw := strings.TrimPrefix(h, "Bearer ")
			parts := strings.Split(raw, "_")
			if len(parts) != 3 || parts[0] != "cp" || len(parts[1]) != 8 || len(parts[2]) != 32 {
				Error(w, 401, "unauthorized", "invalid API key")
				return
			}
			hash := sha256.Sum256([]byte(raw))
			key, e := s.Store.ControlReader().KeyByHash(r.Context(), hash[:])
			if e != nil || subtle.ConstantTimeCompare(hash[:], key.Hash) != 1 {
				Error(w, 401, "unauthorized", "invalid API key")
				return
			}
			i = Identity{UserID: key.CreatedBy.String, WorkspaceID: key.WorkspaceID, KeyID: key.ID}
			if json.Unmarshal([]byte(key.Scopes), &i.Scopes) != nil {
				Error(w, 401, "unauthorized", "invalid API key")
				return
			}
			if s.touched.due(key.ID, s.Clock.Now()) {
				if s.onTouch != nil {
					s.onTouch()
				}
				now := s.Clock.Now().UnixMilli()
				_ = s.Store.Control().TouchKey(r.Context(), sqlitegen.TouchKeyParams{ID: key.ID, Now: sql.NullInt64{Int64: now, Valid: true}, Before: sql.NullInt64{Int64: now - 60000, Valid: true}})
			}
		} else {
			i.UserID = s.Sessions.GetString(r.Context(), "user_id")
			if i.UserID == "" {
				s.clearSignedInHint(w, r) // the session expired or was revoked
				Error(w, 401, "unauthorized", "authentication required")
				return
			}
			i.Scopes = []string{"*"}
			workspace := r.Header.Get("Cronpad-Workspace")
			if workspace == "" {
				ws, e := s.Store.ControlReader().PersonalWorkspace(r.Context(), i.UserID)
				if e != nil {
					Error(w, 401, "unauthorized", "workspace unavailable")
					return
				}
				workspace = ws.ID
			}
			if _, e := s.Store.ControlReader().WorkspaceMember(r.Context(), sqlitegen.WorkspaceMemberParams{UserID: i.UserID, WorkspaceID: workspace}); e != nil {
				Error(w, 404, "not_found", "workspace not found")
				return
			}
			i.WorkspaceID = workspace
			if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
				cookie, e := r.Cookie("cp_csrf")
				token := r.Header.Get("X-CSRF-Token")
				expected := s.Sessions.GetString(r.Context(), "csrf")
				if e != nil || expected == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(expected)) != 1 || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
					Error(w, 403, "forbidden", "CSRF token required")
					return
				}
			}
		}
		if err := s.activeUser(r.Context(), i.UserID); err != nil {
			Error(w, 401, "unauthorized", "user unavailable")
			return
		}
		if i.KeyID == "" && r.URL.Path != "/auth/logout" {
			session := ext.Session{UserID: i.UserID, Provider: s.Sessions.GetString(r.Context(), "provider")}
			if err := s.Registry.check(r.Context(), session, i.WorkspaceID); err != nil {
				Error(w, 403, "forbidden", "session policy rejected access")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), i)))
	})
}
func (s *Service) RateLimit(n int) func(http.Handler) http.Handler {
	if n == 0 {
		return func(h http.Handler) http.Handler { return h }
	}
	return httprate.LimitBy(n, time.Minute, func(r *http.Request) (string, error) {
		i := Current(r.Context())
		if i.KeyID != "" {
			return i.KeyID, nil
		}
		return "session:" + s.Sessions.Token(r.Context()), nil
	}, httprate.WithResponseHeaders(httprate.ResponseHeaders{Limit: "RateLimit-Limit", Remaining: "RateLimit-Remaining", Reset: "RateLimit-Reset", RetryAfter: "Retry-After"}), httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) { Error(w, 429, "rate_limited", "rate limit exceeded") }))
}
func (s *Service) Login(ctx context.Context, w http.ResponseWriter, r *http.Request, user string) error {
	if err := s.activeUser(ctx, user); err != nil {
		return err
	}
	if e := s.Sessions.RenewToken(ctx); e != nil {
		return fmt.Errorf("renew session: %w", e)
	}
	token, e := Random(40)
	if e != nil {
		return e
	}
	s.Sessions.Put(ctx, "user_id", user)
	s.Sessions.Put(ctx, "provider", "")
	s.Sessions.Put(ctx, "csrf", token)
	http.SetCookie(w, &http.Cookie{Name: "cp_csrf", Value: token, Path: "/", Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: int(s.Sessions.Lifetime.Seconds()), Expires: s.Clock.Now().Add(s.Sessions.Lifetime)})
	s.setSignedInHint(w)
	return nil
}

// HasUsers reports whether anyone has an account yet; until then the dashboard shows onboarding.
func (s *Service) HasUsers(ctx context.Context) (bool, error) {
	n, err := s.Store.ControlReader().HasUsers(ctx)
	return n != 0, err
}

// Sign-in refusals the dashboard explains to the person signing in.
var (
	ErrSignupClosed     = errors.New("signup disabled")
	ErrDomainNotAllowed = errors.New("email domain not allowed")
)

// Account links only verified provider emails and creates a personal workspace atomically.
// With signup off, only the first user of an empty server may create an account.
func (s *Service) Account(ctx context.Context, provider string, p Profile) (sqlitegen.User, error) {
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	if !p.Verified || p.Subject == "" || !strings.Contains(p.Email, "@") {
		return sqlitegen.User{}, fmt.Errorf("verified email required")
	}
	parts := strings.Split(p.Email, "@")
	if len(parts) != 2 {
		return sqlitegen.User{}, fmt.Errorf("invalid email")
	}
	if len(s.Config.AllowedEmailDomains) > 0 {
		allowed := false
		for _, d := range s.Config.AllowedEmailDomains {
			if strings.EqualFold(parts[1], d) {
				allowed = true
			}
		}
		if !allowed {
			return sqlitegen.User{}, ErrDomainNotAllowed
		}
	}
	var user sqlitegen.User
	e := s.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		existing, e := tx.Control.IdentityUser(ctx, sqlitegen.IdentityUserParams{Provider: provider, Subject: p.Subject})
		if e == nil {
			user = existing
			if strings.EqualFold(user.Email, p.Email) {
				return nil
			}
			// The provider subject is the stable identity, so an address changed at the
			// provider never locks its owner out. Follow it unless another account holds it.
			holder, e := tx.Control.UserByEmail(ctx, p.Email)
			if e == nil && holder.ID != user.ID {
				slog.WarnContext(ctx, "provider email belongs to another account; keeping the old address", "user_id", user.ID, "provider", provider)
				return nil
			}
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if e = tx.Control.UpdateUserEmail(ctx, sqlitegen.UpdateUserEmailParams{Email: p.Email, ID: user.ID}); e != nil {
				return e
			}
			slog.InfoContext(ctx, "user email updated from provider", "user_id", user.ID, "provider", provider)
			user.Email = p.Email
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		user, e = tx.Control.UserByEmail(ctx, p.Email)
		if errors.Is(e, sql.ErrNoRows) {
			// Development login is local-only, so any name may sign in.
			if !s.Config.AllowSignup && provider != "dev" {
				// The insert below runs in this write transaction, so two first
				// sign-ins can't both see an empty server.
				exists, e := tx.Control.HasUsers(ctx)
				if e != nil {
					return e
				}
				if exists != 0 {
					return ErrSignupClosed
				}
			}
			now := s.Clock.Now()
			uid, e := id.New("usr", now)
			if e != nil {
				return e
			}
			wid, e := id.New(id.Workspace, now)
			if e != nil {
				return e
			}
			if e = tx.Control.InsertUser(ctx, sqlitegen.InsertUserParams{ID: uid, Email: p.Email, Name: sql.NullString{String: p.Name, Valid: true}, CreatedAt: now.UnixMilli()}); e != nil {
				return e
			}
			if e = tx.Control.CreateWorkspace(ctx, sqlitegen.CreateWorkspaceParams{ID: wid, Name: p.Name + "'s workspace", Slug: strings.ToLower(wid), Plan: "free", CreatedAt: now.UnixMilli()}); e != nil {
				return e
			}
			if e = tx.Control.SeedDevMember(ctx, sqlitegen.SeedDevMemberParams{UserID: uid, WorkspaceID: wid}); e != nil {
				return e
			}
			user, e = tx.Control.UserByID(ctx, uid)
			if e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		return tx.Control.LinkIdentity(ctx, sqlitegen.LinkIdentityParams{Provider: provider, Subject: p.Subject, UserID: user.ID, CreatedAt: s.Clock.Now().UnixMilli()})
	})
	if e != nil {
		return user, fmt.Errorf("account: %w", e)
	}
	if user.DeactivatedAt.Valid {
		return user, fmt.Errorf("user deactivated")
	}
	_ = s.Store.Control().TouchLogin(ctx, sqlitegen.TouchLoginParams{ID: user.ID, LastLoginAt: sql.NullInt64{Int64: s.Clock.Now().UnixMilli(), Valid: true}})
	return user, nil
}
func (s *Service) Mount(r chi.Router) {
	public := r
	if s.Hosted {
		public = r.With(httprate.LimitBy(10, time.Minute, func(r *http.Request) (string, error) {
			// Set by app.New from server.trusted_proxies; the TCP peer otherwise.
			if ip := middleware.GetClientIP(r.Context()); ip != "" {
				return ip, nil
			}
			host, _, e := net.SplitHostPort(r.RemoteAddr)
			if e != nil {
				host = r.RemoteAddr
			}
			return httprate.CanonicalizeIP(host), nil
		}, httprate.WithResponseHeaders(httprate.ResponseHeaders{Limit: "RateLimit-Limit", Remaining: "RateLimit-Remaining", Reset: "RateLimit-Reset", RetryAfter: "Retry-After"}), httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
			Error(w, 429, "rate_limited", "sign-in rate limit exceeded")
		})))
	}
	public.Get("/auth/sso", s.sso)
	public.Get("/auth/{provider}/login", s.begin)
	public.Get("/auth/{provider}/callback", s.callback)
	r.With(s.Middleware).Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if e := s.Sessions.Destroy(r.Context()); e != nil {
			Error(w, 500, "internal", "cannot destroy session")
			return
		}
		s.clearSignedInHint(w, nil)
		w.WriteHeader(204)
	})
	if s.Config.DevLogin {
		r.Post("/auth/dev/login", func(w http.ResponseWriter, r *http.Request) {
			if e := r.ParseForm(); e != nil {
				Error(w, 400, "validation_failed", "invalid form")
				return
			}
			name := r.Form.Get("name")
			if name == "" || len(name) > 200 {
				Error(w, 400, "validation_failed", "name required")
				return
			}
			u, e := s.Account(r.Context(), "dev", Profile{Subject: name, Email: fmt.Sprintf("%x@dev.invalid", sha256.Sum256([]byte(name))), Name: name, Verified: true})
			if e != nil {
				Error(w, 403, "forbidden", "login refused")
				return
			}
			if e = s.Login(r.Context(), w, r, u.ID); e != nil {
				Error(w, 500, "internal", "cannot create session")
				return
			}
			w.WriteHeader(204)
		})
	}
}

// PrepareKey creates a credential and its insert parameters without persisting it.
func (s *Service) PrepareKey(workspace, user, name string, scopes []string) (sqlitegen.CreateKeyParams, string, error) {
	prefix, e := Random(8)
	if e != nil {
		return sqlitegen.CreateKeyParams{}, "", e
	}
	secret, e := Random(32)
	if e != nil {
		return sqlitegen.CreateKeyParams{}, "", e
	}
	raw := "cp_" + prefix + "_" + secret
	hash := sha256.Sum256([]byte(raw))
	kid, e := id.New(id.Key, s.Clock.Now())
	if e != nil {
		return sqlitegen.CreateKeyParams{}, "", e
	}
	b, e := json.Marshal(scopes)
	if e != nil {
		return sqlitegen.CreateKeyParams{}, "", e
	}
	return sqlitegen.CreateKeyParams{ID: kid, WorkspaceID: workspace, Name: name, Prefix: "cp_" + prefix, Hash: hash[:], Scopes: string(b), CreatedBy: sql.NullString{String: user, Valid: user != ""}, CreatedAt: s.Clock.Now().UnixMilli()}, raw, nil
}

// MintKey returns the credential exactly once; only its digest is persisted.
func (s *Service) MintKey(ctx context.Context, workspace, user, name string, scopes []string) (sqlitegen.ApiKey, string, error) {
	params, raw, e := s.PrepareKey(workspace, user, name, scopes)
	if e != nil {
		return sqlitegen.ApiKey{}, "", e
	}
	var key sqlitegen.ApiKey
	e = s.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		if s.Limits != nil {
			n, e := tx.Control.CountKeys(ctx, workspace)
			if e != nil {
				return e
			}
			if e = s.Limits.Check(ctx, workspace, limits.Action{Limit: "max_api_keys_per_workspace", Value: n + 1}); e != nil {
				return e
			}
		}
		var e error
		key, e = tx.Control.CreateKey(ctx, params)
		return e
	})
	return key, raw, e
}
