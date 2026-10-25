// Package app wires the server and owns component lifecycles.
package app

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"

	contract "github.com/gowthamgts/cronpad/api"
	"github.com/gowthamgts/cronpad/internal/auth"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/outbound"
	"github.com/gowthamgts/cronpad/internal/secrets"

	"github.com/riandyrn/otelchi"
	"go.opentelemetry.io/otel"

	"github.com/gowthamgts/cronpad/internal/alert"
	"github.com/gowthamgts/cronpad/internal/dispatcher"
	"github.com/gowthamgts/cronpad/internal/events"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/sink/command"
	"github.com/gowthamgts/cronpad/internal/telemetry"
	"github.com/gowthamgts/cronpad/internal/worker"

	"github.com/gowthamgts/cronpad/internal/netguard"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/sink/webhook"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/sync/errgroup"

	"github.com/gowthamgts/cronpad/internal/api"
	"github.com/gowthamgts/cronpad/internal/clock"
	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/janitor"
	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/internal/mcpserver"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/version"
	"github.com/gowthamgts/cronpad/pkg/ext"
	"github.com/gowthamgts/cronpad/web"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Sections validates extension identities before loading their configuration.
func Sections(extensions []ext.Extension) (map[string]any, error) {
	sections := map[string]any{}
	names := map[string]bool{}
	for _, e := range extensions {
		if e == nil {
			return nil, errors.New("nil extension")
		}
		name := e.Name()
		if !identifier.MatchString(name) || names[name] {
			return nil, fmt.Errorf("invalid or duplicate extension name %q", name)
		}
		names[name] = true
		key, target := e.ConfigSection()
		if key == "" && target == nil {
			continue
		}
		if !identifier.MatchString(key) || target == nil || sections[key] != nil {
			return nil, fmt.Errorf("invalid or duplicate config section %q", key)
		}
		sections[key] = target
	}
	return sections, nil
}

type configView struct{ mode, url string }

func (v configView) Mode() string      { return v.mode }
func (v configView) PublicURL() string { return v.url }

type App struct {
	router      chi.Router
	Config      config.Config
	Handler     http.Handler
	DBs         *store.DBs
	deps        ext.Deps
	extensions  []ext.Extension
	janitor     *janitor.Janitor
	log         *slog.Logger
	queue       *queue.SQLite
	Dispatcher  *dispatcher.Dispatcher
	hook        *webhook.Webhook
	notifier    *alert.Notifier
	audit       *auth.AuditBus
	lifecycle   *events.Subscribers
	auditCancel context.CancelFunc
	auditDone   chan struct{}
}

