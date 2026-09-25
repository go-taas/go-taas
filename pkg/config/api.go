// Package config provides configuration loading for all platform binaries.
//
// Configuration is merged from a glob of YAML files (e.g. configs/*.yaml),
// with environment variables overriding any value: the key "db.master.host"
// is overridden by CONFIG_DB_MASTER_HOST. After loading, GetConfig() returns
// the process-wide configuration.
package config

import (
	"regexp"
	"strings"
	"time"
)

// tenancyIDRegex is the shared id syntax for organizations and projects:
// 3-64 chars, lowercase alphanumeric with internal hyphens, starting
// with a lowercase letter or digit.
var tenancyIDRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)

// TenancyIDRegex returns the id syntax shared by organizations and
// projects. Services use it to validate caller-supplied ids.
func TenancyIDRegex() *regexp.Regexp {
	return tenancyIDRegex
}

// DBConfig holds connection parameters for one PostgreSQL database.
type DBConfig struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	DBName          string        `mapstructure:"dbName"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	SSLMode         string        `mapstructure:"sslMode"`
	MaxOpenConns    int           `mapstructure:"maxOpenConns"`
	MaxIdleConns    int           `mapstructure:"maxIdleConns"`
	ConnMaxLifetime time.Duration `mapstructure:"connMaxLifetime"`
	Debug           bool          `mapstructure:"debug"`
}

// Databases groups the database endpoints used by the platform.
type Databases struct {
	Master DBConfig `mapstructure:"master"`
}

// Redis holds connection parameters for the Redis-compatible cache
// (Redis or Valkey).
type Redis struct {
	Host        string        `mapstructure:"host"`
	Port        int           `mapstructure:"port"`
	Password    string        `mapstructure:"password"`
	DB          int           `mapstructure:"db"`
	MaxIdle     int           `mapstructure:"maxIdle"`
	MaxActive   int           `mapstructure:"maxActive"`
	IdleTimeout time.Duration `mapstructure:"idleTimeout"`
}

// MQConfig holds connection parameters for the message queue. The platform
// targets Kafka/NATS as brokers; the wire details are hidden behind the
// pkg/mq abstraction.
type MQConfig struct {
	// Driver selects the broker implementation ("nats" / "kafka").
	Driver string `mapstructure:"driver"`
	// URL is the broker connection string.
	URL string `mapstructure:"url"`
	// Namespace prefixes all subjects/topics to isolate environments.
	Namespace string `mapstructure:"namespace"`
}

// Argon2Params holds the parameters of the Argon2id KDF used for API-key
// hashing. Zero values fall back to the shipped defaults at the use site
// (WithDefaults), so custom configs that predate the section keep working.
type Argon2Params struct {
	// Algorithm selects the KDF: "argon2id" (bcrypt is reserved as a
	// fallback for platforms without Argon2 support, not shipped).
	Algorithm string `mapstructure:"algorithm"`
	// Time is the number of Argon2id passes.
	Time int `mapstructure:"time"`
	// MemoryMiB is the Argon2id memory cost in MiB.
	MemoryMiB int `mapstructure:"memoryMiB"`
	// Parallelism is the Argon2id thread count.
	Parallelism int `mapstructure:"parallelism"`
}

// WithDefaults returns a copy of p with zero values replaced by the
// shipped defaults: argon2id, t=1, m=64 MiB, p=1.
func (p Argon2Params) WithDefaults() Argon2Params {
	if p.Algorithm == "" {
		p.Algorithm = "argon2id"
	}
	if p.Time <= 0 {
		p.Time = 1
	}
	if p.MemoryMiB <= 0 {
		p.MemoryMiB = 64
	}
	if p.Parallelism <= 0 {
		p.Parallelism = 1
	}
	return p
}

// AuthConfig holds auth-module specific settings.
type AuthConfig struct {
	// SessionTTL bounds the lifetime of issued access tokens.
	SessionTTL time.Duration `mapstructure:"sessionTTL"`
	// APIKeyCacheTTL is the TTL of the positive API-key cache in Redis.
	// It must stay <= the Wasm local cache TTL so the revoke propagation
	// bound holds even when the Redis delete fails.
	APIKeyCacheTTL time.Duration `mapstructure:"apiKeyCacheTTL"`
	// APIKeyHash holds the Argon2id parameters used for API-key hashing.
	APIKeyHash Argon2Params `mapstructure:"apiKeyHash"`
	// LocalPasswordLogin enables/disables local password login alongside SSO.
	LocalPasswordLogin bool `mapstructure:"localPasswordLogin"`
	// AutoRegister enables JIT account provisioning on first SSO login.
	AutoRegister bool `mapstructure:"autoRegister"`
}

// MeteringConfig holds metering-module specific settings.
type MeteringConfig struct {
	// BufferSize is the number of metering events buffered before flush.
	// Reserved for the future Redis metering buffer (deferred; unused at
	// v1 — the MQ + DB path is sufficient at current scale).
	BufferSize int `mapstructure:"bufferSize"`
	// FlushInterval bounds how long events may wait in the buffer.
	// Reserved for the future Redis metering buffer (deferred).
	FlushInterval time.Duration `mapstructure:"flushInterval"`
	// Settlement configures the hourly settlement runner.
	Settlement MeteringSettlementConfig `mapstructure:"settlement"`
	// Retention configures the voucher retention cleanup runner.
	Retention MeteringRetentionConfig `mapstructure:"retention"`
	// EventConsumer configures the metering.events MQ consumer.
	EventConsumer MeteringEventConsumerConfig `mapstructure:"eventConsumer"`
}

// MeteringSettlementConfig holds the hourly settlement runner settings.
type MeteringSettlementConfig struct {
	// Enabled turns the settlement runner on or off (incident-triage
	// kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Interval is the ticker period between settlement passes.
	Interval time.Duration `mapstructure:"interval"`
	// GracePeriod is the extra wait after an hour closes before it is
	// settlement-eligible (the late-arrival window).
	GracePeriod time.Duration `mapstructure:"gracePeriod"`
	// Workers is the number of concurrent bucket settlement workers.
	Workers int `mapstructure:"workers"`
}

// MeteringRetentionConfig holds the voucher retention runner settings.
type MeteringRetentionConfig struct {
	// Enabled turns the retention runner on or off (incident-triage
	// kill switch).
	Enabled bool `mapstructure:"enabled"`
	// VoucherTTL is how long settled vouchers are kept before deletion.
	VoucherTTL time.Duration `mapstructure:"voucherTTL"`
	// RequestLogTTL is how long request logs are kept before deletion
	// (feature #12, AD4); default 30 days.
	RequestLogTTL time.Duration `mapstructure:"requestLogTTL"`
	// BatchSize is the number of rows deleted per retention pass.
	BatchSize int `mapstructure:"batchSize"`
	// Interval is the ticker period between retention passes.
	Interval time.Duration `mapstructure:"interval"`
}

// MeteringEventConsumerConfig holds the metering.events consumer
// Runner settings, mirroring infer.statusConsumer.
type MeteringEventConsumerConfig struct {
	// Enabled turns the event consumer Runner on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of concurrent event handlers.
	Workers int `mapstructure:"workers"`
}

// BillingConfig holds billing-module specific settings.
type BillingConfig struct {
	// SettlementInterval is the period between settlement runs.
	SettlementInterval time.Duration `mapstructure:"settlementInterval"`
	// Currency is the single platform billing currency (D4); price
	// entries, charge records and bills carry it.
	Currency string `mapstructure:"currency"`
	// EventConsumer configures the billing metering.events consumer
	// (usage-line ingestion).
	EventConsumer BillingConsumerConfig `mapstructure:"eventConsumer"`
	// SettlementsConsumer configures the billing.settlements consumer
	// (the charge trigger).
	SettlementsConsumer BillingConsumerConfig `mapstructure:"settlementsConsumer"`
	// Reconciliation configures the charging safety-net runner.
	Reconciliation BillingReconciliationConfig `mapstructure:"reconciliation"`
	// CycleReset configures the postpaid monthly cycle-reset runner
	// (feature #8, AD6).
	CycleReset BillingCycleResetConfig `mapstructure:"cycleReset"`
	// AutoRecharge configures the feature-14 auto-recharge runner.
	AutoRecharge BillingAutoRechargeConfig `mapstructure:"autoRecharge"`
}

// BillingAutoRechargeConfig holds the auto-recharge runner settings
// (feature-14 AD4).
type BillingAutoRechargeConfig struct {
	// Enabled turns the auto-recharge runner on or off.
	Enabled bool `mapstructure:"enabled"`
	// Interval is the ticker period between auto-recharge passes.
	Interval time.Duration `mapstructure:"interval"`
}

// BillingCycleResetConfig holds the postpaid cycle-reset runner
// settings.
type BillingCycleResetConfig struct {
	// Enabled turns the cycle-reset runner on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Interval is the ticker period between reset checks (the reset
	// itself is condition-based, so short intervals are cheap).
	Interval time.Duration `mapstructure:"interval"`
}

// BillingConsumerConfig holds the kill switch and worker count of a
// billing MQ consumer Runner.
type BillingConsumerConfig struct {
	// Enabled turns the consumer Runner on or off (incident-triage
	// kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of concurrent event handlers.
	Workers int `mapstructure:"workers"`
}

// BillingReconciliationConfig holds the charging reconciliation runner
// settings.
type BillingReconciliationConfig struct {
	// Enabled turns the reconciliation runner on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Interval is the ticker period between reconciliation passes.
	Interval time.Duration `mapstructure:"interval"`
	// GracePeriod is the extra wait after an hour closes before it is
	// charge-eligible (mirrors metering settlement).
	GracePeriod time.Duration `mapstructure:"gracePeriod"`
	// Workers is the number of concurrent bucket pricing workers.
	Workers int `mapstructure:"workers"`
}

// ControllerConfig holds controller-specific settings.
type ControllerConfig struct {
	// Workers is the number of concurrent reconcile workers.
	Workers int `mapstructure:"workers"`
	// MaxRetries bounds retries for a failed reconcile task.
	MaxRetries int `mapstructure:"maxRetries"`
}

// InferConfig holds infer-module specific settings.
type InferConfig struct {
	// EndpointBaseURL is the base URL the controller uses to compose
	// inference endpoint URLs ("<base>/v1"). Empty means the controller
	// records the in-cluster Service DNS name instead.
	EndpointBaseURL string `mapstructure:"endpointBaseURL"`
	// StatusConsumer controls the infer module's status-subject consumer.
	StatusConsumer StatusConsumerConfig `mapstructure:"statusConsumer"`
}

// StatusConsumerConfig holds the kill switch and worker count of the
// infer status consumer.
type StatusConsumerConfig struct {
	// Enabled turns the status consumer Runner on or off (incident-triage
	// kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of concurrent status handlers.
	Workers int `mapstructure:"workers"`
}

// ImageRegistryEntry is one image-registry seed row. Since feature #3
// the images table is the single source of truth; this seed is a
// deprecated bootstrap default read only when the table is empty at
// startup (first-boot, insert-only).
type ImageRegistryEntry struct {
	// ImageID is the stable image identifier used by deploy requests.
	ImageID string `mapstructure:"imageId"`
	// Name is the image repository name (without tag).
	Name string `mapstructure:"name"`
	// Tag is the image tag.
	Tag string `mapstructure:"tag"`
	// Accelerator names the hardware platform the image runs on
	// (nvidia, iluvatar, metax).
	Accelerator string `mapstructure:"accelerator"`
	// Engine names the inference engine (vllm, sglang, ...).
	Engine string `mapstructure:"engine"`
}

// ImageWarmupStatusConsumerConfig holds the warmup status consumer
// Runner settings, mirroring infer.statusConsumer.
type ImageWarmupStatusConsumerConfig struct {
	// Enabled turns the warmup status consumer Runner on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of concurrent status handlers.
	Workers int `mapstructure:"workers"`
}

// ImageConfig holds the image module settings: the deprecated
// first-boot registry seed and the warmup status consumer.
type ImageConfig struct {
	// Registry is the seed list of engine images (first-boot only).
	Registry []ImageRegistryEntry `mapstructure:"registry"`
	// WarmupStatusConsumer configures the warmup status Runner.
	WarmupStatusConsumer ImageWarmupStatusConsumerConfig `mapstructure:"warmupStatusConsumer"`
}

// ModelAuthConfig holds the per-tenant model authorization settings
// (feature #13).
type ModelAuthConfig struct {
	// CacheTTL is the TTL of the data-plane per-(org, model)
	// authorization cache in the auth service (AD6). It bounds how long
	// a revoked organization may keep calling a restricted model.
	CacheTTL time.Duration `mapstructure:"cacheTTL"`
}

// ModelConfig holds model-module specific settings.
type ModelConfig struct {
	// Auth configures the data-plane model authorization gate.
	Auth ModelAuthConfig `mapstructure:"auth"`
}

// LogConfig holds logging settings loaded from configuration files.
type LogConfig struct {
	// Level is the minimum log level: debug, info, warn, error.
	Level string `mapstructure:"level"`
	// Encoding selects "console" or "json" output.
	Encoding string `mapstructure:"encoding"`
}

// TenancyConfig holds the tenancy-module settings: the first-boot
// default-organization seed and the invitation expiry window.
type TenancyConfig struct {
	// DefaultOrgID is the organization id seeded on first boot (when the
	// organizations table is empty). It must match the org-id regex
	// ^[a-z0-9][a-z0-9-]{2,63}$.
	DefaultOrgID string `mapstructure:"defaultOrgId"`
	// DefaultOrgDisplayName is the seeded organization's display name.
	DefaultOrgDisplayName string `mapstructure:"defaultOrgDisplayName"`
	// InvitationTTL is the default invitation expiry window (feature
	// #10, AD5). 0 means the 7-day default.
	InvitationTTL time.Duration `mapstructure:"invitationTTL"`
}

// AuditConfig holds audit-module specific settings (feature #15).
type AuditConfig struct {
	// Retention configures the audit-event retention cleanup runner.
	Retention AuditRetentionConfig `mapstructure:"retention"`
	// ExportMaxRows is the export row cap (AD7); default 10000.
	ExportMaxRows int `mapstructure:"exportMaxRows"`
}

// AuditRetentionConfig holds the audit retention runner settings
// (feature #15, AD6).
type AuditRetentionConfig struct {
	// Enabled turns the audit retention runner on or off (incident-triage
	// kill switch).
	Enabled bool `mapstructure:"enabled"`
	// EventTTL is how long audit events are kept before deletion;
	// default 365 days.
	EventTTL time.Duration `mapstructure:"eventTTL"`
	// BatchSize is the number of rows deleted per retention pass.
	BatchSize int `mapstructure:"batchSize"`
	// Interval is the ticker period between retention passes.
	Interval time.Duration `mapstructure:"interval"`
}

// Configuration is the root of the merged configuration tree.
type Configuration struct {
	Databases  Databases        `mapstructure:"db"`
	Redis      Redis            `mapstructure:"redis"`
	MQ         MQConfig         `mapstructure:"mq"`
	Auth       AuthConfig       `mapstructure:"auth"`
	Metering   MeteringConfig   `mapstructure:"metering"`
	Billing    BillingConfig    `mapstructure:"billing"`
	Controller ControllerConfig `mapstructure:"controller"`
	Infer      InferConfig      `mapstructure:"infer"`
	Image      ImageConfig      `mapstructure:"image"`
	Model      ModelConfig      `mapstructure:"model"`
	Tenancy    TenancyConfig    `mapstructure:"tenancy"`
	Audit      AuditConfig      `mapstructure:"audit"`
	Log        LogConfig        `mapstructure:"log"`
}

// Validate checks semantic constraints that cannot be expressed as struct
// tags. It returns an error describing the first violation found.
func (c *Configuration) Validate() error {
	if c.Databases.Master.MaxIdleConns > c.Databases.Master.MaxOpenConns {
		return &FieldError{Field: "db.master.maxIdleConns", Reason: "must not exceed db.master.maxOpenConns"}
	}
	if c.Redis.MaxActive < 0 || c.Redis.MaxIdle < 0 {
		return &FieldError{Field: "redis", Reason: "pool sizes must be non-negative"}
	}
	if c.Auth.APIKeyCacheTTL < 0 {
		return &FieldError{Field: "auth.apiKeyCacheTTL", Reason: "must not be negative"}
	}
	// The session TTL is defaulted by applyDefaults before Validate in the
	// production path; a raw zero-value config (as built by tests) is
	// allowed through so the empty default is not rejected.
	if c.Auth.SessionTTL < 0 {
		return &FieldError{Field: "auth.sessionTTL", Reason: "must not be negative"}
	}
	if c.Auth.APIKeyHash.Time < 0 || c.Auth.APIKeyHash.MemoryMiB < 0 || c.Auth.APIKeyHash.Parallelism < 0 {
		return &FieldError{Field: "auth.apiKeyHash", Reason: "argon2 parameters must be non-negative"}
	}
	if c.Infer.StatusConsumer.Workers < 0 {
		return &FieldError{Field: "infer.statusConsumer.workers", Reason: "must not be negative"}
	}
	if c.Image.WarmupStatusConsumer.Workers < 0 {
		return &FieldError{Field: "image.warmupStatusConsumer.workers", Reason: "must not be negative"}
	}
	if c.Model.Auth.CacheTTL < 0 {
		return &FieldError{Field: "model.auth.cacheTTL", Reason: "must not be negative"}
	}
	// The tenancy defaults are filled by applyDefaults before Validate
	// in the production path; a raw zero-value config (as built by
	// tests) is allowed through so the empty default is not rejected.
	if c.Tenancy.DefaultOrgID != "" && !tenancyIDRegex.MatchString(c.Tenancy.DefaultOrgID) {
		return &FieldError{Field: "tenancy.defaultOrgId", Reason: "must match ^[a-z0-9][a-z0-9-]{2,63}$"}
	}
	if l := len(strings.TrimSpace(c.Tenancy.DefaultOrgDisplayName)); l > 128 {
		return &FieldError{Field: "tenancy.defaultOrgDisplayName", Reason: "must be 1-128 characters"}
	}
	if c.Metering.Settlement.Interval < 0 {
		return &FieldError{Field: "metering.settlement.interval", Reason: "must not be negative"}
	}
	if c.Metering.Settlement.GracePeriod < 0 {
		return &FieldError{Field: "metering.settlement.gracePeriod", Reason: "must not be negative"}
	}
	if c.Metering.Settlement.Workers < 0 {
		return &FieldError{Field: "metering.settlement.workers", Reason: "must be non-negative"}
	}
	if c.Metering.Retention.VoucherTTL < 0 {
		return &FieldError{Field: "metering.retention.voucherTTL", Reason: "must not be negative"}
	}
	if c.Metering.Retention.RequestLogTTL < 0 {
		return &FieldError{Field: "metering.retention.requestLogTTL", Reason: "must not be negative"}
	}
	if c.Metering.Retention.BatchSize < 0 {
		return &FieldError{Field: "metering.retention.batchSize", Reason: "must not be negative"}
	}
	if c.Metering.Retention.Interval < 0 {
		return &FieldError{Field: "metering.retention.interval", Reason: "must not be negative"}
	}
	if c.Metering.EventConsumer.Workers < 0 {
		return &FieldError{Field: "metering.eventConsumer.workers", Reason: "must be non-negative"}
	}
	if c.Billing.Currency == "" {
		return &FieldError{Field: "billing.currency", Reason: "must not be empty"}
	}
	if len(c.Billing.Currency) > 8 {
		return &FieldError{Field: "billing.currency", Reason: "must not exceed 8 characters"}
	}
	if c.Billing.EventConsumer.Workers < 0 {
		return &FieldError{Field: "billing.eventConsumer.workers", Reason: "must be non-negative"}
	}
	if c.Billing.SettlementsConsumer.Workers < 0 {
		return &FieldError{Field: "billing.settlementsConsumer.workers", Reason: "must be non-negative"}
	}
	if c.Billing.Reconciliation.Interval < 0 {
		return &FieldError{Field: "billing.reconciliation.interval", Reason: "must be non-negative"}
	}
	if c.Billing.Reconciliation.GracePeriod < 0 {
		return &FieldError{Field: "billing.reconciliation.gracePeriod", Reason: "must be non-negative"}
	}
	if c.Billing.Reconciliation.Workers < 0 {
		return &FieldError{Field: "billing.reconciliation.workers", Reason: "must be non-negative"}
	}
	if c.Billing.CycleReset.Interval < 0 {
		return &FieldError{Field: "billing.cycleReset.interval", Reason: "must be non-negative"}
	}
	if c.Audit.Retention.EventTTL < 0 {
		return &FieldError{Field: "audit.retention.eventTTL", Reason: "must not be negative"}
	}
	if c.Audit.Retention.BatchSize < 0 {
		return &FieldError{Field: "audit.retention.batchSize", Reason: "must not be negative"}
	}
	if c.Audit.Retention.Interval < 0 {
		return &FieldError{Field: "audit.retention.interval", Reason: "must not be negative"}
	}
	if c.Audit.ExportMaxRows < 0 {
		return &FieldError{Field: "audit.exportMaxRows", Reason: "must not be negative"}
	}
	return nil
}

// FieldError describes a single invalid configuration field.
type FieldError struct {
	Field  string
	Reason string
}

// Error implements the error interface.
func (e *FieldError) Error() string {
	return "invalid config: " + e.Field + ": " + e.Reason
}
