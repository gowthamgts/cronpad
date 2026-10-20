package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

// ErrForbidden identifies an authorization denial.
var ErrForbidden = errors.New("forbidden")

// DefaultAuthorizer preserves the OSS owner/member policy.
type DefaultAuthorizer struct{}

func (DefaultAuthorizer) Authorize(_ context.Context, p ext.Principal, a ext.Action, r ext.Resource) error {
	if p.WorkspaceID == "" || (r.WorkspaceID != "" && r.WorkspaceID != p.WorkspaceID) {
		return ErrForbidden
	}
	if p.Role == "owner" {
		return nil
	}
	if p.Role != "member" {
		return ErrForbidden
	}
	switch a {
	case ext.MembersManage, ext.WorkspaceAdminAction:
		return ErrForbidden
	case ext.KeysManage:
		if p.UserID == "" || r.OwnerID != p.UserID {
			return ErrForbidden
		}
	}
	return nil
}

// Authorization owns the last-wins authorizer. Key scopes are always intersected
// outside the extension hook, so a custom authorizer cannot expand a credential.
type Authorization struct {
	mu     sync.RWMutex
	active ext.Authorizer
	Log    *slog.Logger
}

// Set implements the context-free extension registry API. Startup code with a
// parent context should use SetContext instead.
func (a *Authorization) Set(next ext.Authorizer) {
	a.SetContext(context.Background(), next)
}

// SetContext preserves startup cancellation, trace and logging metadata.
func (a *Authorization) SetContext(ctx context.Context, next ext.Authorizer) {
	if next == nil {
		next = DefaultAuthorizer{}
	}
	a.mu.Lock()
	a.active = next
	a.mu.Unlock()
	if a.Log != nil {
		a.Log.InfoContext(ctx, "authorization provider active", logx.Authorizer, fmt.Sprintf("%T", next))
	}
}
func (a *Authorization) Authorize(ctx context.Context, p ext.Principal, action ext.Action, r ext.Resource) error {
	if p.KeyID != "" && !ScopeAllowed(p.Scopes, RequiredScope(action, r.Kind)) {
		return ErrForbidden
	}
	var active ext.Authorizer = DefaultAuthorizer{}
	if a != nil {
		a.mu.RLock()
		if a.active != nil {
			active = a.active
		}
		a.mu.RUnlock()
	}
	return active.Authorize(ctx, p, action, r)
}

// RequiredScope maps an explicit action and its resource family to the v1
// credential policy. An action can be shared by jobs and workspace metadata;
// those operations deliberately have different API-key requirements.
func RequiredScope(action ext.Action, kind string) string {
	switch kind {
	case "jobs":
		switch action {
		case ext.JobsRead:
			return "jobs:read"
		case ext.RunsRead:
			return "runs:read"
		case ext.JobsWrite, ext.JobsTrigger:
			return "jobs:write"
		}
	case "runs":
		switch action {
		case ext.RunsRead:
			return "runs:read"
		case ext.RunsRetry:
			return "jobs:write"
		}
	case "schedules":
		// DECISION: retain v1's public schedule utilities for MCP credentials too.
		// MCP still invokes the authorizer, but these operations need no key scope.
		if action == ext.JobsRead {
			return ""
		}
	}
	return "*"
}

// ScopeAllowed applies the exact credential requirement; v2 action names do
// not implicitly grant access to endpoints that v1 restricted to full access.
func ScopeAllowed(scopes []string, required string) bool {
	if required == "" {
		return true
	}
	for _, scope := range scopes {
		if scope == "*" || scope == required {
			return true
		}
	}
	return false
}

// Principal resolves membership on every operation so revocation takes effect immediately.
func (s *Service) Principal(ctx context.Context) (ext.Principal, error) {
	i := Current(ctx)
	p := ext.Principal{UserID: i.UserID, WorkspaceID: i.WorkspaceID, KeyID: i.KeyID, Scopes: i.Scopes}
	// DECISION: offline operator keys without a creator retain their v1 workspace authority.
	if i.KeyID != "" && i.UserID == "" {
		p.Role = "owner"
		return p, nil
	}
	role, err := s.Store.ControlReader().WorkspaceMember(ctx, sqlitegen.WorkspaceMemberParams{UserID: i.UserID, WorkspaceID: i.WorkspaceID})
	if err != nil {
		return p, ErrForbidden
	}
	p.Role = role
	return p, nil
}
