package ext

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// LoginState contains core-validated OAuth state, PKCE verifier and OIDC nonce.
// This is public semver API; providers must preserve state in their login URL.
type LoginState struct{ State, Verifier, Nonce string }

// Identity is a provider-validated identity. EmailVerified must reflect the provider's trust policy.
type Identity struct {
	Subject, Email, Name string
	EmailVerified        bool
}

// ProviderScope restricts a provider to the instance or a single workspace.
type ProviderScope struct{ WorkspaceID string }

// Global registers an instance-wide provider.
var Global = ProviderScope{}

// Workspace registers a provider for one workspace.
func Workspace(id string) ProviderScope { return ProviderScope{WorkspaceID: id} }

// Session describes a signed-in user and their login provider; API keys bypass session policies.
type Session struct{ UserID, Provider string }

// AuthProvider is the public semver contract for verified login providers.
type AuthProvider interface {
	ID() string
	LoginURL(context.Context, LoginState) (string, error)
	HandleCallback(context.Context, *http.Request) (Identity, error)
}

// AuthRegistry registers providers and optional discovery/session enforcement policies.
// Register replaces a provider with the same ID. Policies are evaluated in registration order.
type AuthRegistry interface {
	Register(AuthProvider, ProviderScope)
	SetDiscovery(func(context.Context, string) (string, bool))
	AddSessionPolicy(func(context.Context, Session, string) error)
}

// Action names an authorization operation. New action names are additive semver API.
type Action string

// The built-in authorization actions are shared by REST and MCP.
const (
	JobsRead             Action = "jobs:read"
	JobsWrite            Action = "jobs:write"
	JobsTrigger          Action = "jobs:trigger"
	RunsRead             Action = "runs:read"
	RunsRetry            Action = "runs:retry"
	SecretsWrite         Action = "secrets:write"
	AlertsWrite          Action = "alerts:write"
	KeysManage           Action = "keys:manage"
	MembersManage        Action = "members:manage"
	WorkspaceAdminAction Action = "workspace:admin"
	SSOManage            Action = "sso:manage"
	AuditRead            Action = "audit:read"
)

// Principal is the authenticated actor, including its workspace role and key scopes.
type Principal struct {
	UserID, WorkspaceID, KeyID, Role string
	Scopes                           []string
}

// Resource describes an operation target. OwnerID supports management of one's own keys.
// Collection operations leave ID empty; Attributes allows additive contextual constraints.
type Resource struct {
	WorkspaceID, Kind, ID, OwnerID string
	Attributes                     map[string]string
}

// Authorizer is the public semver authorization decision hook; nil means allowed.
type Authorizer interface {
	Authorize(context.Context, Principal, Action, Resource) error
}

// AuthzRegistry replaces the active authorizer; the last Set wins.
type AuthzRegistry interface{ Set(Authorizer) }

// Feature is an optional extension capability, independent of authentication.
type Feature string

// Built-in optional feature names are public semver API.
const (
	SAML          Feature = "saml"
	OIDCWorkspace Feature = "oidc_workspace"
	SCIM          Feature = "scim"
	RBAC          Feature = "rbac"
	AuditStream   Feature = "audit_stream"
	HA            Feature = "ha"
)

// Entitlements answers whether a workspace has a feature. OSS always returns false.
type Entitlements interface {
	Has(context.Context, string, Feature) bool
}

// DirectoryUser provisions an account by normalized email; ID optionally selects an existing account.
type DirectoryUser struct{ ID, Email, Name string }

// Directory provisions core users and memberships without exposing core write handles.
// DeactivateUser revokes sessions and credentials while retaining audit history.
type Directory interface {
	UpsertUser(context.Context, DirectoryUser) (string, error)
	SetMembership(context.Context, string, string, string) error
	RemoveMembership(context.Context, string, string) error
	DeactivateUser(context.Context, string) error
}

// AuditEvent is a committed audit record. Details contains the JSON audit payload.
type AuditEvent struct {
	ID, WorkspaceID, Actor, Action, Target string
	Details                                json.RawMessage
	CreatedAt                              time.Time
}

// AuditRegistry subscribes to committed events on a bounded asynchronous queue.
// Subscribers must return promptly and honor cancellation; overflow may drop events.
type AuditRegistry interface {
	Subscribe(func(context.Context, AuditEvent))
}

// Event is a committed run or job lifecycle event, such as run.failed, run.succeeded,
// run.timeout, job.auto_disabled or system.degraded. Type names are additive semver API.
// WorkspaceID is empty for instance-wide system events.
type Event struct {
	Type, WorkspaceID, JobID, RunID string
	Attempt                         int
	Error                           string
	At                              time.Time
}

// EventRegistry subscribes to lifecycle events on a bounded asynchronous queue.
// Subscribers must return promptly and honor cancellation; overflow may drop events,
// so counts that must be exact belong in Deps.Stats or an extension's own tables.
type EventRegistry interface {
	Subscribe(func(context.Context, Event))
}

// WorkspaceAdmin writes audited plan and suspension changes through core services.
// SetPlan invalidates cached limits after a successful update.
type WorkspaceAdmin interface {
	SetPlan(context.Context, string, string) error
	Suspend(context.Context, string, string) error
	Unsuspend(context.Context, string) error
}

// DECISION: StoreProvider is reserved as a design name for future alternative-storage
// support. No public interface is defined before that design exists.

type loginStateContextKey struct{}

// WithLoginState attaches core-validated callback state to a provider context.
// Providers normally read this through LoginStateFromContext, rather than setting it.
func WithLoginState(ctx context.Context, state LoginState) context.Context {
	return context.WithValue(ctx, loginStateContextKey{}, state)
}

// LoginStateFromContext exposes the validated callback's PKCE verifier and nonce
// so external providers can exchange and verify tokens without internal imports.
func LoginStateFromContext(ctx context.Context) (LoginState, bool) {
	s, ok := ctx.Value(loginStateContextKey{}).(LoginState)
	return s, ok
}
