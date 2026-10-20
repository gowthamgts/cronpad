package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

// NoEntitlements is the OSS feature policy.
type NoEntitlements struct{}

func (NoEntitlements) Has(context.Context, string, ext.Feature) bool { return false }

// AuditBus owns a bounded, nonblocking queue of committed audit events.
// DECISION: overflow drops the newest event and emits a warning. Core history is
// authoritative; slow subscribers never delay a database writer.
type AuditBus struct {
	mu          sync.RWMutex
	subscribers []func(context.Context, ext.AuditEvent)
	queue       chan ext.AuditEvent
	Log         *slog.Logger
}

func NewAuditBus(log *slog.Logger) *AuditBus {
	return &AuditBus{queue: make(chan ext.AuditEvent, 256), Log: log}
}

// Active reports whether transaction audit capture is needed.
func (b *AuditBus) Active() bool { b.mu.RLock(); defer b.mu.RUnlock(); return len(b.subscribers) > 0 }
func (b *AuditBus) Subscribe(fn func(context.Context, ext.AuditEvent)) {
	if fn == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers = append(b.subscribers, fn)
}
func (b *AuditBus) Publish(ctx context.Context, p historygen.WriteAuditParams) {
	b.mu.RLock()
	active := len(b.subscribers) > 0
	b.mu.RUnlock()
	if !active {
		return
	}
	e := ext.AuditEvent{ID: p.ID, WorkspaceID: p.WorkspaceID, Actor: p.Actor, Action: p.Action, Target: p.Target.String, CreatedAt: time.UnixMilli(p.CreatedAt).UTC()}
	if p.Details.Valid {
		e.Details = json.RawMessage(p.Details.String)
	}
	select {
	case b.queue <- e:
	default:
		if b.Log != nil {
			b.Log.WarnContext(ctx, "audit subscriber queue overflow", logx.WorkspaceID, p.WorkspaceID)
		}
	}
}
func (b *AuditBus) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-b.queue:
			b.mu.RLock()
			subscribers := append([]func(context.Context, ext.AuditEvent){}, b.subscribers...)
			b.mu.RUnlock()
			for _, fn := range subscribers {
				b.deliver(ctx, fn, event)
			}
		}
	}
}
func (b *AuditBus) deliver(ctx context.Context, fn func(context.Context, ext.AuditEvent), e ext.AuditEvent) {
	defer func() {
		if p := recover(); p != nil && b.Log != nil {
			b.Log.ErrorContext(ctx, "audit subscriber panic", logx.Error, fmt.Sprint(p))
		}
	}()
	fn(ctx, e)
}

// Administration implements the public directory and workspace administration hooks.
// DECISION: control commits precede history audit commits, matching the two-file
// suspension service. Audit failure is returned without undoing a committed control write.
type Administration struct {
	Auth   *Service
	Store  store.Store
	Clock  clockwork.Clock
	Limits *limits.Registry
}

