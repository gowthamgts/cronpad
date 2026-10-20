// Package hello demonstrates the public extension contract without internal imports.
package hello

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/pkg/ext"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Config struct {
	Greeting string `koanf:"greeting"`
}
type Extension struct {
	Config Config
	// Hooks exposes the captured public dependencies for the example's integration tests.
	Hooks      ext.Deps
	Events     chan ext.AuditEvent
	Authorized atomic.Int64
	Policies   atomic.Int64
}

func New() *Extension {
	return &Extension{Config: Config{Greeting: "hello"}, Events: make(chan ext.AuditEvent, 32)}
}
func (*Extension) Name() string                   { return "hello" }
func (e *Extension) ConfigSection() (string, any) { return "hello", &e.Config }
func (*Extension) Migrations() (ext.DB, fs.FS) {
	sub, _ := fs.Sub(migrations, "migrations")
	return ext.Control, sub
}
func (e *Extension) Routes(r chi.Router, d ext.Deps) {
	e.Hooks = d
	d.Auth.Register(dummyProvider{}, ext.Global)
	d.Auth.SetDiscovery(func(_ context.Context, email string) (string, bool) { return "hello", email == "hello@example.invalid" })
	d.Auth.AddSessionPolicy(func(context.Context, ext.Session, string) error { e.Policies.Add(1); return nil })
	d.Authz.Set(exampleAuthorizer{e: e})
	d.Audit.Subscribe(func(ctx context.Context, event ext.AuditEvent) {
		select {
		case e.Events <- event:
		case <-ctx.Done():
		default:
		}
	})
	r.Get("/hello", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(e.Config.Greeting + "\n")) })
}
func (*Extension) Start(ctx context.Context, _ ext.Deps) error { <-ctx.Done(); return nil }

// dummyProvider demonstrates the public provider contract without accepting
// unsigned identities. A real provider validates its assertion in HandleCallback.
type dummyProvider struct{}

func (dummyProvider) ID() string { return "hello" }
func (dummyProvider) LoginURL(_ context.Context, s ext.LoginState) (string, error) {
	return "/hello?state=" + url.QueryEscape(s.State), nil
}
func (dummyProvider) HandleCallback(context.Context, *http.Request) (ext.Identity, error) {
	return ext.Identity{}, fmt.Errorf("demonstration provider cannot authenticate")
}

// exampleAuthorizer demonstrates replacement while retaining the OSS role policy.
type exampleAuthorizer struct{ e *Extension }

func (a exampleAuthorizer) Authorize(_ context.Context, p ext.Principal, action ext.Action, r ext.Resource) error {
	a.e.Authorized.Add(1)
	if p.WorkspaceID == "" || (r.WorkspaceID != "" && r.WorkspaceID != p.WorkspaceID) {
		return fmt.Errorf("forbidden")
	}
	if p.Role == "owner" {
		return nil
	}
	if p.Role != "member" {
		return fmt.Errorf("forbidden")
	}
	switch action {
	case ext.MembersManage, ext.WorkspaceAdminAction:
		return fmt.Errorf("forbidden")
	case ext.KeysManage:
		if p.UserID == "" || p.UserID != r.OwnerID {
			return fmt.Errorf("forbidden")
		}
	}
	return nil
}
