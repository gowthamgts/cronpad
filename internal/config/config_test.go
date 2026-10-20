package config

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/spf13/pflag"
)

func TestPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[queue]\npath='toml.db'\n[server]\naddr=':3456'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(context.Background(), "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Queue.Path != "" {
		t.Fatal(cfg.Queue.Path)
	}
	cfg, err = Load(context.Background(), path, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Queue.Path != "toml.db" || cfg.Server.Addr != ":3456" {
		t.Fatal(cfg)
	}
	t.Setenv("CRONPAD_QUEUE__PATH", "env.db")
	t.Setenv("CRONPAD_SERVER__ADDR", ":9999")
	t.Setenv("CRONPAD_AUTH__ALLOWED_EMAIL_DOMAINS", "example.com,example.org")
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("server.addr", ":1111", "")
	cfg, err = Load(context.Background(), path, flags, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Queue.Path != "env.db" || cfg.Server.Addr != ":9999" || len(cfg.Auth.AllowedEmailDomains) != 2 {
		t.Fatal(cfg)
	}
	if err = flags.Set("server.addr", ":7777"); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(context.Background(), path, flags, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":7777" {
		t.Fatal(cfg.Server.Addr)
	}
}
func TestHostedExplicitOverride(t *testing.T) {
	t.Setenv("CRONPAD_MODE", "hosted")
	c, err := Load(context.Background(), "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sinks.Command.Enabled || c.Sinks.Webhook.AllowPrivateNetworks {
		t.Fatal("unsafe defaults")
	}
	t.Setenv("CRONPAD_SINKS__COMMAND__ENABLED", "true")
	t.Setenv("CRONPAD_SINKS__WEBHOOK__ALLOW_PRIVATE_NETWORKS", "true")
	c, err = Load(context.Background(), "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Sinks.Command.Enabled || !c.Sinks.Webhook.AllowPrivateNetworks {
		t.Fatal("explicit override lost")
	}
}
func TestDuration(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want time.Duration
	}{{"30d", 720 * time.Hour}, {"1d12h", 36 * time.Hour}, {"0", 0}, {"500ms", 500 * time.Millisecond}, {"1.5d", 36 * time.Hour}} {
		var d Duration
		if err := d.UnmarshalText([]byte(tc.s)); err != nil {
			t.Fatal(err)
		}
		if d.Duration() != tc.want {
			t.Fatalf("%s = %v", tc.s, d)
		}
	}
	for _, s := range []string{"bad", "999999999999d"} {
		var d Duration
		if err := d.UnmarshalText([]byte(s)); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestValidationAllErrors(t *testing.T) {
	c, err := Load(context.Background(), "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Mode = "bad"
	c.Workers.Count = -1
	c.Server.Addr = "bad"
	c.Logging.Format = "bad"
	err = c.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, s := range []string{"mode", "workers.count", "server.addr", "logging.format"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("missing %s: %v", s, err)
		}
	}
}
func TestSharedCookieDomainOnlyWhenHosted(t *testing.T) {
	for _, tc := range []struct{ mode, publicURL, want string }{
		{"hosted", "https://dash.cronpad.com", "cronpad.com"},
		{"hosted", "https://dash.eu.cronpad.com", "eu.cronpad.com"},
		{"hosted", "https://cronpad.com", ""},
		{"hosted", "http://127.0.0.1:8080", ""},
		{"hosted", "http://localhost:8080", ""},
		{"hosted", "", ""},
		{"selfhosted", "https://cron.example.com", ""},
	} {
		c := Config{Mode: tc.mode, Server: ConfigServer{PublicURL: tc.publicURL}}
		if got := c.SharedCookieDomain(); got != tc.want {
			t.Fatalf("%s %s: got %q, want %q", tc.mode, tc.publicURL, got, tc.want)
		}
	}
}
func TestExampleMatchesDefaults(t *testing.T) {
	b, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(defaults) {
		t.Fatal("example/default drift")
	}
	if _, err = toml.Parser().Unmarshal(b); err != nil {
		t.Fatal(err)
	}
	c, err := Load(context.Background(), "../../config.example.toml", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := Load(context.Background(), "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, base) {
		t.Fatal("defaults differ from example")
	}
}

func TestExtensionCoreSectionConflictBeforeLoading(t *testing.T) {
	for _, section := range []string{"server", "mode", "telemetry"} {
		t.Run(section, func(t *testing.T) {
			for _, content := range []string{"", "[" + section + "]\nvalue = true\n", "[invalid TOML"} {
				path := filepath.Join(t.TempDir(), "config.toml")
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				_, err := Load(context.Background(), path, nil, map[string]any{section: &struct{}{}}, nil)
				if err == nil || !strings.Contains(err.Error(), "conflicts with core") {
					t.Fatalf("expected conflict for %q before loading %q, got %v", section, content, err)
				}
			}
		})
	}
}

func TestBuildMetadataDoesNotPolluteConfig(t *testing.T) {
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CRONPAD_") {
			t.Setenv(key, "")
			_ = os.Unsetenv(key)
		}
	}
	for _, key := range []string{"BUILD_VERSION", "BUILD_COMMIT", "BUILD_DATE"} {
		t.Setenv(key, "test")
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := Load(context.Background(), "", nil, nil, logger); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatal(logs.String())
	}
	t.Setenv("CRONPAD_UNKNOWN_SECTION__VALUE", "yes")
	if _, err := Load(context.Background(), "", nil, nil, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "unknown_section") {
		t.Fatal("unknown section was silently ignored")
	}
}

func TestResultDeliveryValidation(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		valid      bool
	}{
		{"RESULTS_FLUSH_INTERVAL", "0s", true}, {"RESULTS_FLUSH_INTERVAL", "1ms", true}, {"RESULTS_FLUSH_INTERVAL", "1s", true},
		{"RESULTS_FLUSH_INTERVAL", "-1ms", false},
		{"RESULTS_FLUSH_INTERVAL", "999us", false}, {"RESULTS_FLUSH_INTERVAL", "1001ms", false},
		{"RESULTS_MAX_BATCH", "0", false}, {"RESULTS_MAX_BATCH", "10001", false}, {"RESULTS_MAX_BATCH", "1", true},
		{"DELIVERY_MODE", "fast", true}, {"DELIVERY_MODE", "durable", true}, {"DELIVERY_MODE", "unsafe", false},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__"+tc.key, tc.value)
			_, err := Load(context.Background(), "", nil, nil, nil)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestDeprecatedDailyQuotaConvertsAndWarns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.toml")
	if err := os.WriteFile(path, []byte("[limits]\nmax_runs_per_day_per_workspace=7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	c, err := Load(context.Background(), path, nil, nil, logger)
	if err != nil || c.Limits.MaxRunsPerMonthPerWorkspace != 210 || !strings.Contains(output.String(), "deprecated") {
		t.Fatal(c.Limits, err, output.String())
	}
	if err := os.WriteFile(path, []byte("[limits]\nmax_runs_per_day_per_workspace=7\nmax_runs_per_month_per_workspace=11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(context.Background(), path, nil, nil, logger)
	if err != nil || c.Limits.MaxRunsPerMonthPerWorkspace != 11 {
		t.Fatal(c.Limits, err)
	}
}

func TestCodeDefaultsValidatedAtStartup(t *testing.T) {
	load := func(body string) error {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(context.Background(), path, nil, nil, nil)
		return err
	}
	cfg, err := Load(context.Background(), "", nil, nil, nil)
	if err != nil || len(cfg.Sinks.Webhook.RetryOn) != 5 || cfg.Sinks.Command.SuccessExitCodes[0] != 0 {
		t.Fatalf("defaults: %v %+v", err, cfg.Sinks)
	}
	if err := load("[sinks.webhook]\nsuccess_codes=['2xx','301']\nretry_on=['5xx','network']\n[sinks.command]\nretry_exit_codes=[75]\n"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"[sinks.webhook]\nsuccess_codes=['6xx']\n",
		"[sinks.webhook]\nsuccess_codes=[]\n",
		"[sinks.webhook]\nretry_on=['network','2xx']\n",
		"[sinks.webhook]\nretry_on=['later']\n",
		"[sinks.command]\nsuccess_exit_codes=[256]\n",
		"[sinks.command]\nretry_exit_codes=[-1]\n",
	} {
		if err := load(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestQueuePathValidation(t *testing.T) {
	cfg, err := Load(context.Background(), "", nil, nil, nil)
	if err != nil || cfg.QueuePath() != filepath.Join("data", "cronpad-queue.db") {
		t.Fatal("unexpected default", cfg.QueuePath(), err)
	}
	t.Setenv("CRONPAD_QUEUE__PATH", "./data/cronpad.db")
	if _, err = Load(context.Background(), "", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "queue.path") {
		t.Fatalf("want queue.path error, got %v", err)
	}
}

func TestRedisSectionWarnsItIsIgnored(t *testing.T) {
	for _, set := range []bool{false, true} {
		if set {
			t.Setenv("CRONPAD_REDIS__ADDR", "redis:6379")
		}
		var buf bytes.Buffer
		if _, err := Load(context.Background(), "", nil, nil, slog.New(slog.NewTextHandler(&buf, nil))); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(buf.String(), "no longer uses Redis"); got != set {
			t.Fatalf("set=%v warned=%v: %s", set, got, buf.String())
		}
	}
}

func TestQueuePathFollowsDatabasePath(t *testing.T) {
	t.Setenv("CRONPAD_DATABASE__PATH", "/var/lib/cronpad/control.db")
	cfg, err := Load(context.Background(), "", nil, nil, nil)
	if err != nil || cfg.QueuePath() != "/var/lib/cronpad/control-queue.db" {
		t.Fatal(cfg.QueuePath(), err)
	}
	t.Setenv("CRONPAD_QUEUE__PATH", "/tmp/q.db")
	cfg, err = Load(context.Background(), "", nil, nil, nil)
	if err != nil || cfg.QueuePath() != "/tmp/q.db" {
		t.Fatal(cfg.QueuePath(), err)
	}
}
