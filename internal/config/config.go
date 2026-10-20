package config

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/gowthamgts/cronpad/internal/logx"
)

//go:embed defaults.toml
var defaults []byte

// Load merges defaults, optional TOML, environment, and explicitly changed flags.
// Sections contains extension-owned typed config pointers. Unknown sections warn.
func Load(ctx context.Context, path string, flags *pflag.FlagSet, sections map[string]any, log *slog.Logger) (Config, error) {
	k := koanf.New(".")
	parser := toml.Parser()
	m, err := parser.Unmarshal(defaults)
	if err != nil {
		return Config{}, fmt.Errorf("default config: %w", err)
	}
	known := map[string]bool{}
	for key := range m {
		known[key] = true
	}
	for key := range sections {
		if known[key] {
			return Config{}, fmt.Errorf("extension config section %q conflicts with core", key)
		}
	}
	if err = k.Load(confmap.Provider(m, "."), nil); err != nil {
		return Config{}, fmt.Errorf("load defaults: %w", err)
	}
	explicit := koanf.New(".")
	if path != "" {
		if err = explicit.Load(file.Provider(path), parser); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("load %s: %w", path, err)
		}
	}
	if err = explicit.Load(env.ProviderWithValue("CRONPAD_", ".", func(key, value string) (string, any) {
		key = strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(key, "CRONPAD_")), "__", ".")
		if v := k.Get(key); v != nil && reflect.TypeOf(v).Kind() == reflect.Slice {
			if value == "" {
				return key, []string{}
			}
			return key, strings.Split(value, ",")
		}
		return key, value
	}), nil); err != nil {
		return Config{}, fmt.Errorf("load environment: %w", err)
	}
	if flags != nil {
		if err = explicit.Load(posflag.ProviderWithFlag(flags, ".", nil, func(f *pflag.Flag) (string, any) {
			if f.Name == "config" || f.Name == "help" {
				return "", nil
			}
			return f.Name, posflag.FlagVal(flags, f)
		}), nil); err != nil {
			return Config{}, fmt.Errorf("load flags: %w", err)
		}
	}
	if err = k.Merge(explicit); err != nil {
		return Config{}, fmt.Errorf("merge config: %w", err)
	}
	if k.String("mode") == "hosted" {
		defaults := map[string]any{"sinks.webhook.allow_private_networks": false, "sinks.command.enabled": false, "auth.allow_signup": true, "limits.max_jobs_per_workspace": 100, "limits.min_interval": "1m", "limits.max_runs_per_month_per_workspace": 50000, "limits.max_concurrent_runs_per_workspace": 5, "limits.max_alert_emails_per_day": 100, "limits.max_api_keys_per_workspace": 20, "limits.max_retry_attempts": 10, "limits.api_rate_limit_per_minute": 600, "sinks.webhook.max_timeout": "120s"}
		for key, value := range defaults {
			if !explicit.Exists(key) {
				_ = k.Set(key, value)
			}
		}
	}
	if explicit.Exists("limits.max_runs_per_day_per_workspace") {
		if !explicit.Exists("limits.max_runs_per_month_per_workspace") {
			_ = k.Set("limits.max_runs_per_month_per_workspace", k.Int64("limits.max_runs_per_day_per_workspace")*30)
		}
		if log != nil {
			log.WarnContext(ctx, "limits.max_runs_per_day_per_workspace is deprecated; use max_runs_per_month_per_workspace (daily ×30)")
		}
	}
	var cfg Config
	uc := koanf.UnmarshalConf{Tag: "koanf", DecoderConfig: &mapstructure.DecoderConfig{WeaklyTypedInput: true, DecodeHook: mapstructure.ComposeDecodeHookFunc(mapstructure.TextUnmarshallerHookFunc(), mapstructure.StringToSliceHookFunc(","))}}
	if err = k.UnmarshalWithConf("", &cfg, uc); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	keys := k.Raw()
	for key := range keys {
		if known[key] {
			continue
		}
		if target, ok := sections[key]; ok {
			if err = k.UnmarshalWithConf(key, target, uc); err != nil {
				return cfg, fmt.Errorf("decode extension %s: %w", key, err)
			}
		} else if key == "redis" && log != nil {
			// DECISION: Redis support was removed; say so instead of a generic warning.
			log.WarnContext(ctx, "the [redis] section is ignored: cronpad no longer uses Redis, the queue is a SQLite file (see queue.path)")
		} else if log != nil {
			log.WarnContext(ctx, "unknown config section", logx.Section, key)
		}
	}
	if err = cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// SharedCookieDomain is the parent domain the hosted dashboard shares with the public site: server.public_url
