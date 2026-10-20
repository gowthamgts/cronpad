package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOIDCConfigDefaultsAndOverrides(t *testing.T) {
	c, err := Load(context.Background(), "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Auth.OIDC.DisplayName != "SSO" || !c.Auth.OIDC.RequireEmailVerified || len(c.Auth.OIDC.Scopes) != 3 {
		t.Fatalf("defaults: %+v", c.Auth.OIDC)
	}
	path := filepath.Join(t.TempDir(), "oidc.toml")
	if err = os.WriteFile(path, []byte("[auth.oidc]\nissuer='https://issuer.example.com'\nclient_id='client'\nrequire_email_verified=false\ndisplay_name='Company Login'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRONPAD_AUTH__OIDC__CLIENT_SECRET", "from-env")
	c, err = Load(context.Background(), path, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Auth.OIDC.ClientSecret != "from-env" || c.Auth.OIDC.RequireEmailVerified || c.Auth.OIDC.DisplayName != "Company Login" {
		t.Fatalf("overrides: %+v", c.Auth.OIDC)
	}
	c.Auth.OIDC.ClientID = ""
	if err = c.Validate(); err == nil {
		t.Fatal("missing client ID accepted")
	}
	c.Auth.OIDC.ClientID = "client"
	c.Auth.OIDC.Scopes = []string{"email"}
	if err = c.Validate(); err == nil {
		t.Fatal("scopes without openid accepted")
	}
}