// New opens both databases, migrates core and extensions, then mounts routes.
func New(ctx context.Context, c config.Config, edition string, extensions []ext.Extension, clk clock.Clock, log *slog.Logger) (*App, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if _, err := Sections(extensions); err != nil {
		return nil, err
	}
	dbs, err := store.Open(ctx, c.Database.Path, c.Database.HistoryPath, c.Database.BusyTimeout.Duration(), c.Database.RunMigrations)
	if err != nil {
		return nil, err
	}
	publicURL := c.Server.PublicURL
	if publicURL == "" {
		publicURL = "http://" + c.Server.Addr
		if strings.HasPrefix(c.Server.Addr, ":") {
			publicURL = "http://localhost" + c.Server.Addr
		}
	}
	d := ext.Deps{Config: configView{c.Mode, publicURL}, Clock: clk, Log: log, Control: dbs.Control.Writer, History: dbs.History.Writer}
	for _, e := range extensions {
		db, fsys := e.Migrations()
		if fsys == nil {
			continue
		}
		handle := d.Control
		if db == ext.History {
			handle = d.History
		} else if db != ext.Control {
			_ = dbs.Close()
			return nil, fmt.Errorf("extension %s: unknown DB %q", e.Name(), db)
		}
		if c.Database.RunMigrations {
			if err = store.Migrate(ctx, handle, fsys, "goose_"+e.Name()); err != nil {
				_ = dbs.Close()
				return nil, err
			}
		}
	}
	a := &App{Config: c, DBs: dbs, deps: d, extensions: extensions, log: log}
	r := chi.NewRouter()
	a.router = r
	// The client IP for rate limits: X-Forwarded-For is read only from server.trusted_proxies (right to left,
	// skipping trusted hops), so a client connecting directly cannot choose its address.
	clientIP := middleware.ClientIPFromRemoteAddr
	if len(c.Server.TrustedProxies) > 0 {
		clientIP = middleware.ClientIPFromXFF(c.Server.TrustedProxies...)
	}
	r.Use(clientIP, middleware.RequestID, requestLogging(log, clk), middleware.Recoverer)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Request-ID", middleware.GetReqID(r.Context()))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "same-origin")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'")
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			next.ServeHTTP(w, r)
		})
	})
	// DECISION: mount the public schedule utility before extension routes.
	api.Schedules(r, clk, c.Limits.MinInterval.Duration(), c.Limits.APIRateLimitPerMinute)
	r.Get("/api/v1/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(contract.OpenAPI)
	})
	q, err := openQueue(ctx, c, clk)
	if err != nil {
		_ = dbs.Close()
		return nil, err
	}
	a.queue = q
	repository, err := store.NewPrepared(ctx, dbs)
	if err != nil {
		_ = q.Close()
		_ = dbs.Close()
		return nil, err
	}
	scheduler := &api.QueueScheduler{Store: repository, Queue: q, Clock: clk, WakeCh: make(chan struct{}, 1), Log: log}
	hook := webhook.New(netguard.Policy{AllowPrivateNetworks: c.Sinks.Webhook.AllowPrivateNetworks, Hosted: c.Mode == "hosted"}, clk, strings.ReplaceAll(c.Sinks.Webhook.UserAgent, "{version}", version.Version), c.Sinks.Webhook.MaxResponseBytes, c.Sinks.Webhook.MaxTimeout.Duration(), webhook.WithConcurrency(c.Workers.Count))
	a.hook = hook
	var vault *secrets.Vault
	key := make([]byte, 32)
	if c.Secrets.EncryptionKey != "" {
		vault, err = secrets.New(c.Secrets.EncryptionKey, repository)
		if err == nil {
			err = vault.Migrate(ctx)
		}
		if err != nil {
			hook.Close()
			_ = q.Close()
			_ = dbs.Close()
			return nil, err
		}
		key, _ = base64.StdEncoding.DecodeString(c.Secrets.EncryptionKey)
	} else {
		if _, err = cryptorand.Read(key); err != nil {
			hook.Close()
			_ = q.Close()
			_ = dbs.Close()
			return nil, err
		}
	}
	authConfig := c
	authConfig.Server.PublicURL = publicURL
	authentication := auth.New(ctx, repository, clk, authConfig, key)
	if c.Auth.Google.ClientID != "" {
		provider, e := auth.NewOIDC(ctx, "https://accounts.google.com", c.Auth.Google.ClientID, c.Auth.Google.ClientSecret, publicURL+"/auth/google/callback", clk.Now)
		if e != nil {
			hook.Close()
			_ = q.Close()
			_ = dbs.Close()
			return nil, e
		}
		authentication.Registry.Register(auth.ProviderAdapter{Name: "google", Provider: provider}, ext.Global)
	}
	if c.Auth.GitHub.ClientID != "" {
		authentication.Registry.Register(auth.ProviderAdapter{Name: "github", Provider: &auth.GitHub{Config: oauth2.Config{ClientID: c.Auth.GitHub.ClientID, ClientSecret: c.Auth.GitHub.ClientSecret, RedirectURL: publicURL + "/auth/github/callback", Scopes: []string{"read:user", "user:email"}, Endpoint: oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token"}}, APIURL: "https://api.github.com"}}, ext.Global)
	}
	if c.Auth.OIDC.Issuer != "" {
		provider, e := auth.NewOIDC(ctx, c.Auth.OIDC.Issuer, c.Auth.OIDC.ClientID, c.Auth.OIDC.ClientSecret, publicURL+"/auth/oidc:default/callback", clk.Now)
		if e != nil {
			_ = a.Close()
			return nil, e
		}
		provider.Config.Scopes = c.Auth.OIDC.Scopes
		provider.AllowUnverifiedEmail = !c.Auth.OIDC.RequireEmailVerified
		authentication.Registry.Register(auth.ProviderAdapter{Name: "oidc:default", Provider: provider}, ext.Global)
	}
	if c.Auth.DevLogin {
		log.WarnContext(ctx, "WARNING: DEVELOPMENT LOGIN ENABLED; anyone can authenticate")
	}
	if exists, err := authentication.HasUsers(ctx); err == nil && !exists && !c.Auth.AllowSignup {
		log.InfoContext(ctx, "no users yet: the first person to sign in gets an account, then signup stays closed")
	}
	if c.Mode == "hosted" {
		hook.Hosts = &outbound.Hosts{Clock: clk, Rate: c.Sinks.Webhook.PerHostRateLimit}
	}
	quotas := &limits.Registry{Config: c.Limits, Retention: c.Retention, Store: repository, Clock: clk}
	if err := quotas.Seed(ctx); err != nil {
		_ = a.Close()
		return nil, err
	}
	authentication.Limits = quotas
	authentication.Authz.Log = log
	authentication.Authz.SetContext(ctx, auth.DefaultAuthorizer{})
	audit := auth.NewAuditBus(log)
	a.audit = audit
	repository.SetAuditListener(audit.Publish, audit.Active)
	admin := &auth.Administration{Auth: authentication, Store: repository, Clock: clk, Limits: quotas}
	d.Auth = authentication.Registry
	d.Authz = authentication.Authz
	d.Entitlements = auth.NoEntitlements{}
	d.Directory = admin
	d.Audit = audit
	lifecycle := events.NewSubscribers(log)
	a.lifecycle = lifecycle
	d.Events = lifecycle
	d.Stats = stats{repository}
	d.Workspaces = admin
	d.Session = authentication
	d.Limits = quotas
	a.deps = d
	authentication.Mount(r)
	jobs := &api.Jobs{Store: repository, Queue: q, Scheduler: scheduler, Clock: clk, Config: c, Webhook: hook, Auth: authentication, Vault: vault, Limits: quotas}
	instruments, err := telemetry.NewInstruments(otel.Meter("cronpad"))
	if err != nil {
		_ = q.Close()
		_ = dbs.Close()
		return nil, err
	}
	writer := results.New(repository, clk, instruments, c.Scheduler.LeaseDuration.Duration())
	a.janitor = &janitor.Janitor{Store: repository, Limits: quotas, Writer: writer, Clock: clk, Config: c.Retention, Metrics: instruments, Log: log}
	evaluator := &alert.Evaluator{Store: repository, Writer: writer, Clock: clk, Config: c, PublicURL: publicURL}
	if err = evaluator.Refresh(ctx); err != nil {
		_ = a.Close()
		return nil, err
	}
	notifier := &alert.Notifier{Hosts: hook.Hosts, Limits: quotas, Store: repository, Writer: writer, Clock: clk, Vault: vault, Email: c.Alerts.Email, Policy: hook.Policy, PublicURL: publicURL, SigningKey: key, Metrics: instruments}
	a.notifier = notifier
	jobs.Alerts, jobs.Notifier = evaluator, notifier
	jobs.Mount(r)
	if c.MCP.Enabled {
		r.Handle(c.MCP.Path, authentication.APIKeyMiddleware(jobs.APILimiter(mcpserver.Handler(jobs))))
	}

	sinks := map[string]sink.Sink{}
	if c.Sinks.Webhook.Enabled {
		sinks["webhook"] = hook
	}
	if c.Sinks.Command.Enabled && c.Mode != "hosted" {
		sinks["command"] = command.New(c.Sinks.Command)
	}
	cache := worker.NewJobCache(repository, 10000)
	scheduler.Invalidate = cache.Invalidate
	engine := &worker.Engine{Cache: cache, Store: repository, Queue: q, Writer: writer, Clock: clk, Config: c, Sinks: sinks, Metrics: instruments, Log: log, Tracer: otel.Tracer("cronpad"), Events: events.Multi{evaluator, lifecycle}, Alerts: evaluator, Vault: vault, Limits: quotas, RNG: rand.New(rand.NewSource(clk.Now().UnixNano()))}
	a.Dispatcher = &dispatcher.Dispatcher{Store: repository, Queue: q, Writer: writer, Workers: engine, Clock: clk, Config: c, Metrics: instruments, Log: log, Wake: scheduler.WakeCh}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		var out map[string]any
		var ready, controlOK, historyOK bool
		probes := new(errgroup.Group)
		probes.Go(func() error { out, ready = a.Dispatcher.Ready(ctx); return nil })
		probes.Go(func() error {
			_, err := dbs.Control.Writer.ExecContext(ctx, "UPDATE jobs SET updated_at=updated_at WHERE 0")
			controlOK = err == nil
			return nil
		})
		probes.Go(func() error {
			_, err := dbs.History.Writer.ExecContext(ctx, "UPDATE runs SET attempts=attempts WHERE 0")
			historyOK = err == nil
			return nil
		})
		_ = probes.Wait()
		sqliteOK := controlOK && historyOK

		out["sqlite_writable"] = sqliteOK
		w.Header().Set("Content-Type", "application/json")
		if !ready || !sqliteOK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	r.Get("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(version.Get(edition))
	})
	// DECISION: every crawler is allowed, so link-preview bots that honour robots.txt (Twitterbot) can read the
	// Open Graph tags. Docs and marketing pages live in the separate landing site.
	r.Get("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "User-agent: *\nAllow: /\n")
	})
	if c.UI.Dashboard {
		mountDashboard(r, web.Dist(), c.MCP.Path, c.SharedCookieDomain(), c.Server.PublicURL)
	}
	for _, e := range extensions {
		e.Routes(r, d)
	}
	api.MountFeatures(r, c, publicURL, authentication.Registry.Names(), authentication.HasUsers, c.Limits.APIRateLimitPerMinute)
	traced := otelchi.Middleware("cronpad", otelchi.WithChiRoutes(r))(r)
	sessionHandler := authentication.Sessions.LoadAndSave(traced)
	// DECISION: public endpoints bypass session loading even when the browser sends
	// a cookie, keeping them independent of SQLite.
	a.Handler = withCORS(c.UI.CORSOrigins, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		if p == "/robots.txt" || p == "/api/v1/schedules/preview" || p == "/api/v1/schedules/validate" {
			traced.ServeHTTP(w, req)
			return
		}
		sessionHandler.ServeHTTP(w, req)
	}))
	auditCtx, cancel := context.WithCancel(ctx)
	a.auditCancel = cancel
	a.auditDone = make(chan struct{})
	// The extension buses share one lifetime: both drain until Close.
	go func() {
		defer close(a.auditDone)
		lifecycleDone := make(chan struct{})
		go func() { defer close(lifecycleDone); _ = a.lifecycle.Run(auditCtx) }()
		_ = a.audit.Run(auditCtx)
		<-lifecycleDone
	}()
	return a, nil
}