// without its first label (https://dash.cronpad.com -> cronpad.com). The theme cookie and the signed-in hint
// cookie live there so the site can match the dashboard. Self-hosted servers have no such site, so their
// cookies stay on the dashboard host ("").
func (c Config) SharedCookieDomain() string {
	if c.Mode != "hosted" {
		return ""
	}
	u, err := url.Parse(c.Server.PublicURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	labels := strings.Split(host, ".")
	if net.ParseIP(host) != nil || len(labels) < 3 {
		return ""
	}
	return strings.Join(labels[1:], ".")
}

// QueuePath is the SQLite queue file: queue.path, or <name>-queue.db beside
// database.path. Deriving it from the control DB's name keeps two instances
// that share a directory (e.g. `just dev` and `just dev-hosted`) apart.
func (c Config) QueuePath() string {
	if c.Queue.Path != "" {
		return c.Queue.Path
	}
	stem := strings.TrimSuffix(filepath.Base(c.Database.Path), filepath.Ext(c.Database.Path))
	return filepath.Join(filepath.Dir(c.Database.Path), stem+"-queue.db")
}

// Validate returns all field and cross-field errors in stable, readable order.
func (c Config) Validate() error {
	v := validator.New()
	v.RegisterTagNameFunc(func(f reflect.StructField) string { return f.Tag.Get("koanf") })
	var problems []string
	if err := v.Struct(c); err != nil {
		var fields validator.ValidationErrors
		if errors.As(err, &fields) {
			for _, f := range fields {
				problems = append(problems, strings.TrimPrefix(f.Namespace(), "Config.")+": must satisfy "+f.Tag())
			}
		} else {
			problems = append(problems, err.Error())
		}
	}
	if _, port, err := net.SplitHostPort(c.Server.Addr); err != nil {
		problems = append(problems, "server.addr: must be host:port")
	} else if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		problems = append(problems, "server.addr: port must be between 0 and 65535")
	}
	for _, cidr := range c.Server.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			problems = append(problems, "server.trusted_proxies: invalid CIDR "+cidr)
		}
	}
	controlPath, _ := filepath.Abs(c.Database.Path)
	historyPath, _ := filepath.Abs(c.Database.HistoryPath)
	if controlPath == historyPath {
		problems = append(problems, "database.history_path: must differ from database.path")
	}
	if queuePath, _ := filepath.Abs(c.QueuePath()); queuePath == controlPath || queuePath == historyPath {
		problems = append(problems, "queue.path: must differ from database.path and database.history_path")
	}
	if !c.Database.BusyTimeout.ValidMilliseconds() {
		problems = append(problems, "database.busy_timeout: out of range")
	}
	if c.Sinks.Webhook.DefaultTimeout > c.Sinks.Webhook.MaxTimeout {
		problems = append(problems, "sinks.webhook.default_timeout: exceeds max_timeout")
	}
	problems = append(problems, c.codeDefaultProblems()...)
	if c.Retry.Defaults.InitialInterval > c.Retry.Defaults.MaxInterval {
		problems = append(problems, "retry.defaults.initial_interval: exceeds max_interval")
	}
	if key := c.Secrets.EncryptionKey; key != "" {
		decoded, err := base64.StdEncoding.DecodeString(key)
		if err != nil || len(decoded) != 32 {
			problems = append(problems, "secrets.encryption_key: must be 32 bytes encoded as base64")
		}
	}
	if c.Telemetry.OTLPEndpoint != "" {
		u, err := url.Parse(c.Telemetry.OTLPEndpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			problems = append(problems, "telemetry.otlp_endpoint: must be an http or https URL")
		}
	}
	if c.Auth.OIDC.Issuer != "" && c.Auth.OIDC.ClientID == "" {
		problems = append(problems, "auth.oidc.client_id: required when issuer is configured")
	}
	if c.Auth.OIDC.Issuer == "" && (c.Auth.OIDC.ClientID != "" || c.Auth.OIDC.ClientSecret != "") {
		problems = append(problems, "auth.oidc.issuer: required when OIDC credentials are configured")
	}
	if c.Auth.OIDC.Issuer != "" {
		openid := false
		for _, scope := range c.Auth.OIDC.Scopes {
			if scope == "openid" {
				openid = true
			}
		}
		if !openid {
			problems = append(problems, "auth.oidc.scopes: must include openid")
		}
	}
	if c.Alerts.Email.Enabled && c.Alerts.Email.SMTPHost == "" {
		problems = append(problems, "alerts.email.smtp_host: required when enabled")
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
}

// codeDefaultProblems checks the status and exit codes new jobs start with, so a typo stops startup
// instead of surfacing later as a job that can never be created.
func (c Config) codeDefaultProblems() []string {
	var problems []string
	status := func(token string) bool {
		if n, err := strconv.Atoi(token); err == nil {
			return n >= 100 && n <= 599
		}
		return len(token) == 3 && token[0] >= '1' && token[0] <= '5' && token[1:] == "xx"
	}
	w := c.Sinks.Webhook
	if len(w.SuccessCodes) == 0 {
		problems = append(problems, "sinks.webhook.success_codes: must list at least one code or class")
	}
	for _, t := range w.SuccessCodes {
		if !status(t) {
			problems = append(problems, fmt.Sprintf("sinks.webhook.success_codes: invalid entry %q (use 100-599 or 1xx-5xx)", t))
		}
	}
	for _, t := range w.RetryOn {
		if !status(t) && t != "network" && t != "timeout" || t == "1xx" || t == "2xx" || t == "3xx" {
			problems = append(problems, fmt.Sprintf("sinks.webhook.retry_on: invalid entry %q (use 100-599, 4xx, 5xx, network or timeout)", t))
		}
	}
	m := c.Sinks.Command
	if len(m.SuccessExitCodes) == 0 {
		problems = append(problems, "sinks.command.success_exit_codes: must list at least one exit code")
	}
	for _, list := range []struct {
		key   string
		codes []int
	}{{"success_exit_codes", m.SuccessExitCodes}, {"retry_exit_codes", m.RetryExitCodes}} {
		for _, n := range list.codes {
			if n < 0 || n > 255 {
				problems = append(problems, fmt.Sprintf("sinks.command.%s: invalid exit code %d (use 0-255)", list.key, n))
			}
		}
	}
	return problems
}
