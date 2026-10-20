package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

type planProvider struct {
	s     store.Store
	calls int
}

func (p *planProvider) For(ctx context.Context, ws string) (ext.LimitSet, error) {
	p.calls++
	w, err := p.s.ControlReader().GetWorkspace(ctx, ws)
	if err != nil {
		return ext.LimitSet{}, err
	}
	maxJobs := 1
	if w.Plan == "pro" {
		maxJobs = 10
	}
	return ext.LimitSet{MaxJobs: maxJobs}, nil
}
func TestAdministrationPlanInvalidatesCache(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	u, err := s.Account(ctx, "test", Profile{Subject: "plan", Email: "plan@example.com", Name: "Plan", Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := s.Store.ControlReader().PersonalWorkspace(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	quotas := &limits.Registry{Store: s.Store, Clock: s.Clock}
	provider := &planProvider{s: s.Store}
	if err = quotas.Register("plan", provider); err != nil {
		t.Fatal(err)
	}
	before, err := quotas.For(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	admin := &Administration{Auth: s, Store: s.Store, Clock: s.Clock, Limits: quotas}
	if err = admin.SetPlan(ctx, ws.ID, "pro"); err != nil {
		t.Fatal(err)
	}
	after, err := quotas.For(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.MaxJobs != 1 || after.MaxJobs != 10 || provider.calls != 2 {
		t.Fatalf("before=%v after=%v calls=%d", before, after, provider.calls)
	}
}
func TestSessionPolicyAndDirectoryDeactivation(t *testing.T) {
	s := testService(t)
	s.Config.AllowSignup = true
	ctx := context.Background()
	u, err := s.Account(ctx, "test", Profile{Subject: "active", Email: "active@example.com", Name: "Active", Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Account(ctx, "test", Profile{Subject: "other", Email: "other@example.com", Name: "Other", Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := s.Store.ControlReader().PersonalWorkspace(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := s.MintKey(ctx, ws.ID, u.ID, "test", []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Get("/login", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Login(r.Context(), w, r, u.ID); err != nil {
			t.Error(err)
		}
	})
	r.With(s.Middleware).Get("/protected", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	server := httptest.NewServer(s.Sessions.LoadAndSave(r))
	defer server.Close()
	client := server.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	get := func(path, key string, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), "GET", server.URL+path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s: %d want %d", path, resp.StatusCode, want)
		}
	}
	get("/login", "", 200)
	get("/protected", "", 204)
	// Keep an unrelated user's session to prove targeted rather than global revocation.
	data, err := s.Sessions.Codec.Encode(s.Clock.Now().Add(time.Hour), map[string]any{"user_id": other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Sessions.Store.Commit("other-token", data, s.Clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.Registry.AddSessionPolicy(func(_ context.Context, session ext.Session, workspace string) error {
		if session.UserID != u.ID || workspace != ws.ID {
			t.Error("policy context mismatch")
		}
		return errors.New("SSO required")
	})
	get("/protected", "", 403)
	get("/protected", raw, 204)
	admin := &Administration{Auth: s, Store: s.Store, Clock: s.Clock}
	if err = admin.DeactivateUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	get("/protected", "", 401)
	get("/protected", raw, 401)
	sessions, err := s.Store.ControlReader().DirectorySessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Token != "other-token" {
		t.Fatalf("sessions after deactivation: %v", sessions)
	}
	if _, err = s.Account(ctx, "test", Profile{Subject: "active", Email: "active@example.com", Name: "Active", Verified: true}); err == nil {
		t.Fatal("deactivated user logged in")
	}
	events, err := s.Store.HistoryReader().ListAudit(ctx, historygen.ListAuditParams{WorkspaceID: "", Action: "", Cursor: "~", PageSize: 100})
	if err != nil || len(events) == 0 {
		t.Fatal("deactivation audit missing", err)
	}
}
func TestAuditOnlyAfterCommitAndNeverBlocksWriter(t *testing.T) {
	s := testService(t)
	repository := s.Store.(*store.SQLite)
	ctx := context.Background()
	bus := NewAuditBus(nil)
	bus.Subscribe(func(context.Context, ext.AuditEvent) {})
	repository.SetAuditListener(bus.Publish, bus.Active)
	failed := errors.New("rollback")
	event := historygen.WriteAuditParams{ID: "audit_rollback", WorkspaceID: "ws", Actor: "system", Action: "test", CreatedAt: 1}
	err := repository.WithTx(ctx, store.History, func(tx store.Tx) error {
		if err := tx.History.WriteAudit(ctx, event); err != nil {
			return err
		}
		if len(bus.queue) != 0 {
			t.Fatal("event before commit")
		}
		return failed
	})
	if !errors.Is(err, failed) || len(bus.queue) != 0 {
		t.Fatal("rollback published event", err)
	}
	event.ID = "audit_commit"
	if err = repository.WithTx(ctx, store.History, func(tx store.Tx) error { return tx.History.WriteAudit(ctx, event) }); err != nil {
		t.Fatal(err)
	}
	if len(bus.queue) != 1 {
		t.Fatal("commit not published")
	}
	committed := <-bus.queue
	if committed.ID != event.ID {
		t.Fatal(committed)
	}
	// Fill the queue without a consumer. Neither Publish nor the SQL writer waits.
	for n := 0; n < cap(bus.queue)+1; n++ {
		bus.Publish(ctx, event)
	}
	event.ID = "audit_overflow"
	if err = repository.History().WriteAudit(ctx, event); err != nil {
		t.Fatal(err)
	}
	if len(bus.queue) != cap(bus.queue) {
		t.Fatal("queue was not bounded")
	}
	if err = repository.History().WriteAudit(ctx, event); err == nil {
		t.Fatal("duplicate audit accepted")
	}
	var n int
	if err = repository.DBs.History.Reader.QueryRowContext(ctx, "SELECT count(*) FROM audit_log WHERE id=?", event.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestProvisionedUserCanSignIn(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	admin := &Administration{Auth: s, Store: s.Store, Clock: s.Clock}
	uid, err := admin.UpsertUser(ctx, ext.DirectoryUser{Email: "provisioned@example.com", Name: "Provisioned"})
	if err != nil {
		t.Fatal(err)
	}
	// Existing directory accounts remain usable when public signup is disabled.
	s.Config.AllowSignup = false
	user, err := s.Account(ctx, "oidc:default", Profile{Subject: "provisioned", Email: "provisioned@example.com", Name: "Provisioned", Verified: true})
	if err != nil || user.ID != uid {
		t.Fatal(user, err)
	}
	if _, err = s.Store.ControlReader().PersonalWorkspace(ctx, uid); err != nil {
		t.Fatal("provisioned login lacks default workspace", err)
	}
}

func TestAuditTransactionCursorUsesBoundedBatches(t *testing.T) {
	s := testService(t)
	repository := s.Store.(*store.SQLite)
	ctx := context.Background()
	prior := historygen.WriteAuditParams{ID: "audit_prior", WorkspaceID: "ws", Actor: "system", Action: "prior", CreatedAt: 1}
	if err := repository.History().WriteAudit(ctx, prior); err != nil {
		t.Fatal(err)
	}
	var published []historygen.WriteAuditParams
	repository.SetAuditListener(func(_ context.Context, p historygen.WriteAuditParams) { published = append(published, p) })
	err := repository.WithTx(ctx, store.History, func(tx store.Tx) error {
		for n := 0; n < 300; n++ {
			p := prior
			p.ID = fmt.Sprintf("audit_batch_%03d", n)
			p.Action = "batch"
			if err := tx.History.WriteAudit(ctx, p); err != nil {
				return err
			}
		}
		if len(published) != 0 {
			t.Fatal("publication before commit")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 300 {
		t.Fatalf("committed batch count=%d", len(published))
	}
	for _, p := range published {
		if p.Action != "batch" {
			t.Fatalf("published prior transaction: %v", p)
		}
	}
}
