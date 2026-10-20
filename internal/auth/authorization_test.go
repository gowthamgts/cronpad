package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/gowthamgts/cronpad/pkg/ext"
)

func TestDefaultAuthorizerEquivalence(t *testing.T) {
	actions := []ext.Action{ext.JobsRead, ext.JobsWrite, ext.JobsTrigger, ext.RunsRead, ext.RunsRetry, ext.SecretsWrite, ext.AlertsWrite, ext.KeysManage, ext.MembersManage, ext.WorkspaceAdminAction, ext.SSOManage, ext.AuditRead}
	for _, role := range []string{"owner", "member"} {
		for _, a := range actions {
			for _, own := range []bool{true, false} {
				t.Run(role+"/"+string(a)+map[bool]string{true: "/own", false: "/other"}[own], func(t *testing.T) {
					owner := "other"
					if own {
						owner = "user"
					}
					p := ext.Principal{UserID: "user", WorkspaceID: "workspace", Role: role}
					resource := ext.Resource{WorkspaceID: "workspace", OwnerID: owner}
					want := role == "owner" || (a != ext.MembersManage && a != ext.WorkspaceAdminAction && (a != ext.KeysManage || own))
					err := (DefaultAuthorizer{}).Authorize(context.Background(), p, a, resource)
					if (err == nil) != want {
						t.Fatalf("allowed=%v want=%v", err == nil, want)
					}
				})
			}
		}
	}
}

type authorizerFunc func(context.Context, ext.Principal, ext.Action, ext.Resource) error

func (f authorizerFunc) Authorize(ctx context.Context, p ext.Principal, a ext.Action, r ext.Resource) error {
	return f(ctx, p, a, r)
}
func TestAuthorizationLastWinsAndScopeIntersection(t *testing.T) {
	ctx := context.Background()
	registry := &Authorization{}
	allow := authorizerFunc(func(context.Context, ext.Principal, ext.Action, ext.Resource) error { return nil })
	deny := authorizerFunc(func(context.Context, ext.Principal, ext.Action, ext.Resource) error { return ErrForbidden })
	p := ext.Principal{KeyID: "key", Scopes: []string{"jobs:write"}}
	registry.Set(allow)
	for _, a := range []ext.Action{ext.JobsWrite, ext.JobsTrigger, ext.RunsRetry} {
		if err := registry.Authorize(ctx, p, a, ext.Resource{Kind: map[ext.Action]string{ext.JobsWrite: "jobs", ext.JobsTrigger: "jobs", ext.RunsRetry: "runs"}[a]}); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(registry.Authorize(ctx, p, ext.MembersManage, ext.Resource{}), ErrForbidden) {
		t.Fatal("custom authorizer expanded key")
	}
	registry.Set(deny)
	p.Scopes = []string{"*"}
	if !errors.Is(registry.Authorize(ctx, p, ext.JobsWrite, ext.Resource{}), ErrForbidden) {
		t.Fatal("last registration did not win")
	}
}
