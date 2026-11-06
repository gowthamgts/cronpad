package entitlements

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/pkg/ext"
)

type fake struct {
	grant map[string]ext.Feature
	calls int
}

func (f *fake) Has(_ context.Context, ws string, feature ext.Feature) bool {
	f.calls++
	return f.grant[ws] == feature
}

func TestNoProviderDeniesEverything(t *testing.T) {
	var r Registry
	if r.Has(t.Context(), "ws", ext.SAML) {
		t.Fatal("entitled without a provider")
	}
}

func TestRegisterRejectsDuplicatesAndNil(t *testing.T) {
	var r Registry
	if err := r.Register("a", &fake{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("a", &fake{}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := r.Register("b", nil); err == nil {
		t.Fatal("nil accepted")
	}
}

func TestHasAnyProviderGrantsAndCaches(t *testing.T) {
	clk := clockwork.NewFakeClock()
	r := &Registry{Clock: clk}
	p := &fake{grant: map[string]ext.Feature{"ws": ext.SCIM}}
	if err := r.Register("p", p); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("other", &fake{}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if !r.Has(ctx, "ws", ext.SCIM) || !r.Has(ctx, "ws", ext.SCIM) || p.calls != 1 {
		t.Fatalf("calls=%d", p.calls)
	}
	if r.Has(ctx, "ws", ext.SAML) || r.Has(ctx, "ws2", ext.SCIM) {
		t.Fatal("unexpected grant")
	}
	p.grant["ws"] = ext.SAML
	if !r.Has(ctx, "ws", ext.SCIM) {
		t.Fatal("cache not used")
	}
	clk.Advance(time.Minute)
	if r.Has(ctx, "ws", ext.SCIM) || !r.Has(ctx, "ws", ext.SAML) {
		t.Fatal("expired entry not refreshed")
	}
}

func TestInvalidateDropsWorkspaceEntries(t *testing.T) {
	r := &Registry{Clock: clockwork.NewFakeClock()}
	p := &fake{grant: map[string]ext.Feature{"ws": ext.RBAC}}
	if err := r.Register("p", p); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	r.Has(ctx, "ws", ext.RBAC)
	r.Has(ctx, "keep", ext.RBAC)
	p.grant["ws"] = ext.HA
	r.Invalidate("ws")
	if r.Has(ctx, "ws", ext.RBAC) || !r.Has(ctx, "ws", ext.HA) {
		t.Fatal("invalidate did not refresh")
	}
	if p.calls != 4 {
		t.Fatalf("calls=%d; other workspace should stay cached", p.calls)
	}
}