func (a *Administration) audit(ctx context.Context, ws, action, target string, details any) error {
	aid, err := id.New(id.Run, a.Clock.Now())
	if err != nil {
		return err
	}
	data, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("audit details: %w", err)
	}
	return a.Store.History().WriteAudit(ctx, historygen.WriteAuditParams{ID: "audit_" + aid, WorkspaceID: ws, Actor: "system", Action: action, Target: sql.NullString{String: target, Valid: target != ""}, Details: sql.NullString{String: string(data), Valid: true}, CreatedAt: a.Clock.Now().UnixMilli()})
}
func (a *Administration) UpsertUser(ctx context.Context, u ext.DirectoryUser) (string, error) {
	email := strings.ToLower(strings.TrimSpace(u.Email))
	if strings.Count(email, "@") != 1 || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return "", fmt.Errorf("invalid directory email")
	}
	uid := u.ID
	err := a.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		if uid != "" {
			n, err := tx.Control.UpdateDirectoryUser(ctx, sqlitegen.UpdateDirectoryUserParams{ID: uid, Email: email, Name: sql.NullString{String: u.Name, Valid: true}})
			if err != nil {
				return err
			}
			if n == 0 {
				return sql.ErrNoRows
			}
			return nil
		}
		generated, err := id.New("usr", a.Clock.Now())
		if err != nil {
			return err
		}
		row, err := tx.Control.UpsertDirectoryUser(ctx, sqlitegen.UpsertDirectoryUserParams{ID: generated, Email: email, Name: sql.NullString{String: u.Name, Valid: true}, CreatedAt: a.Clock.Now().UnixMilli()})
		if err != nil {
			return err
		}
		uid = row.ID
		if uid == generated {
			// DECISION: provision the same personal workspace as OAuth signup, so a
			// directory-created user can use the normal default-workspace session flow.
			wid, err := id.New(id.Workspace, a.Clock.Now())
			if err != nil {
				return err
			}
			if err = tx.Control.CreateWorkspace(ctx, sqlitegen.CreateWorkspaceParams{ID: wid, Name: u.Name + "'s workspace", Slug: strings.ToLower(wid), Plan: "free", CreatedAt: a.Clock.Now().UnixMilli()}); err != nil {
				return err
			}
			return tx.Control.SeedDevMember(ctx, sqlitegen.SeedDevMemberParams{WorkspaceID: wid, UserID: uid})
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("provision user: %w", err)
	}
	if err = a.audit(ctx, "", "directory.user.upsert", uid, map[string]string{"email": email}); err != nil {
		return uid, err
	}
	return uid, nil
}
func (a *Administration) SetMembership(ctx context.Context, ws, user, role string) error {
	if role == "" {
		return fmt.Errorf("membership role required")
	}
	if err := a.Store.Control().SetMembership(ctx, sqlitegen.SetMembershipParams{WorkspaceID: ws, UserID: user, Role: role}); err != nil {
		return fmt.Errorf("set membership: %w", err)
	}
	return a.audit(ctx, ws, "directory.membership.set", user, map[string]string{"role": role})
}
func (a *Administration) RemoveMembership(ctx context.Context, ws, user string) error {
	n, err := a.Store.Control().RemoveMembership(ctx, sqlitegen.RemoveMembershipParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		return fmt.Errorf("remove membership: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return a.audit(ctx, ws, "directory.membership.remove", user, nil)
}
func (a *Administration) DeactivateUser(ctx context.Context, user string) error {
	err := a.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		n, err := tx.Control.DeactivateUser(ctx, sqlitegen.DeactivateUserParams{ID: user, DeactivatedAt: sql.NullInt64{Int64: a.Clock.Now().UnixMilli(), Valid: true}})
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		if err = tx.Control.RevokeUserKeys(ctx, sqlitegen.RevokeUserKeysParams{CreatedBy: sql.NullString{String: user, Valid: true}, RevokedAt: sql.NullInt64{Int64: a.Clock.Now().UnixMilli(), Valid: true}}); err != nil {
			return err
		}
		sessions, err := tx.Control.DirectorySessions(ctx)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			_, values, err := a.Auth.Sessions.Codec.Decode(session.Data)
			if err != nil {
				return fmt.Errorf("decode session: %w", err)
			}
			if values["user_id"] == user {
				if err = tx.Control.DeleteSession(ctx, session.Token); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("deactivate user: %w", err)
	}
	return a.audit(ctx, "", "directory.user.deactivate", user, nil)
}
func (a *Administration) SetPlan(ctx context.Context, ws, plan string) error {
	if strings.TrimSpace(plan) == "" {
		return fmt.Errorf("plan required")
	}
	n, err := a.Store.Control().SetWorkspacePlan(ctx, sqlitegen.SetWorkspacePlanParams{ID: ws, Plan: plan})
	if err != nil {
		return fmt.Errorf("set plan: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if a.Limits != nil {
		a.Limits.Invalidate(ws)
	}
	return a.audit(ctx, ws, "workspace.plan", ws, map[string]string{"plan": plan})
}
func (a *Administration) Suspend(ctx context.Context, ws, reason string) error {
	return store.SetSuspension(ctx, a.Store, ws, reason, true, a.Clock.Now())
}
func (a *Administration) Unsuspend(ctx context.Context, ws string) error {
	return store.SetSuspension(ctx, a.Store, ws, "", false, a.Clock.Now())
}

func (s *Service) activeUser(ctx context.Context, user string) error {
	if user == "" {
		return nil
	}
	u, err := s.Store.ControlReader().UserByID(ctx, user)
	if err != nil {
		return fmt.Errorf("user unavailable: %w", err)
	}
	if u.DeactivatedAt.Valid {
		return errors.New("user deactivated")
	}
	return nil
}
