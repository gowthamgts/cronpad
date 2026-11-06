// Package entitlements answers optional-feature checks from registered providers.
package entitlements

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/pkg/ext"
)

type key struct {
	ws      string
	feature ext.Feature
}
type cached struct {
	has   bool
	until time.Time
}

// Registry is the active ext.Entitlements. With no provider every feature is off.
type Registry struct {
	Clock     clockwork.Clock
	mu        sync.Mutex
	providers map[string]ext.Entitlements
	order     []string
	cache     map[key]cached
}

func (r *Registry) now() time.Time {
	if r.Clock == nil {
		return clockwork.NewRealClock().Now()
	}
	return r.Clock.Now()
}

// Register adds a provider; a duplicate name is an error.
func (r *Registry) Register(name string, provider ext.Entitlements) error {
	if provider == nil {
		return fmt.Errorf("invalid entitlements provider")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[name]; ok {
		return fmt.Errorf("duplicate entitlements provider %q", name)
	}
	if r.providers == nil {
		r.providers = map[string]ext.Entitlements{}
	}
	r.providers[name] = provider
	r.order = append(r.order, name)
	r.cache = nil
	return nil
}

// Invalidate makes a committed workspace plan change visible immediately.
func (r *Registry) Invalidate(ws string) {
	r.mu.Lock()
	for k := range r.cache {
		if k.ws == ws {
			delete(r.cache, k)
		}
	}
	r.mu.Unlock()
}

// Has reports whether any provider grants the feature; results are cached for a minute.
// DECISION: the lock is held across provider calls, like limits.Registry.For, so a
// slow provider serializes lookups; providers must return promptly.
func (r *Registry) Has(ctx context.Context, ws string, f ext.Feature) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 {
		return false
	}
	k := key{ws, f}
	if c, ok := r.cache[k]; ok && r.now().Before(c.until) {
		return c.has
	}
	has := false
	for _, name := range r.order {
		if r.providers[name].Has(ctx, ws, f) {
			has = true
			break
		}
	}
	if r.cache == nil {
		r.cache = map[key]cached{}
	}
	r.cache[k] = cached{has, r.now().Add(time.Minute)}
	return has
}
