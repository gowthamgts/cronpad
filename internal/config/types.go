// Package config loads and validates process configuration.
package config

type ConfigServer struct {
	Addr           string   `koanf:"addr" validate:"required"`
	PublicURL      string   `koanf:"public_url" validate:"omitempty,http_url"`
	ReadTimeout    Duration `koanf:"read_timeout" validate:"gt=0"`
	WriteTimeout   Duration `koanf:"write_timeout" validate:"gt=0"`
	ShutdownGrace  Duration `koanf:"shutdown_grace" validate:"gt=0"`
	TrustedProxies []string `koanf:"trusted_proxies"`
}

type ConfigUI struct {
	Dashboard   bool     `koanf:"dashboard"`
	CORSOrigins []string `koanf:"cors_origins"`
}

type ConfigDatabase struct {
	Path          string   `koanf:"path" validate:"required"`
	HistoryPath   string   `koanf:"history_path" validate:"required"`
	BusyTimeout   Duration `koanf:"busy_timeout" validate:"gt=0"`
	RunMigrations bool     `koanf:"run_migrations"`
}

type ConfigQueue struct {
	Path string `koanf:"path"`
}

type ConfigScheduler struct {
	ResultsFlushInterval  Duration `koanf:"results_flush_interval" validate:"eq=0|gte=1000000,lte=1000000000"`
	ResultsMaxBatch       int      `koanf:"results_max_batch" validate:"gte=1,lte=10000"`
	DeliveryMode          string   `koanf:"delivery_mode" validate:"oneof=durable fast"`
	TickInterval          Duration `koanf:"tick_interval" validate:"gt=0"`
	ClaimBatch            int      `koanf:"claim_batch" validate:"gt=0"`
	LeaseDuration         Duration `koanf:"lease_duration" validate:"gt=0"`
	ReaperInterval        Duration `koanf:"reaper_interval" validate:"gt=0"`
	LockTTL               Duration `koanf:"lock_ttl" validate:"gt=0"`
	ReconcileInterval     Duration `koanf:"reconcile_interval" validate:"gt=0"`
	OverdueGrace          Duration `koanf:"overdue_grace" validate:"gt=0"`
	FullReconcileInterval Duration `koanf:"full_reconcile_interval" validate:"gt=0"`
	SLAThreshold          Duration `koanf:"sla_threshold" validate:"gt=0"`
	DefaultMisfirePolicy  string   `koanf:"default_misfire_policy" validate:"oneof=fire_once skip fire_all"`
	MisfireFireAllCap     int      `koanf:"misfire_fire_all_cap" validate:"gt=0"`
}

type ConfigWorkers struct {
	Count     int `koanf:"count" validate:"gt=0"`
	QueueSize int `koanf:"queue_size" validate:"gt=0"`
}

type ConfigSinksWebhook struct {
	PerHostRateLimit     int      `koanf:"per_host_rate_limit" validate:"gte=0"`
	Enabled              bool     `koanf:"enabled"`
	DefaultTimeout       Duration `koanf:"default_timeout" validate:"gt=0"`
	MaxTimeout           Duration `koanf:"max_timeout" validate:"gt=0"`
	MaxResponseBytes     int      `koanf:"max_response_bytes" validate:"gte=0"`
	AllowPrivateNetworks bool     `koanf:"allow_private_networks"`
	UserAgent            string   `koanf:"user_agent"`
	// Codes a new job starts with when it doesn't set its own; each job stores what it was created with.
	SuccessCodes []string `koanf:"success_codes"`
	RetryOn      []string `koanf:"retry_on"`
}

type ConfigSinksCommand struct {
	Enabled         bool     `koanf:"enabled"`
	Shell           string   `koanf:"shell"`
	AllowedCommands []string `koanf:"allowed_commands"`
	DefaultTimeout  Duration `koanf:"default_timeout" validate:"gt=0"`
	MaxOutputBytes  int      `koanf:"max_output_bytes" validate:"gte=0"`
	EnvPassthrough  []string `koanf:"env_passthrough"`
	// Exit codes a new job starts with when it doesn't set its own; each job stores what it was created with.
	SuccessExitCodes []int `koanf:"success_exit_codes"`
	RetryExitCodes   []int `koanf:"retry_exit_codes"`
}

type ConfigSinks struct {
	Webhook ConfigSinksWebhook `koanf:"webhook"`
	Command ConfigSinksCommand `koanf:"command"`
}

type ConfigRetryDefaults struct {
	MaxAttempts     int      `koanf:"max_attempts" validate:"gt=0"`
	Backoff         string   `koanf:"backoff" validate:"oneof=fixed exponential"`
	InitialInterval Duration `koanf:"initial_interval" validate:"gt=0"`
	MaxInterval     Duration `koanf:"max_interval" validate:"gt=0"`
	Multiplier      float64  `koanf:"multiplier" validate:"gte=1"`
	Jitter          float64  `koanf:"jitter" validate:"gte=0,lte=1"`
}

type ConfigRetry struct {
	Defaults ConfigRetryDefaults `koanf:"defaults"`
}

type ConfigAutoDisable struct {
	Enabled             bool `koanf:"enabled"`
	ConsecutiveFailures int  `koanf:"consecutive_failures" validate:"gt=0"`
	Notify              bool `koanf:"notify"`
}

