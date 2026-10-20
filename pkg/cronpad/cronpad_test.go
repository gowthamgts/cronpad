package cronpad

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionEdition(t *testing.T) {
	var b bytes.Buffer
	cmd := command(WithEdition("cloud"))
	cmd.SetOut(&b)
	cmd.SetArgs([]string{"version"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "edition=cloud") {
		t.Fatal(b.String())
	}
}

func TestScheduleCommandsOffline(t *testing.T) {
	t.Setenv("CRONPAD_MODE", "invalid configuration must not be loaded")
	for _, args := range [][]string{{"schedule", "validate", "2nd tuesday of every month at 9am", "--json", "--tz", "Asia/Kolkata"}, {"schedule", "next", "2nd tuesday of every month at 9am", "-n", "3", "--tz", "Asia/Kolkata", "--after", "2026-10-16T00:00:00Z", "--json"}} {
		var out bytes.Buffer
		cmd := command()
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if args[1] == "next" && !strings.Contains(out.String(), "2026-11-10T03:30:00Z") {
			t.Fatal(out.String())
		}
		if args[1] == "validate" && !strings.Contains(out.String(), `"valid":true`) {
			t.Fatal(out.String())
		}
	}
}

func TestEntrypointLoggerFollowsServeConfig(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "cronpad.toml")
	if err := os.WriteFile(file, []byte("[logging]\nformat = \"logfmt\"\nlevel = \"debug\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("[logging]\nformat = \"xml\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		env           map[string]string
		args          []string
		format, level string
	}{
		{"defaults", nil, []string{"serve", "--config", filepath.Join(dir, "missing.toml")}, "json", "info"},
		{"environment", map[string]string{"CRONPAD_LOGGING__FORMAT": "logfmt"}, []string{"serve", "--config", filepath.Join(dir, "missing.toml")}, "logfmt", "info"},
		{"config file", nil, []string{"serve", "--config=" + file}, "logfmt", "debug"},
		{"serve flags win", nil, []string{"serve", "--config", file, "--logging.format", "json", "--server.addr", ":9"}, "json", "debug"},
		{"invalid config falls back", nil, []string{"serve", "--config", bad}, "json", "info"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, got := entrypointLogger(ctx, tc.args)
			if got.Format != tc.format || got.Level != tc.level {
				t.Fatalf("got %+v, want %s/%s", got, tc.format, tc.level)
			}
		})
	}
}
