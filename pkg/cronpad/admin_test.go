package cronpad

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/auth"
	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/store"
)

func TestOfflineCreateKeyByEmail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRONPAD_DATABASE__PATH", filepath.Join(dir, "control.db"))
	t.Setenv("CRONPAD_DATABASE__HISTORY_PATH", filepath.Join(dir, "history.db"))
	ctx := context.Background()
	c, e := config.Load(ctx, "", nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	dbs, e := store.Open(ctx, c.Database.Path, c.Database.HistoryPath, c.Database.BusyTimeout.Duration(), true)
	if e != nil {
		t.Fatal(e)
	}
	s := store.New(dbs)
	service := auth.New(ctx, s, clockwork.NewRealClock(), c, nil)
	if _, e = service.Account(ctx, "fake", auth.Profile{Subject: "alice", Email: "alice@example.com", Name: "Alice", Verified: true}); e != nil {
		t.Fatal(e)
	}
	if e = dbs.Close(); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		email string
		json  bool
		ok    bool
	}{{"alice@example.com", false, true}, {"alice@example.com", true, true}, {"missing@example.com", false, false}} {
		cmd := command()
		args := []string{"user", "create-key", "--email", tc.email, "--name", "automation"}
		if tc.json {
			args = append(args, "--json")
		}
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		e := cmd.ExecuteContext(ctx)
		if (e == nil) != tc.ok {
			t.Fatalf("%s: %v", tc.email, e)
		}
		if !tc.ok {
			continue
		}
		raw := strings.TrimSpace(out.String())
		if tc.json {
			var obj map[string]string
			if e = json.Unmarshal(out.Bytes(), &obj); e != nil {
				t.Fatal(e)
			}
			raw = obj["key"]
		}
		if len(raw) != 44 || !strings.HasPrefix(raw, "cp_") {
			t.Fatal("invalid key output", len(raw))
		}
	}
}
func TestServeRefusesMissingAuthentication(t *testing.T) {
	t.Setenv("CRONPAD_AUTH__DEV_LOGIN", "false")
	t.Setenv("CRONPAD_AUTH__GOOGLE__CLIENT_ID", "")
	t.Setenv("CRONPAD_AUTH__GITHUB__CLIENT_ID", "")
	cmd := command()
	cmd.SetArgs([]string{"serve"})
	if e := cmd.ExecuteContext(context.Background()); e == nil || !strings.Contains(e.Error(), "configure an OAuth provider") {
		t.Fatal(e)
	}
}