type ConfigLimits struct {
	MaxRunsPerMonthPerWorkspace   int      `koanf:"max_runs_per_month_per_workspace" validate:"gte=0"`
	OverQuota                     string   `koanf:"over_quota" validate:"oneof=block allow"`
	MaxConcurrentRunsPerWorkspace int      `koanf:"max_concurrent_runs_per_workspace" validate:"gte=0"`
	MaxAlertEmailsPerDay          int      `koanf:"max_alert_emails_per_day" validate:"gte=0"`
	MaxJobsPerWorkspace           int      `koanf:"max_jobs_per_workspace" validate:"gte=0"`
	MinInterval                   Duration `koanf:"min_interval" validate:"gte=0"`
	MaxRunsPerDayPerWorkspace     int      `koanf:"max_runs_per_day_per_workspace" validate:"gte=0"`
	MaxAPIKeysPerWorkspace        int      `koanf:"max_api_keys_per_workspace" validate:"gte=0"`
	MaxRetryAttempts              int      `koanf:"max_retry_attempts" validate:"gte=0"`
	APIRateLimitPerMinute         int      `koanf:"api_rate_limit_per_minute" validate:"gte=0"`
}

type ConfigRetention struct {
	Runs               Duration `koanf:"runs" validate:"gte=0"`
	AttemptBodies      Duration `koanf:"attempt_bodies" validate:"gte=0"`
	StoreSuccessBodies bool     `koanf:"store_success_bodies"`
	AuditLog           Duration `koanf:"audit_log" validate:"gte=0"`
	AlertOutbox        Duration `koanf:"alert_outbox" validate:"gte=0"`
	JobStatsDaily      Duration `koanf:"job_stats_daily" validate:"gte=0"`
	JanitorInterval    Duration `koanf:"janitor_interval" validate:"gt=0"`
	JanitorBatch       int      `koanf:"janitor_batch" validate:"gt=0"`
}

type ConfigAuthGoogle struct {
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
}

type ConfigAuthGitHub struct {
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
}

// ConfigAuthOIDC configures a single instance-wide OIDC login provider.
type ConfigAuthOIDC struct {
	Issuer               string   `koanf:"issuer" validate:"omitempty,http_url"`
	ClientID             string   `koanf:"client_id"`
	ClientSecret         string   `koanf:"client_secret"`
	DisplayName          string   `koanf:"display_name"`
	Scopes               []string `koanf:"scopes"`
	RequireEmailVerified bool     `koanf:"require_email_verified"`
}

type ConfigAuth struct {
	OIDC                ConfigAuthOIDC   `koanf:"oidc"`
	SessionLifetime     Duration         `koanf:"session_lifetime" validate:"gt=0"`
	AllowedEmailDomains []string         `koanf:"allowed_email_domains"`
	AllowSignup         bool             `koanf:"allow_signup"`
	DevLogin            bool             `koanf:"dev_login"`
	Google              ConfigAuthGoogle `koanf:"google"`
	GitHub              ConfigAuthGitHub `koanf:"github"`
}

type ConfigSecrets struct {
	EncryptionKey string `koanf:"encryption_key"`
}

type ConfigAlertsEmail struct {
	Enabled  bool   `koanf:"enabled"`
	SMTPHost string `koanf:"smtp_host"`
	SMTPPort int    `koanf:"smtp_port" validate:"gte=1,lte=65535"`
	Username string `koanf:"username"`
	Password string `koanf:"password"`
	From     string `koanf:"from"`
}

type ConfigAlerts struct {
	Email ConfigAlertsEmail `koanf:"email"`
}

type ConfigTelemetry struct {
	ServiceName       string  `koanf:"service_name" validate:"required"`
	OTLPEndpoint      string  `koanf:"otlp_endpoint"`
	OTLPProtocol      string  `koanf:"otlp_protocol" validate:"oneof=grpc http"`
	PrometheusAddr    string  `koanf:"prometheus_addr"`
	TracesSampleRatio float64 `koanf:"traces_sample_ratio" validate:"gte=0,lte=1"`
	OTLPLogs          bool    `koanf:"otlp_logs"`
}

type ConfigLogging struct {
	Level     string `koanf:"level" validate:"oneof=debug info warn error"`
	Format    string `koanf:"format" validate:"oneof=json logfmt"`
	AddSource bool   `koanf:"add_source"`
}

type ConfigMCP struct {
	Enabled bool   `koanf:"enabled"`
	Path    string `koanf:"path"`
}

type Config struct {
	Mode        string            `koanf:"mode" validate:"oneof=selfhosted hosted"`
	Server      ConfigServer      `koanf:"server"`
	UI          ConfigUI          `koanf:"ui"`
	Database    ConfigDatabase    `koanf:"database"`
	Queue       ConfigQueue       `koanf:"queue"`
	Scheduler   ConfigScheduler   `koanf:"scheduler"`
	Workers     ConfigWorkers     `koanf:"workers"`
	Sinks       ConfigSinks       `koanf:"sinks"`
	Retry       ConfigRetry       `koanf:"retry"`
	AutoDisable ConfigAutoDisable `koanf:"auto_disable"`
	Limits      ConfigLimits      `koanf:"limits"`
	Retention   ConfigRetention   `koanf:"retention"`
	Auth        ConfigAuth        `koanf:"auth"`
	Secrets     ConfigSecrets     `koanf:"secrets"`
	Alerts      ConfigAlerts      `koanf:"alerts"`
	Telemetry   ConfigTelemetry   `koanf:"telemetry"`
	Logging     ConfigLogging     `koanf:"logging"`
	MCP         ConfigMCP         `koanf:"mcp"`
}
