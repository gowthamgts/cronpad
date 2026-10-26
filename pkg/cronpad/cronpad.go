// Package cronpad is the entry point for the standard binary and extension-based builds.
package cronpad

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/gowthamgts/cronpad/internal/app"
	"github.com/gowthamgts/cronpad/internal/clock"
	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/containerentrypoint"
	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/internal/telemetry"
	"github.com/gowthamgts/cronpad/internal/version"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

type options struct {
	edition    string
	extensions []ext.Extension
	dashboard  fs.FS
}

// Option configures a binary before its CLI is executed.
type Option func(*options)

// WithExtension installs extension-owned routes, migrations and background work.
func WithExtension(e ext.Extension) Option {
	return func(o *options) { o.extensions = append(o.extensions, e) }
}

// WithDashboard serves a different dashboard build, rooted at its index.html, instead of the
// embedded one. It must keep the embedded build's asset layout and CSP-safe markup.
func WithDashboard(dist fs.FS) Option { return func(o *options) { o.dashboard = dist } }

// WithEdition sets the edition reported in version metadata (default oss).
func WithEdition(name string) Option { return func(o *options) { o.edition = name } }

// Main executes the CLI with a signal-owned context and exits nonzero on failure.
func Main(opts ...Option) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := command(opts...).ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func command(opts ...Option) *cobra.Command {
	o := options{edition: "oss"}
	for _, opt := range opts {
		opt(&o)
	}
	root := &cobra.Command{Use: "cronpad", SilenceUsage: true, SilenceErrors: true}
	var path string
	root.PersistentFlags().StringVar(&path, "config", "./cronpad.toml", "optional TOML configuration")
	serve := &cobra.Command{Use: "serve", Short: "Run the cronpad HTTP server", RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		sections, err := app.Sections(o.extensions)
		if err != nil {
			return err
		}
		bootstrap := logx.Setup(logx.Config{Level: "info", Format: "json", Service: "cronpad", Version: version.Version}, os.Stdout, nil)
		cfg, err := config.Load(ctx, path, cmd.Flags(), sections, bootstrap)
		if err != nil {
			return err
		}
		if !cfg.Auth.DevLogin && cfg.Auth.Google.ClientID == "" && cfg.Auth.GitHub.ClientID == "" && cfg.Auth.OIDC.Issuer == "" {
			return fmt.Errorf("configure an OAuth provider or explicitly enable auth.dev_login for development")
		}
		if !cfg.Auth.DevLogin && cfg.Secrets.EncryptionKey == "" {
			return fmt.Errorf("secrets.encryption_key is required to encrypt webhook signing secrets")
		}
		telemetryRuntime, err := telemetry.Bootstrap(ctx, cfg.Telemetry)
		if err != nil {
			return err
		}
		defer func() {
			shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.Server.ShutdownGrace.Duration())
			defer cancel()
			_ = telemetryRuntime.Shutdown(shutdown)
		}()
		log := logx.Setup(logx.Config{Level: cfg.Logging.Level, Format: cfg.Logging.Format, AddSource: cfg.Logging.AddSource, Service: cfg.Telemetry.ServiceName, Version: version.Version}, os.Stdout, telemetryRuntime.Logs)
		a, err := app.New(ctx, cfg, o.edition, o.extensions, o.dashboard, clock.New(), log)
		if err != nil {
			return err
		}
		defer func() { _ = a.Close() }()
		return a.Run(ctx, telemetryRuntime.Metrics)
	}}
	// DECISION: dotted flags mirror TOML keys; only changed flags override env.
	serve.Flags().String("server.addr", ":8080", "HTTP listen address")
	serve.Flags().String("server.public_url", "", "public origin")
	serve.Flags().String("mode", "selfhosted", "selfhosted or hosted")
	serve.Flags().String("logging.format", "json", "json or logfmt")
	serve.Flags().String("logging.level", "info", "logging threshold")
	root.AddCommand(serve, &cobra.Command{Use: "version", Short: "Print build and edition", RunE: func(cmd *cobra.Command, _ []string) error {
		v := version.Get(o.edition)
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "cronpad %s commit=%s build_date=%s edition=%s\n", v.Version, v.Commit, v.BuildDate, v.Edition)
		return err
	}})
	health := &cobra.Command{Use: "healthcheck", Short: "Probe the local HTTP server", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(cmd.Context(), path, nil, nil, nil)
		if err != nil {
			return err
		}
		addr := cfg.Server.Addr
		if len(addr) > 0 && addr[0] == ':' {
			addr = "127.0.0.1" + addr
		}
		req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
		if err != nil {
			return fmt.Errorf("health request: %w", err)
		}
		client := &http.Client{Timeout: cfg.Server.ReadTimeout.Duration()}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("healthcheck: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("healthcheck returned %s", resp.Status)
		}
		return nil
	}}
	root.AddCommand(&cobra.Command{Use: "entrypoint", Hidden: true, DisableFlagParsing: true, RunE: func(cmd *cobra.Command, args []string) error {
		log, logging := entrypointLogger(cmd.Context(), args)
		return containerentrypoint.Run(cmd.Context(), clock.New(), log, logging, args)
	}})
	root.AddCommand(health, scheduleCommand(clock.New()), jobsCommand(), mcpCommand())
	root.AddCommand(adminCommands(&path)...)
	return root
}

// entrypointLogger logs like `serve` will: same format, level and service fields, read
// from the same config file, environment and serve flags. The entrypoint and Litestream
// share the container's output with cronpad, so mixed formats would break log parsing.
func entrypointLogger(ctx context.Context, args []string) (*slog.Logger, containerentrypoint.Logging) {
	flags := pflag.NewFlagSet("entrypoint", pflag.ContinueOnError)
	flags.ParseErrorsAllowlist.UnknownFlags = true
	flags.SetOutput(io.Discard)
	path := flags.String("config", "./cronpad.toml", "")
	flags.String("logging.format", "json", "")
	flags.String("logging.level", "info", "")
	_ = flags.Parse(args)
	logging := containerentrypoint.Logging{Format: "json", Level: "info"}
	service := "cronpad"
	// DECISION: an invalid configuration is reported by `serve` itself; here it only
	// falls back to the defaults for the few lines logged before cronpad starts.
	if cfg, err := config.Load(ctx, *path, flags, nil, nil); err == nil {
		logging = containerentrypoint.Logging{Format: cfg.Logging.Format, Level: cfg.Logging.Level}
		service = cfg.Telemetry.ServiceName
	}
	log := logx.Setup(logx.Config{Level: logging.Level, Format: logging.Format, Service: service, Version: version.Version}, os.Stdout, nil)
	return log, logging
}
