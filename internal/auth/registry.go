package auth

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"github.com/gowthamgts/cronpad/pkg/ext"
)

type registeredProvider struct {
	provider ext.AuthProvider
	scope    ext.ProviderScope
}

// Registry is shared by built-in and extension login providers.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]registeredProvider
	discovery func(context.Context, string) (string, bool)
	policies  []func(context.Context, ext.Session, string) error
}

func (r *Registry) Register(p ext.AuthProvider, scope ext.ProviderScope) {
	if p == nil || p.ID() == "" {
		panic("auth: provider ID required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]registeredProvider)
	}
	r.providers[p.ID()] = registeredProvider{p, scope}
}
func (r *Registry) SetDiscovery(fn func(context.Context, string) (string, bool)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discovery = fn
}
func (r *Registry) AddSessionPolicy(fn func(context.Context, ext.Session, string) error) {
	if fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policies = append(r.policies, fn)
}
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []string{}
	for name, p := range r.providers {
		if p.scope.WorkspaceID == "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
func (r *Registry) provider(id string) (registeredProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}
func (r *Registry) check(ctx context.Context, s ext.Session, ws string) error {
	r.mu.RLock()
	policies := append([]func(context.Context, ext.Session, string) error{}, r.policies...)
	r.mu.RUnlock()
	for _, fn := range policies {
		if err := fn(ctx, s, ws); err != nil {
			return err
		}
	}
	return nil
}
func (r *Registry) discover(ctx context.Context, email string) (string, bool) {
	r.mu.RLock()
	fn := r.discovery
	r.mu.RUnlock()
	if fn == nil {
		return "", false
	}
	return fn(ctx, email)
}

// ProviderAdapter keeps the v1 Provider contract usable while routing all login
// traffic through the public registry. Callback state is validated by core first.
type ProviderAdapter struct {
	Name     string
	Provider Provider
}

func (p ProviderAdapter) ID() string { return p.Name }
func (p ProviderAdapter) LoginURL(_ context.Context, s ext.LoginState) (string, error) {
	return p.Provider.AuthorizationURL(s.State, s.Verifier, s.Nonce), nil
}

func (p ProviderAdapter) HandleCallback(ctx context.Context, r *http.Request) (ext.Identity, error) {
	state, ok := ext.LoginStateFromContext(ctx)
	if !ok {
		return ext.Identity{}, fmt.Errorf("missing validated login state")
	}
	profile, err := p.Provider.Exchange(ctx, r.URL.Query().Get("code"), state.Verifier, state.Nonce)
	return ext.Identity{Subject: profile.Subject, Email: profile.Email, Name: profile.Name, EmailVerified: profile.Verified}, err
}
func (s *Service) provider(name string) (registeredProvider, bool) {
	if p, ok := s.Registry.provider(name); ok {
		return p, true
	}
	// v1 tests and embedders may still populate Providers directly.
	if p := s.Providers[name]; p != nil {
		return registeredProvider{ProviderAdapter{Name: name, Provider: p}, ext.Global}, true
	}
	return registeredProvider{}, false
}
func (s *Service) sso(w http.ResponseWriter, r *http.Request) {
	id, ok := s.Registry.discover(r.Context(), r.URL.Query().Get("email"))
	if !ok {
		Error(w, 404, "not_found", "provider not found")
		return
	}
	if _, ok = s.provider(id); !ok {
		Error(w, 404, "not_found", "provider not configured")
		return
	}
	http.Redirect(w, r, "/auth/"+id+"/login", http.StatusFound)
}