// withCORS lets the listed origins (e.g. the landing site's schedule preview) call /api/.
// DECISION: credentials are never allowed cross-origin, so session cookies stay same-site;
// other origins can use public endpoints or an API key in the Authorization header.
func withCORS(origins []string, next http.Handler) http.Handler {
	if len(origins) == 0 {
		return next
	}
	allowed := map[string]bool{}
	for _, o := range origins {
		allowed[strings.TrimRight(o, "/")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		origin := req.Header.Get("Origin")
		if !strings.HasPrefix(req.URL.Path, "/api/") || origin == "" {
			next.ServeHTTP(w, req)
			return
		}
		w.Header().Add("Vary", "Origin")
		if !allowed[origin] {
			next.ServeHTTP(w, req)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", "Retry-After, RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset, X-Request-ID")
		if req.Method == http.MethodOptions && req.Header.Get("Access-Control-Request-Method") != "" {
			h.Add("Vary", "Access-Control-Request-Method, Access-Control-Request-Headers")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, req)
	})
}
func (a *App) Close() error {
	if a.auditCancel != nil {
		a.auditCancel()
		<-a.auditDone
	}
	a.hook.Close()
	return errors.Join(a.queue.Close(), a.DBs.Close())
}

// Run owns HTTP and extension goroutines until cancellation or a component failure.
func (a *App) Run(ctx context.Context, metrics http.Handler) error {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", a.Config.Server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	group, ctx := errgroup.WithContext(ctx)
	server := &http.Server{Handler: a.Handler, ReadTimeout: a.Config.Server.ReadTimeout.Duration(), WriteTimeout: a.Config.Server.WriteTimeout.Duration(), BaseContext: func(net.Listener) context.Context { return ctx }}
	var metricServer *http.Server
	var metricListener net.Listener
	if metrics != nil {
		metricListener, err = listenConfig.Listen(ctx, "tcp", a.Config.Telemetry.PrometheusAddr)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("metrics listen: %w", err)
		}
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics)
		metricServer = &http.Server{Handler: mux, ReadHeaderTimeout: a.Config.Server.ReadTimeout.Duration()}
	}
	// Keep the result writer alive until HTTP mutations and the notifier have stopped producing writes.
	pipelineCtx, stopPipeline := context.WithCancel(context.WithoutCancel(ctx))
	defer stopPipeline()
	notifierDone := make(chan struct{})
	group.Go(func() error { return a.Dispatcher.Run(pipelineCtx) })
	janitorDone := make(chan struct{})
	group.Go(func() error { defer close(janitorDone); return a.janitor.Run(ctx) })
	group.Go(func() error { defer close(notifierDone); return a.notifier.Run(ctx) })
	group.Go(func() error {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP serve: %w", err)
	})
	if metricServer != nil {
		group.Go(func() error {
			err := metricServer.Serve(metricListener)
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("metrics serve: %w", err)
		})
	}
	for _, extension := range a.extensions {
		group.Go(func() error {
			if err := extension.Start(ctx, a.deps); err != nil {
				return fmt.Errorf("extension %s: %w", extension.Name(), err)
			}
			return nil
		})
	}
	group.Go(func() error {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.Config.Server.ShutdownGrace.Duration())
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		if metricServer != nil {
			if e := metricServer.Shutdown(shutdown); e != nil {
				_ = metricServer.Close()
				err = errors.Join(err, e)
			}
		}
		<-notifierDone
		<-janitorDone
		stopPipeline()
		return err
	})
	a.log.InfoContext(ctx, "server started", logx.Address, listener.Addr().String())
	err = group.Wait()
	a.log.InfoContext(context.WithoutCancel(ctx), "server stopped")
	return err
}

// openQueue opens the SQLite queue, which also closes it on shutdown.
func openQueue(ctx context.Context, c config.Config, clk clock.Clock) (*queue.SQLite, error) {
	q, err := queue.NewSQLite(ctx, c.QueuePath(), clk, c.Database.BusyTimeout.Duration())
	if err != nil {
		return nil, fmt.Errorf("queue: %w", err)
	}
	return q, nil
}
