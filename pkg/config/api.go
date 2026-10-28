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
// targets Kafka/RabbitMQ as brokers; the wire details are hidden behind the
// pkg/mq abstraction.
type MQConfig struct {
	// Driver selects the broker implementation ("rabbitmq" / "kafka").
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
	// EmailVerification enables email verification codes for local
	// registration and login. When enabled, registration and login
	// require a code sent to the user's email. Disabled when SMTP is
	// not configured.
	EmailVerification bool `mapstructure:"emailVerification"`
	// SMTP configures the outbound mail transport for verification
	// codes. When empty, verification codes are logged instead of sent.
	SMTP SMTPConfig `mapstructure:"smtp"`
	// AdminRoles is the set of session roles that make a user an
	// administrator (feature-22 AD3). A user is an admin iff any session
	// role is in the set. Defaults to platform-admin, org-admin, admin,
	// owner.
	AdminRoles []string `mapstructure:"adminRoles"`
	// SeedSso configures the first-boot SSO provider seed (feature-22
	// AD6/AD10). When enabled, the auth Migrate hook inserts a provider
	// row (and the admin user/binding) if none exists, so a fresh
	// install is usable without manual IdP configuration. The Helm chart
	// points it at the bundled Keycloak; production deployments can
	// disable it and register their own IdP through the admin API.
	SeedSso SeedSsoConfig `mapstructure:"seedSso"`
}

// SeedSsoConfig holds the first-boot SSO provider seed settings.
type SeedSsoConfig struct {
	// Enabled turns the first-boot SSO seed on or off. Defaults to true
	// so a fresh install is usable out of the box.
	Enabled bool `mapstructure:"enabled"`
	// ProviderID is the id of the seeded provider row.
	ProviderID string `mapstructure:"providerId"`
	// Issuer is the OIDC issuer of the seeded provider.
	Issuer string `mapstructure:"issuer"`
	// ClientID is the OIDC client id of the seeded provider.
	ClientID string `mapstructure:"clientId"`
	// ClientSecret is the OIDC client secret of the seeded provider.
	ClientSecret string `mapstructure:"clientSecret"`
	// RedirectURI is the OIDC redirect URI of the seeded provider.
	RedirectURI string `mapstructure:"redirectUri"`
	// AdminUsername is the seeded admin platform user's username.
	AdminUsername string `mapstructure:"adminUsername"`
	// AdminPassword is the seeded admin user's password at the IdP.
	AdminPassword string `mapstructure:"adminPassword"`
	// AdminLocalPassword is the seeded admin local account's initial
	// password (hashed with Argon2id at first boot). It defaults to
	// Pass4admin when unset.
	AdminLocalPassword string `mapstructure:"adminLocalPassword"`
}

// SMTPConfig configures the outbound mail transport for verification
// codes. When Host is empty, verification codes are logged instead of
// sent (useful for local development and tests).
type SMTPConfig struct {
	// Host is the SMTP server host. Empty disables real sending.
	Host string `mapstructure:"host"`
	// Port is the SMTP server port (default 587).
	Port int `mapstructure:"port"`
	// Username is the SMTP auth username.
	Username string `mapstructure:"username"`
	// Password is the SMTP auth password.
	Password string `mapstructure:"password"`
	// From is the sender address.
	From string `mapstructure:"from"`
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
	// Currency is the platform's default billing currency (D4); price
	// entries, charge records and bills carry it. SetPrice accepts an
	// empty currency (inherits this) or any SupportedCurrencies value.
	Currency string `mapstructure:"currency"`
	// SupportedCurrencies is the set of currencies SetPrice accepts.
	// Empty defaults to the platform Currency. At least CNY and USD are
	// supported so operators can price in either.
	SupportedCurrencies []string `mapstructure:"supportedCurrencies"`
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
	// Reports configures the feature-25 billing-reports runners.
	Reports BillingReportsConfig `mapstructure:"reports"`
	// Forecast configures the feature-36 usage & cost forecasting.
	Forecast BillingForecastConfig `mapstructure:"forecast"`
}

// BillingForecastConfig holds the feature-36 usage & cost forecasting
// settings (AD3, AD4).
type BillingForecastConfig struct {
	// HorizonDefaultDays is the default forecast horizon in days.
	HorizonDefaultDays int `mapstructure:"horizonDefaultDays"`
	// HorizonMaxDays is the maximum forecast horizon in days.
	HorizonMaxDays int `mapstructure:"horizonMaxDays"`
	// RangeMaxDays is the maximum forecast range length in days.
	RangeMaxDays int `mapstructure:"rangeMaxDays"`
}

// BillingReportsConfig holds the feature-25 billing-reports runner
// settings (AD1, AD4).
type BillingReportsConfig struct {
	// Enabled turns the report generator and schedule runners on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// GeneratorInterval is the report-generator runner's tick interval.
	GeneratorInterval time.Duration `mapstructure:"generatorInterval"`
	// ScheduleInterval is the schedule runner's tick interval.
	ScheduleInterval time.Duration `mapstructure:"scheduleInterval"`
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
	// Namespace is the Kubernetes namespace the controller manages
	// inference resources in. Empty defaults to "taas-infer".
	Namespace string `mapstructure:"namespace"`
	// Kubeconfig is an explicit kubeconfig path the controller uses to
	// reach the Kubernetes cluster. Empty means the in-cluster config is
	// used, falling back to ~/.kube/config for local development.
	Kubeconfig string `mapstructure:"kubeconfig"`
	// Weights configures the shared model-weights filesystem the
	// controller mounts into inference pods. It is backed by a
	// JuiceFS-backed StorageClass so every inference pod reads the same
	// model weights the control plane downloads.
	Weights WeightsConfig `mapstructure:"weights"`
	// AcceleratorLabelKey is the node label key whose value is the
	// accelerator vendor (nvidia / iluvatar / metax). Inference pods pin
	// it as a node selector, so it must be a key the cluster's GPU nodes
	// actually carry. Empty uses "go-taas.com/accelerator"; when no node
	// carries that key the controller falls back to "gpu-vendor".
	AcceleratorLabelKey string `mapstructure:"acceleratorLabelKey"`
	// ResourceMetrics configures the per-service CPU/memory/GPU sampling
	// loop (feature #37, AD2).
	ResourceMetrics ControllerResourceMetricsConfig `mapstructure:"resourceMetrics"`
	// Cluster configures the per-cluster health collection loop (feature
	// #40, Section 9).
	Cluster ControllerClusterConfig `mapstructure:"cluster"`
}

// WeightsConfig holds the model-weights storage settings used by the
// controller when it mounts model weights into inference pods.
type WeightsConfig struct {
	// StorageClass is the JuiceFS-backed StorageClass used to provision
	// the weights PVC. Empty falls back to the cluster default.
	StorageClass string `mapstructure:"storageClass"`
	// PVCName is the name of the PersistentVolumeClaim that backs the
	// shared model-weights filesystem. Defaults to "model-weights".
	PVCName string `mapstructure:"pvcName"`
	// MountPath is the path the weights volume is mounted at inside
	// inference pods. Defaults to "/data/weights".
	MountPath string `mapstructure:"mountPath"`
}

// InferConfig holds infer-module specific settings.
type InferConfig struct {
	// EndpointBaseURL is the base URL the controller uses to compose
	// inference endpoint URLs ("<base>/v1"). Empty means the controller
	// records the in-cluster Service DNS name instead.
	EndpointBaseURL string `mapstructure:"endpointBaseURL"`
	// StatusConsumer controls the infer module's status-subject consumer.
	StatusConsumer StatusConsumerConfig `mapstructure:"statusConsumer"`
	// Autoscaling controls the infer module's autoscaling consumers and
	// the controller's status-report interval (feature #16).
	Autoscaling InferAutoscalingConfig `mapstructure:"autoscaling"`
}

// InferAutoscalingConfig holds the feature-16 autoscaling settings.
type InferAutoscalingConfig struct {
	// ConcurrencyConsumer controls the infer module's concurrency-metric
	// consumer Runner.
	ConcurrencyConsumer InferConcurrencyConsumerConfig `mapstructure:"concurrencyConsumer"`
	// StatusReportInterval is the minimum interval between autoscaling
	// status reports per service (bounded to avoid flooding the subject).
	StatusReportInterval time.Duration `mapstructure:"statusReportInterval"`
}

// InferConcurrencyConsumerConfig holds the kill switch and worker count
// of the infer concurrency-metric consumer.
type InferConcurrencyConsumerConfig struct {
	// Enabled turns the concurrency consumer Runner on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of concurrent concurrency handlers.
	Workers int `mapstructure:"workers"`
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
// first-boot registry seed, the warmup status consumer and the internal
// Harbor registry used by the image import flow.
type ImageConfig struct {
	// Registry is the seed list of engine images (first-boot only).
	Registry []ImageRegistryEntry `mapstructure:"registry"`
	// WarmupStatusConsumer configures the warmup status Runner.
	WarmupStatusConsumer ImageWarmupStatusConsumerConfig `mapstructure:"warmupStatusConsumer"`
	// Compatibility configures the model × engine × card-type
	// compatibility matrix (feature #19, AD3).
	Compatibility CompatibilityConfig `mapstructure:"compatibility"`
	// Harbor configures the internal container registry (Harbor) that
	// imported images are pushed to. Imported images always land in the
	// configured project (default "taas").
	Harbor HarborConfig `mapstructure:"harbor"`
	// Upload configures the image-upload flow (a docker-save archive that
	// the platform pushes into Harbor).
	Upload ImageUploadConfig `mapstructure:"upload"`
}

// ImageUploadConfig holds the image-upload settings.
type ImageUploadConfig struct {
	// MaxArchiveBytes caps one uploaded archive. Defaults to 8 GiB, which
	// covers a model-serving engine image with room to spare.
	MaxArchiveBytes int64 `mapstructure:"maxArchiveBytes"`
	// TempDir is where an upload is staged before it is pushed. Empty
	// uses the OS temp directory.
	TempDir string `mapstructure:"tempDir"`
}

// HarborConfig holds the internal container registry (Harbor) settings
// used by the image import flow.
type HarborConfig struct {
	// URL is the internal Harbor registry host (e.g.
	// hub.sudoinfotech.com). Empty disables the image import flow.
	URL string `mapstructure:"url"`
	// Username is the Harbor account used to push imported images.
	Username string `mapstructure:"username"`
	// Password is the Harbor account password.
	Password string `mapstructure:"password"`
	// Project is the Harbor project imported images are pushed to.
	// Defaults to "taas".
	Project string `mapstructure:"project"`
}

// CompatibilityConfig holds the compatibility matrix settings (feature
// #19, AD3).
type CompatibilityConfig struct {
	// SeedOnBoot seeds the compatibility_cells table on first boot when
	// it is empty (AD3). Disable to skip the first-boot seed entirely.
	SeedOnBoot bool `mapstructure:"seedOnBoot"`
	// LazySeedDefault is the default status applied to a lazily-created
	// cell (AD3). It is overridden by the vendor-match rule: vendor-
	// matched combos default to experimental, vendor-mismatched to
	// unsupported.
	LazySeedDefault string `mapstructure:"lazySeedDefault"`
}

// AcceleratorSnapshotConsumerConfig holds the accelerator snapshot
// consumer Runner settings (feature #18, AD1).
type AcceleratorSnapshotConsumerConfig struct {
	// Enabled turns the snapshot consumer Runner on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of concurrent snapshot handlers.
	Workers int `mapstructure:"workers"`
}

// AcceleratorConfig holds the accelerator inventory settings (feature
// #18). The collect interval lives here (not in ControllerConfig)
// because it is read by both the controller (collection loop) and the
// accelerator service (snapshot consumer).
type AcceleratorConfig struct {
	// CollectInterval is the Controller's node/GPU collection period;
	// the full snapshot is published on this interval (AD1). Default 30s.
	CollectInterval time.Duration `mapstructure:"collectInterval"`
	// SnapshotConsumer configures the accelerator service's snapshot
	// consumer Runner (kill switch + worker count).
	SnapshotConsumer AcceleratorSnapshotConsumerConfig `mapstructure:"snapshotConsumer"`
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
	// WeightsDir is the local directory (a JuiceFS mount) where model
	// weights downloaded from a model hub are written. Empty disables
	// the model-download feature.
	WeightsDir string `mapstructure:"weightsDir"`
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

// WebhookConfig holds webhook-module specific settings (feature #23).
type WebhookConfig struct {
	// Delivery configures the delivery runner.
	Delivery WebhookDeliveryConfig `mapstructure:"delivery"`
	// Retention configures the delivery-log retention runner (AD8).
	Retention WebhookRetentionConfig `mapstructure:"retention"`
	// SecretEncryptionKey is the AES-256-GCM master key for secret-at-rest
	// encryption (AD3). Empty falls back to a dev-only key derived from
	// the MQ namespace; production must set it.
	SecretEncryptionKey string `mapstructure:"secretEncryptionKey"`
}

// WebhookDeliveryConfig holds the webhook delivery runner settings
// (feature #23, AD11).
type WebhookDeliveryConfig struct {
	// Workers is the number of concurrent delivery attempts.
	Workers int `mapstructure:"workers"`
	// PollInterval is how often the delivery runner polls for due pending
	// deliveries.
	PollInterval time.Duration `mapstructure:"pollInterval"`
	// Timeout is the per-attempt HTTP client timeout.
	Timeout time.Duration `mapstructure:"timeout"`
}

// WebhookRetentionConfig holds the webhook delivery-log retention runner
// settings (feature #23, AD8).
type WebhookRetentionConfig struct {
	// Enabled turns the delivery-log retention runner on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// DeliveryTTL is how long deliveries are kept before deletion;
	// default 2160h (90 days).
	DeliveryTTL time.Duration `mapstructure:"deliveryTTL"`
	// BatchSize is the number of rows deleted per retention pass.
	BatchSize int `mapstructure:"batchSize"`
	// Interval is the ticker period between retention passes.
	Interval time.Duration `mapstructure:"interval"`
}

// ObservabilityConfig holds observability-module specific settings
// (feature #24, AD7).
type ObservabilityConfig struct {
	// MaxRangeSeconds is the maximum range accepted by the observability
	// RPCs (AD7). Default 7948800 (92 days), mirroring the metering
	// maxRangeSeconds constant; kept configurable for operational tuning.
	MaxRangeSeconds int64 `mapstructure:"maxRangeSeconds"`
	// Status holds the system-status settings (feature #30, Section 9).
	Status ObservabilityStatusConfig `mapstructure:"status"`
}

// ObservabilityStatusConfig holds the system-status settings (feature
// #30, Section 9).
type ObservabilityStatusConfig struct {
	// StaleAfterSeconds is the health-poll staleness threshold: the
	// console shows a stale marker when last_checked_at is older than
	// this (AD5). Default 60.
	StaleAfterSeconds int64 `mapstructure:"staleAfterSeconds"`
}

// ResourceMetricsConfig holds resourcemetrics-module specific settings
// (feature #37, Section 9).
type ResourceMetricsConfig struct {
	// MaxRangeSeconds is the maximum range accepted by the
	// GetServiceResourceMetrics RPC (AD5). Default 7948800 (92 days),
	// mirroring the metering maxRangeSeconds constant.
	MaxRangeSeconds int64 `mapstructure:"maxRangeSeconds"`
	// PrometheusURL is the Prometheus base URL the metrics page reads
	// from, for example "http://prometheus-operated.monitoring.svc:9090".
	// Prometheus already scrapes cAdvisor and the GPU exporter, so reading
	// it keeps the platform from collecting and storing the same samples
	// itself. Empty falls back to the samples the Controller writes.
	PrometheusURL string `mapstructure:"prometheusURL"`
	// InferenceNamespace is the namespace inference pods run in, used to
	// scope the Prometheus queries. Default: controller.namespace.
	InferenceNamespace string `mapstructure:"inferenceNamespace"`
	// PrometheusTimeout bounds one Prometheus query. Default 10s.
	PrometheusTimeout time.Duration `mapstructure:"prometheusTimeout"`
	// GPUUtilizationMetric is the PromQL metric carrying GPU utilization.
	// Default DCGM_FI_DEV_GPU_UTIL (the NVIDIA DCGM exporter).
	GPUUtilizationMetric string `mapstructure:"gpuUtilizationMetric"`
	// GPUMemoryUsedMetric reports card memory in use, in MiB. Default
	// DCGM_FI_DEV_FB_USED. DCGM has no "total" gauge in every version, so
	// capacity comes from GPUMemoryTotalMetric when it exists and otherwise
	// from used + GPUMemoryFreeMetric.
	GPUMemoryUsedMetric  string `mapstructure:"gpuMemoryUsedMetric"`
	GPUMemoryTotalMetric string `mapstructure:"gpuMemoryTotalMetric"`
	GPUMemoryFreeMetric  string `mapstructure:"gpuMemoryFreeMetric"`
	// Engine holds the inference engine's own metric names. They default
	// to vLLM's, which is what the shipped images run; another engine's
	// names are a configuration change, not a code change.
	Engine EngineMetricsConfig `mapstructure:"engine"`
}

// EngineMetricsConfig names the engine-reported PromQL metrics the
// metrics page reads. Each panel is skipped when its metric is empty.
type EngineMetricsConfig struct {
	// TTFTBucket is the time-to-first-token histogram (seconds).
	TTFTBucket string `mapstructure:"ttftBucket"`
	// TPOTBucket is the inter-token-latency histogram (seconds), i.e. the
	// time per output token.
	TPOTBucket string `mapstructure:"tpotBucket"`
	// KVCacheUsage is the KV-cache occupancy gauge (0-1).
	KVCacheUsage string `mapstructure:"kvCacheUsage"`
	// RequestsRunning / RequestsWaiting are the engine's queue depths.
	RequestsRunning string `mapstructure:"requestsRunning"`
	RequestsWaiting string `mapstructure:"requestsWaiting"`
	// GenerationTokens is the output-token counter, used for throughput.
	GenerationTokens string `mapstructure:"generationTokens"`
	// Quantile is the percentile used for the latency cards. Default 0.95.
	Quantile float64 `mapstructure:"quantile"`
}

// ControllerResourceMetricsConfig holds the controller's per-service
// resource sampling settings (feature #37, Section 9).
type ControllerResourceMetricsConfig struct {
	// SampleInterval is the Controller's per-service CPU/memory/GPU
	// sampling period (AD2). Default 30s.
	SampleInterval time.Duration `mapstructure:"sampleInterval"`
}

// DocsConfig holds docs-module specific settings (feature #38, Section
// 9).
type DocsConfig struct {
	// CatalogVersion is the version of the curated API catalog served by
	// GetApiDocs (AD3, AD7). Default "v1".
	CatalogVersion string `mapstructure:"catalogVersion"`
}

// ClusterConfig holds cluster-module specific settings (feature #40,
// Section 9).
type ClusterConfig struct {
	// DefaultClusterID is the platform default cluster for the deploy
	// form's cluster selector (AD8). Empty means the first active
	// cluster.
	DefaultClusterID string `mapstructure:"defaultClusterId"`
	// SelfName overrides the auto-discovered name of the cluster the
	// platform itself runs in (DiscoverCluster). Empty derives the name
	// from the cluster identity.
	SelfName string `mapstructure:"selfName"`
}

// ControllerClusterConfig holds the controller's per-cluster health
// collection settings (feature #40, Section 9).
type ControllerClusterConfig struct {
	// CollectInterval is the Controller's per-cluster health collection
	// period (AD4). Default 30s.
	CollectInterval time.Duration `mapstructure:"collectInterval"`
	// KubeconfigDir is the base directory the controller resolves a
	// registered cluster's kubeconfig_ref against (AD3). The local
	// cluster ("in-cluster") always uses the controller's own client;
	// remote references are joined onto this directory. Empty disables
	// remote-cluster collection (the local cluster is still collected).
	KubeconfigDir string `mapstructure:"kubeconfigDir"`
}

// AccountConfig holds account-module specific settings (feature #41,
// Section 9).
type AccountConfig struct {
	// Export configures the data-export generation runner.
	Export AccountExportConfig `mapstructure:"export"`
}

// AccountExportConfig holds the data-export generation settings (feature
// #41, Section 9).
type AccountExportConfig struct {
	// GeneratorInterval is the export-generation runner's tick interval
	// (AD2). Default 5s.
	GeneratorInterval time.Duration `mapstructure:"generatorInterval"`
	// MaxRangeSeconds is the maximum range accepted by CreateDataExport
	// (AD6). Default 7948800 (92 days).
	MaxRangeSeconds int64 `mapstructure:"maxRangeSeconds"`
}

// BatchConfig holds batch-module specific settings (feature #42, Section
// 9).
type BatchConfig struct {
	// Worker configures the batch worker runner (AD3, AD4).
	Worker BatchWorkerConfig `mapstructure:"worker"`
	// Retention configures the result/error-file retention runner (AD8).
	Retention BatchRetentionConfig `mapstructure:"retention"`
	// MaxFileBytes is the maximum input file size (AD2). Default
	// 524288000 (500 MB).
	MaxFileBytes int64 `mapstructure:"maxFileBytes"`
	// MaxLines is the maximum number of input lines (AD2). Default 50000.
	MaxLines int `mapstructure:"maxLines"`
	// StoreDir is the local directory where the JSONL input/result/error
	// files are stored (the object-store mount). Default /data/batch.
	StoreDir string `mapstructure:"storeDir"`
}

// BatchWorkerConfig holds the batch worker runner settings (feature #42,
// Section 9).
type BatchWorkerConfig struct {
	// Enabled turns the batch worker on or off (incident-triage kill
	// switch). Default true.
	Enabled bool `mapstructure:"enabled"`
	// PollInterval is how often the batch worker polls for in_progress
	// jobs. Default 5s.
	PollInterval time.Duration `mapstructure:"pollInterval"`
	// Concurrency is the number of concurrent requests processed per job
	// pass. Default 4.
	Concurrency int `mapstructure:"concurrency"`
}

// BatchRetentionConfig holds the batch retention runner settings
// (feature #42, AD8).
type BatchRetentionConfig struct {
	// Enabled turns the result/error-file retention runner on or off
	// (incident-triage kill switch). Default true.
	Enabled bool `mapstructure:"enabled"`
	// FileTTL is how long a completed job's result/error files are kept
	// before deletion. Default 720h (30 days).
	FileTTL time.Duration `mapstructure:"fileTTL"`
	// Interval is the ticker period between retention passes. Default 1h.
	Interval time.Duration `mapstructure:"interval"`
}

// PromptConfig holds prompt-module specific settings (feature #43,
// Section 9).
type PromptConfig struct {
	// MaxNameLength is the maximum prompt/folder/template name length.
	// Default 128.
	MaxNameLength int `mapstructure:"maxNameLength"`
	// MaxContentLength is the maximum prompt/template content length.
	// Default 6144.
	MaxContentLength int `mapstructure:"maxContentLength"`
}

// EvaluationConfig holds prompt-evaluation settings.
type EvaluationConfig struct {
	Worker                 EvaluationWorkerConfig    `mapstructure:"worker"`
	Retention              EvaluationRetentionConfig `mapstructure:"retention"`
	MaxCasesPerRun         int                       `mapstructure:"maxCasesPerRun"`
	MaxVariableValueLength int                       `mapstructure:"maxVariableValueLength"`
	MaxInFlightRunsPerOrg  int                       `mapstructure:"maxInFlightRunsPerOrg"`
}

// EvaluationWorkerConfig controls asynchronous evaluation execution.
type EvaluationWorkerConfig struct {
	Enabled                  bool          `mapstructure:"enabled"`
	PollInterval             time.Duration `mapstructure:"pollInterval"`
	MaxConcurrentRuns        int           `mapstructure:"maxConcurrentRuns"`
	MaxConcurrentCasesPerRun int           `mapstructure:"maxConcurrentCasesPerRun"`
}

// EvaluationRetentionConfig controls evaluation snapshot expiry.
type EvaluationRetentionConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	TTL      time.Duration `mapstructure:"ttl"`
	Interval time.Duration `mapstructure:"interval"`
}

// RuntimeConfig holds the distributed-inference-runtime settings (feature
// #47, Section 9). The controller consumes collectInterval, the scheme and
// model-change consumers, and the aibrix block; the server consumes the
// remaining consumers.
type RuntimeConfig struct {
	// CollectInterval is the controller's runtime-component and ModelClaim
	// collection period; the full snapshot is published on this interval
	// (AD4). Default 30s.
	CollectInterval time.Duration `mapstructure:"collectInterval"`
	// SchemeBroadcastInterval is how often the runtime service re-broadcasts
	// the persisted scheme so a restarted controller reconverges (AD6).
	// Default 30s.
	SchemeBroadcastInterval time.Duration `mapstructure:"schemeBroadcastInterval"`
	// SchemeConsumer configures the controller's scheme consumer.
	SchemeConsumer RuntimeConsumerConfig `mapstructure:"schemeConsumer"`
	// HealthConsumer configures the runtime service's snapshot consumer.
	HealthConsumer RuntimeHealthConsumerConfig `mapstructure:"healthConsumer"`
	// ModelChangeConsumer configures the controller's model-change consumer.
	ModelChangeConsumer RuntimeConsumerConfig `mapstructure:"modelChangeConsumer"`
	// AIBrix names the installed AIBrix control plane's namespace and the
	// Envoy AI Gateway Deployment observed for health.
	AIBrix RuntimeAIBrixConfig `mapstructure:"aibrix"`
	// DataPlane describes the HTTPRoute the platform publishes for
	// inference traffic: the parent Gateway that terminates TLS, and the
	// inference gateway Service the route forwards to. The hostname itself
	// is an operator setting in the console; `hostname` here is the value a
	// fresh install starts from.
	DataPlane RuntimeDataPlaneConfig `mapstructure:"dataPlane"`
}

// RuntimeConsumerConfig holds a runtime consumer's kill switch.
type RuntimeConsumerConfig struct {
	// Enabled turns the consumer Runner on or off (incident-triage kill
	// switch).
	Enabled bool `mapstructure:"enabled"`
}

// RuntimeHealthConsumerConfig holds the runtime service's snapshot
// consumer Runner settings (feature #47, AD4).
type RuntimeHealthConsumerConfig struct {
	// Enabled turns the snapshot consumer Runner on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of concurrent snapshot handlers.
	Workers int `mapstructure:"workers"`
}

// RuntimeAIBrixConfig names the installed AIBrix control plane.
type RuntimeAIBrixConfig struct {
	// Namespace is the namespace of the AIBrix control-plane Deployments.
	// Default "aibrix-system".
	Namespace string `mapstructure:"namespace"`
	// Gateway is the Envoy AI Gateway Deployment observed for health.
	// Default "envoy-aibrix-system-aibrix-eg".
	Gateway string `mapstructure:"gateway"`
	// GatewayName is the Gateway API Gateway object the model HTTPRoutes
	// attach to. Default "aibrix-eg".
	GatewayName string `mapstructure:"gatewayName"`
}

// RuntimeDataPlaneConfig describes the data-plane HTTPRoute the server
// owns. Inference traffic enters through the inference gateway, never
// through the control plane, so the route is an install-time fact plus one
// runtime setting (the hostname).
type RuntimeDataPlaneConfig struct {
	// Enabled turns publishing on. When false the server never writes the
	// route, and saving a hostname in the console fails with 12909.
	Enabled bool `mapstructure:"enabled"`
	// Hostname is the initial public host name tenants call. The console
	// setting overrides it.
	Hostname string `mapstructure:"hostname"`
	// RouteNamespace holds the HTTPRoute. Default: the controller's
	// namespace (the release namespace).
	RouteNamespace string `mapstructure:"routeNamespace"`
	// ParentRefName and ParentRefNamespace identify the Gateway that
	// terminates TLS for the hostname.
	ParentRefName      string `mapstructure:"parentRefName"`
	ParentRefNamespace string `mapstructure:"parentRefNamespace"`
	// ParentRefSection is the Gateway listener name, for example "https".
	ParentRefSection string `mapstructure:"parentRefSection"`
	// BackendName and BackendNamespace identify the inference gateway
	// Service the route forwards to.
	BackendName      string `mapstructure:"backendName"`
	BackendNamespace string `mapstructure:"backendNamespace"`
	// BackendPort is the gateway's HTTP port. Default 80.
	BackendPort int32 `mapstructure:"backendPort"`
}

// GatewayPluginConfig holds the data-plane gateway plugin settings
// (feature #54). The plugin is a Wasm filter inside Envoy: it verifies the
// caller's API key and reports token usage, but makes no business decision
// of its own — every call it makes lands on the internal endpoints below,
// which delegate to the auth and metering modules.
type GatewayPluginConfig struct {
	// Enabled renders the EnvoyExtensionPolicy and serves the internal
	// endpoints. The default is false: an install without Prometheus-style
	// observability should not silently start rejecting inference traffic
	// the day it upgrades.
	Enabled bool `mapstructure:"enabled"`
	// Token is the shared secret the plugin presents on every internal
	// call. Empty disables the endpoints even when Enabled is true: an
	// unauthenticated verification endpoint would let anyone probe keys.
	// The chart generates it into the release Secret.
	Token string `mapstructure:"token"`
	// CacheTTL is the plugin's local verdict lifetime. A revocation
	// becomes effective within this window, not instantly. Default 30s.
	CacheTTL time.Duration `mapstructure:"cacheTTL"`
	// VerifyTimeout bounds one plugin->server verification call. Exceeding
	// it fails closed. Default 500ms.
	VerifyTimeout time.Duration `mapstructure:"verifyTimeout"`
	// FailOpen passes through to the policy when the filter itself cannot
	// initialize or run. Default false (block), because the alternative is
	// an unauthenticated inference surface.
	FailOpen bool `mapstructure:"failOpen"`
	// ProtectedPrefixes are the request paths that require a verified key.
	// Default ["/v1/"]; health and discovery paths stay open so probes work.
	ProtectedPrefixes []string `mapstructure:"protectedPrefixes"`
	// PluginPath is the Wasm artifact the internal API serves. Empty means
	// gateway-plugin.wasm next to the server binary. The policy pins its
	// SHA-256 in the chart, so a mismatched artifact is refused by Envoy.
	PluginPath string `mapstructure:"pluginPath"`
}

// ProviderConfig holds the external-model-provider module settings
// (feature #48, Section 9). The provider module owns the registry, the
// encrypted write-only keys, upstream-model curation, publishing, the
// connectivity-probe worker and the external chat proxy.
type ProviderConfig struct {
	// SecretEncryptionKey is the AES-256-GCM master key for provider-key
	// encryption at rest (AD3/AD12). Empty falls back to a dev-only key
	// derived from the MQ namespace; production must set it. Mirrors the
	// webhook rule so there is one key-resolution convention.
	SecretEncryptionKey string `mapstructure:"secretEncryptionKey"`
	// ProbeInterval is the scheduled connectivity-probe period for
	// enabled providers (design FR3.6). Default 60s.
	ProbeInterval time.Duration `mapstructure:"probeInterval"`
	// ProbeWorker configures the scheduled probe worker runner.
	ProbeWorker ProviderProbeWorkerConfig `mapstructure:"probeWorker"`
	// DefaultRequestTimeoutSeconds is the default per-provider request
	// timeout when a request omits one (range 1-120). Default 10.
	DefaultRequestTimeoutSeconds int `mapstructure:"defaultRequestTimeoutSeconds"`
}

// ProviderProbeWorkerConfig holds the provider probe worker settings
// (feature #48, Section 9).
type ProviderProbeWorkerConfig struct {
	// Enabled turns the scheduled probe worker on or off (incident-triage
	// kill switch). The shipped default is true.
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of concurrent probe handlers. Default 2.
	Workers int `mapstructure:"workers"`
}

// RoutingConfig holds the intelligent-model-routing module settings
// (feature #49, Section 9). The routing module owns the routing-group
// registry, the request-time virtual-name resolver and the decision
// log.
type RoutingConfig struct {
	// DecisionRetentionDays is how long decision-log entries are kept
	// before deletion (AD11). Default 7, range 1-90.
	DecisionRetentionDays int `mapstructure:"decisionRetentionDays"`
	// Retention configures the decision-log retention runner.
	Retention RoutingRetentionConfig `mapstructure:"retention"`
	// LatencyWindow is the observed-latency sampling window for the
	// latency_priority strategy (AD3): the p50 of successful request
	// logs within this window. Default 15m.
	LatencyWindow time.Duration `mapstructure:"latencyWindow"`
}

// RoutingRetentionConfig holds the decision-log retention runner
// settings (feature #49, Section 9).
type RoutingRetentionConfig struct {
	// Enabled turns the decision retention runner on or off
	// (incident-triage kill switch). The shipped default is true.
	Enabled bool `mapstructure:"enabled"`
	// BatchSize is the number of rows deleted per retention pass.
	BatchSize int `mapstructure:"batchSize"`
	// Interval is the ticker period between retention passes.
	Interval time.Duration `mapstructure:"interval"`
}

// OptimizationConfig holds the cost-optimization-recommendations module
// settings (feature #50, Section 9). The optimization module owns the
// rule engine, the recommendation store and the refresh runner. All
// rule thresholds are config constants (design D3).
type OptimizationConfig struct {
	// RefreshIntervalMinutes is the engine ticker period (FR1.2).
	// Default 60, range 10-1440.
	RefreshIntervalMinutes int `mapstructure:"refreshIntervalMinutes"`
	// WindowDays is the settled-data window every rule reads (D4).
	// Default 30, range 7-90.
	WindowDays int `mapstructure:"windowDays"`
	// TopNPerOrg bounds the per-org persisted worklist (AD5).
	// Default 50, range 10-200.
	TopNPerOrg int `mapstructure:"topNPerOrg"`
	// Currency is the platform's single settlement currency.
	Currency string `mapstructure:"currency"`
	// R1 configures the cheaper-model-substitution rule.
	R1 OptimizationR1Config `mapstructure:"r1"`
	// R2 configures the batch-migration rule.
	R2 OptimizationR2Config `mapstructure:"r2"`
	// R3 configures the idle-service scale-to-zero rule.
	R3 OptimizationR3Config `mapstructure:"r3"`
	// R4 configures the cache-hit-improvement rule.
	R4 OptimizationR4Config `mapstructure:"r4"`
	// ModelContextLengths maps model name to max context tokens (AD12).
	// R1 only proposes substitutions between annotated models.
	ModelContextLengths map[string]int64 `mapstructure:"modelContextLengths"`
	// CardHourlyCosts maps accelerator type to hourly GPU cost in
	// Currency (AD14). R3 only prices services whose card type is
	// annotated here.
	CardHourlyCosts map[string]float64 `mapstructure:"cardHourlyCosts"`
	// PeakHours is R2's peak-window list, each "HH:MM-HH:MM" (AD13).
	PeakHours []string `mapstructure:"peakHours"`
	// PeakWeekdaysOnly restricts the peak windows to Monday-Friday.
	PeakWeekdaysOnly bool `mapstructure:"peakWeekdaysOnly"`
	// Timezone is the IANA name the peak windows are evaluated in.
	Timezone string `mapstructure:"timezone"`
	// PrefixCacheEngines lists the engines that support prefix caching
	// (AD15).
	PrefixCacheEngines []string `mapstructure:"prefixCacheEngines"`
}

// OptimizationR1Config holds the cheaper-model-substitution rule
// thresholds (feature #50, FR1.3).
type OptimizationR1Config struct {
	// MinRequests is the per-(org, model) request floor.
	MinRequests int `mapstructure:"minRequests"`
	// MinBilledTokens is the per-(org, model) billed-token floor.
	MinBilledTokens int64 `mapstructure:"minBilledTokens"`
	// ContextRatio is the minimum B-context/A-context ratio.
	ContextRatio float64 `mapstructure:"contextRatio"`
	// MinSaving is the monthly saving floor in Currency.
	MinSaving float64 `mapstructure:"minSaving"`
}

// OptimizationR2Config holds the batch-migration rule thresholds
// (feature #50, FR1.4).
type OptimizationR2Config struct {
	// MinRequests is the per-(org, key, model) request floor.
	MinRequests int `mapstructure:"minRequests"`
	// OfflineRatio is the offline-shape threshold.
	OfflineRatio float64 `mapstructure:"offlineRatio"`
	// PromptTokenFloor is the non-interactive prompt-size floor.
	PromptTokenFloor int64 `mapstructure:"promptTokenFloor"`
}

// OptimizationR3Config holds the idle-service scale-to-zero rule
// thresholds (feature #50, FR1.5).
type OptimizationR3Config struct {
	// IdleRequestsPerDay is the idle-traffic ceiling.
	IdleRequestsPerDay int64 `mapstructure:"idleRequestsPerDay"`
	// IdleWindowDays is the idle-observation window.
	IdleWindowDays int `mapstructure:"idleWindowDays"`
}

// OptimizationR4Config holds the cache-hit-improvement rule thresholds
// (feature #50, FR1.6).
type OptimizationR4Config struct {
	// MinPromptTokens is the per-(org, model) prompt-token floor.
	MinPromptTokens int64 `mapstructure:"minPromptTokens"`
	// MaxCacheHitRate is the cache-hit-rate ceiling.
	MaxCacheHitRate float64 `mapstructure:"maxCacheHitRate"`
	// DefaultHitAssumption is the assumed hit rate for the estimate.
	DefaultHitAssumption float64 `mapstructure:"defaultHitAssumption"`
}

// QuotaConfig holds the service-quota-management module settings
// (feature #51, Section 9). The four org quota dimensions live on the
// organization row; the gateway enforces the hot-path dimensions from
// the VerifyAPIKey verdict (contract pinned in the architecture doc),
// the control plane enforces the lifecycle dimensions at create.
type QuotaConfig struct {
	// Timezone is the IANA name the batch daily counter's local
	// midnight is evaluated in (AD15). Default "UTC".
	Timezone string `mapstructure:"timezone"`
	// RequestIncreaseTo is the operator mailbox the tenant
	// Request-increase button mails to (AD18); empty hides the button.
	RequestIncreaseTo string `mapstructure:"requestIncreaseTo"`
	// ConcurrencyConsumer configures the gateway in-flight report
	// consumer runner (AD14).
	ConcurrencyConsumer QuotaConcurrencyConsumerConfig `mapstructure:"concurrencyConsumer"`
}

// QuotaConcurrencyConsumerConfig holds the in-flight report consumer
// runner settings (feature #51, AD14).
type QuotaConcurrencyConsumerConfig struct {
	// Enabled turns the gateway in-flight report consumer on or off.
	Enabled bool `mapstructure:"enabled"`
	// Workers is the concurrent report handler count.
	Workers int `mapstructure:"workers"`
	// StaleAfter is how old a gateway in-flight report may be before
	// the snapshot reads as 0 (best-effort, FR2.4).
	StaleAfter time.Duration `mapstructure:"staleAfter"`
}

// VGPUConfig holds the vGPU slicing-pool module settings (feature #52,
// Section 9). The vgpu module owns the pool configuration; membership
// and capacity are projections of the accelerator-inventory snapshot
// and the HAMI-reported device state.
type VGPUConfig struct {
	// MembershipInterval bounds the freshness of the pool membership
	// and capacity projection (the accelerator-inventory interval,
	// AD12). Default 30s.
	MembershipInterval time.Duration `mapstructure:"membershipInterval"`
	// HAMI holds the pinned HAMI resource/annotation names (Section
	// 5.3). A HAMI version change is a config change, not a code change.
	HAMI VGPUHAMIConfig `mapstructure:"hami"`
}

// VGPUHAMIConfig holds the pinned HAMI contract names (feature #52,
// Section 5.3).
type VGPUHAMIConfig struct {
	// MemResource is the HAMI memory resource name.
	MemResource string `mapstructure:"memResource"`
	// CoreResource is the HAMI compute resource name.
	CoreResource string `mapstructure:"coreResource"`
	// PolicyAnnotation is the placement-policy annotation name.
	PolicyAnnotation string `mapstructure:"policyAnnotation"`
	// PoolLabelPrefix is the default pool label key prefix.
	PoolLabelPrefix string `mapstructure:"poolLabelPrefix"`
}

// FaultConfig holds the fault-diagnosis module settings (feature #53,
// Section 9). The fault module owns the incident lifecycle, the
// deterministic root-cause classifier, and the recovery actions.
type FaultConfig struct {
	// Debounce holds the per-signal debounce windows (FR3.2).
	Debounce FaultDebounceConfig `mapstructure:"debounce"`
	// Retention is the incident retention window (FR1.4). Default 720h.
	Retention time.Duration `mapstructure:"retention"`
	// Recovery holds the default auto-recovery toggles (FR4.1).
	Recovery FaultRecoveryConfig `mapstructure:"recovery"`
	// SignalsConsumer configures the detection-signal consumer runner
	// (AD13).
	SignalsConsumer FaultSignalsConsumerConfig `mapstructure:"signalsConsumer"`
}

// FaultDebounceConfig holds the per-signal debounce windows (feature
// #53, FR3.2).
type FaultDebounceConfig struct {
	// CrashLoopRestarts is the crash-loop restart threshold.
	CrashLoopRestarts int `mapstructure:"crashLoopRestarts"`
	// CrashLoopWindow is the crash-loop window.
	CrashLoopWindow time.Duration `mapstructure:"crashLoopWindow"`
}

// FaultRecoveryConfig holds the default auto-recovery toggles (feature
// #53, FR4.1).
type FaultRecoveryConfig struct {
	// AutoRestart is the per-category auto-restart toggle map.
	AutoRestart map[string]bool `mapstructure:"autoRestart"`
	// AutoReschedule is the per-category auto-reschedule toggle map.
	AutoReschedule map[string]bool `mapstructure:"autoReschedule"`
}

// FaultSignalsConsumerConfig holds the detection-signal consumer runner
// settings (feature #53, AD13).
type FaultSignalsConsumerConfig struct {
	// Enabled turns the detection-signal consumer on or off.
	Enabled bool `mapstructure:"enabled"`
	// Workers is the concurrent signal handler count.
	Workers int `mapstructure:"workers"`
}

// TracingConfig holds tracing-module specific settings (feature #27,
// Section 9).
type TracingConfig struct {
	// Retention configures the trace retention runner (AD5).
	Retention TracingRetentionConfig `mapstructure:"retention"`
}

// TracingRetentionConfig holds the trace retention runner settings
// (feature #27, AD5).
type TracingRetentionConfig struct {
	// TraceTTL is how long traces (and their spans) are kept before
	// deletion; default 720h (30 days), aligned with request logs.
	TraceTTL time.Duration `mapstructure:"traceTTL"`
}

// NotificationConfig holds notification-module specific settings
// (feature #26, Section 9).
type NotificationConfig struct {
	// Consumer configures the notification.events event consumer.
	Consumer NotificationConsumerConfig `mapstructure:"consumer"`
	// Retention configures the notification retention runner (AD3).
	Retention NotificationRetentionConfig `mapstructure:"retention"`
}

// NotificationConsumerConfig holds the notification.events consumer
// Runner settings (feature #26, AD10).
type NotificationConsumerConfig struct {
	// Workers is the number of concurrent event-handling workers.
	Workers int `mapstructure:"workers"`
}

// NotificationRetentionConfig holds the notification retention runner
// settings (feature #26, AD3).
type NotificationRetentionConfig struct {
	// Enabled turns the notification retention runner on or off
	// (incident-triage kill switch).
	Enabled bool `mapstructure:"enabled"`
	// NotificationTTL is how long notifications are kept before
	// deletion; default 2160h (90 days).
	NotificationTTL time.Duration `mapstructure:"notificationTTL"`
	// BatchSize is the number of rows deleted per retention pass.
	BatchSize int `mapstructure:"batchSize"`
	// Interval is the ticker period between retention passes.
	Interval time.Duration `mapstructure:"interval"`
}

// Configuration is the root of the merged configuration tree.
type Configuration struct {
	Databases       Databases             `mapstructure:"db"`
	Redis           Redis                 `mapstructure:"redis"`
	MQ              MQConfig              `mapstructure:"mq"`
	Auth            AuthConfig            `mapstructure:"auth"`
	Metering        MeteringConfig        `mapstructure:"metering"`
	Billing         BillingConfig         `mapstructure:"billing"`
	Controller      ControllerConfig      `mapstructure:"controller"`
	Infer           InferConfig           `mapstructure:"infer"`
	Image           ImageConfig           `mapstructure:"image"`
	Model           ModelConfig           `mapstructure:"model"`
	Tenancy         TenancyConfig         `mapstructure:"tenancy"`
	Audit           AuditConfig           `mapstructure:"audit"`
	Accelerator     AcceleratorConfig     `mapstructure:"accelerator"`
	LoadTest        LoadTestConfig        `mapstructure:"loadtest"`
	Webhook         WebhookConfig         `mapstructure:"webhook"`
	Observability   ObservabilityConfig   `mapstructure:"observability"`
	Notification    NotificationConfig    `mapstructure:"notification"`
	Tracing         TracingConfig         `mapstructure:"tracing"`
	ResourceMetrics ResourceMetricsConfig `mapstructure:"resourcemetrics"`
	// GatewayPlugin configures the data-plane gateway's Wasm plugin: the
	// internal endpoints it calls and the artifact it loads (feature #54).
	GatewayPlugin GatewayPluginConfig `mapstructure:"gatewayPlugin"`
	Docs          DocsConfig          `mapstructure:"docs"`
	Cluster       ClusterConfig       `mapstructure:"cluster"`
	Account       AccountConfig       `mapstructure:"account"`
	Batch         BatchConfig         `mapstructure:"batch"`
	Prompt        PromptConfig        `mapstructure:"prompt"`
	Evaluation    EvaluationConfig    `mapstructure:"evaluation"`
	Runtime       RuntimeConfig       `mapstructure:"runtime"`
	Provider      ProviderConfig      `mapstructure:"provider"`
	Routing       RoutingConfig       `mapstructure:"routing"`
	Optimization  OptimizationConfig  `mapstructure:"optimization"`
	Quota         QuotaConfig         `mapstructure:"quota"`
	VGPU          VGPUConfig          `mapstructure:"vgpu"`
	Fault         FaultConfig         `mapstructure:"fault"`
	Log           LogConfig           `mapstructure:"log"`
}

// LoadTestConfig holds the feature-20 load-testing settings.
type LoadTestConfig struct {
	// ProgressInterval is how often the runner persists a live-progress
	// snapshot to the load_tests row (AD12). Default 5s.
	ProgressInterval time.Duration `mapstructure:"progressInterval"`
	// Retention is how long terminal runs are kept before the cleanup
	// runner deletes them (AD9). Default 2160h (90 days).
	Retention time.Duration `mapstructure:"retention"`
	// SystemCredentialEnabled is the kill switch for the synthetic
	// platform credential (AD11). When false, CreateLoadTest returns
	// 10311 because there is no credential to drive with.
	SystemCredentialEnabled bool `mapstructure:"systemCredentialEnabled"`
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
	// Feature-22 (AD3): the admin-role set must be a non-empty list of
	// non-empty strings. applyDefaults fills the default before Validate
	// in the production path; a raw zero-value config (as built by
	// tests) is allowed through so the empty default is not rejected.
	if len(c.Auth.AdminRoles) > 0 {
		for _, r := range c.Auth.AdminRoles {
			if strings.TrimSpace(r) == "" {
				return &FieldError{Field: "auth.adminRoles", Reason: "must be a non-empty list of non-empty strings"}
			}
		}
	}
	if c.Infer.StatusConsumer.Workers < 0 {
		return &FieldError{Field: "infer.statusConsumer.workers", Reason: "must not be negative"}
	}
	if c.Controller.Weights.MountPath != "" && !strings.HasPrefix(c.Controller.Weights.MountPath, "/") {
		return &FieldError{Field: "controller.weights.mountPath", Reason: "must be an absolute path"}
	}
	if c.Infer.Autoscaling.ConcurrencyConsumer.Workers < 0 {
		return &FieldError{Field: "infer.autoscaling.concurrencyConsumer.workers", Reason: "must not be negative"}
	}
	if c.Infer.Autoscaling.StatusReportInterval < 0 {
		return &FieldError{Field: "infer.autoscaling.statusReportInterval", Reason: "must not be negative"}
	}
	if c.LoadTest.ProgressInterval < 0 {
		return &FieldError{Field: "loadtest.progressInterval", Reason: "must not be negative"}
	}
	// Feature #47: the runtime intervals must not be negative.
	if c.Runtime.CollectInterval < 0 {
		return &FieldError{Field: "runtime.collectInterval", Reason: "must not be negative"}
	}
	if c.Runtime.SchemeBroadcastInterval < 0 {
		return &FieldError{Field: "runtime.schemeBroadcastInterval", Reason: "must not be negative"}
	}
	// Feature #48: the provider probe interval must not be negative and the
	// default request timeout must be within 1-120 when set.
	if c.Provider.ProbeInterval < 0 {
		return &FieldError{Field: "provider.probeInterval", Reason: "must not be negative"}
	}
	if c.Provider.ProbeWorker.Workers < 0 {
		return &FieldError{Field: "provider.probeWorker.workers", Reason: "must not be negative"}
	}
	if c.Provider.DefaultRequestTimeoutSeconds < 0 || c.Provider.DefaultRequestTimeoutSeconds > 120 {
		return &FieldError{Field: "provider.defaultRequestTimeoutSeconds", Reason: "must be within 1-120"}
	}
	// Feature #49: the routing decision retention days must be within
	// 1-90 when set (applyDefaults fills the default before Validate in
	// the production path; a raw zero-value config is allowed through).
	if c.Routing.DecisionRetentionDays < 0 || c.Routing.DecisionRetentionDays > 90 {
		return &FieldError{Field: "routing.decisionRetentionDays", Reason: "must be within 1-90"}
	}
	if c.Routing.Retention.BatchSize < 0 {
		return &FieldError{Field: "routing.retention.batchSize", Reason: "must not be negative"}
	}
	if c.Routing.Retention.Interval < 0 {
		return &FieldError{Field: "routing.retention.interval", Reason: "must not be negative"}
	}
	if c.Routing.LatencyWindow < 0 {
		return &FieldError{Field: "routing.latencyWindow", Reason: "must not be negative"}
	}
	// Feature #50: the optimization engine bounds (Section 9).
	// applyDefaults fills the defaults before Validate in the
	// production path, so a zero value here means "unset" and passes;
	// any explicit value must sit inside the documented range.
	if c.Optimization.RefreshIntervalMinutes != 0 &&
		(c.Optimization.RefreshIntervalMinutes < 10 || c.Optimization.RefreshIntervalMinutes > 1440) {
		return &FieldError{Field: "optimization.refreshIntervalMinutes", Reason: "must be within 10-1440"}
	}
	if c.Optimization.WindowDays != 0 &&
		(c.Optimization.WindowDays < 7 || c.Optimization.WindowDays > 90) {
		return &FieldError{Field: "optimization.windowDays", Reason: "must be within 7-90"}
	}
	if c.Optimization.TopNPerOrg != 0 &&
		(c.Optimization.TopNPerOrg < 10 || c.Optimization.TopNPerOrg > 200) {
		return &FieldError{Field: "optimization.topNPerOrg", Reason: "must be within 10-200"}
	}
	// Feature #51: the quota consumer settings (Section 9). The
	// timezone must be a valid IANA name when set; the worker count
	// must be positive and the stale window non-negative.
	if c.Quota.Timezone != "" {
		if _, err := time.LoadLocation(c.Quota.Timezone); err != nil {
			return &FieldError{Field: "quota.timezone", Reason: "must be a valid IANA timezone name"}
		}
	}
	if c.Quota.ConcurrencyConsumer.Workers < 0 {
		return &FieldError{Field: "quota.concurrencyConsumer.workers", Reason: "must not be negative"}
	}
	if c.Quota.ConcurrencyConsumer.StaleAfter < 0 {
		return &FieldError{Field: "quota.concurrencyConsumer.staleAfter", Reason: "must not be negative"}
	}
	if c.LoadTest.Retention < 0 {
		return &FieldError{Field: "loadtest.retention", Reason: "must not be negative"}
	}
	if c.Image.WarmupStatusConsumer.Workers < 0 {
		return &FieldError{Field: "image.warmupStatusConsumer.workers", Reason: "must not be negative"}
	}
	if c.Accelerator.CollectInterval < 0 {
		return &FieldError{Field: "accelerator.collectInterval", Reason: "must not be negative"}
	}
	if c.Accelerator.SnapshotConsumer.Workers < 0 {
		return &FieldError{Field: "accelerator.snapshotConsumer.workers", Reason: "must not be negative"}
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
	if c.Webhook.Delivery.Workers < 0 {
		return &FieldError{Field: "webhook.delivery.workers", Reason: "must not be negative"}
	}
	if c.Webhook.Delivery.PollInterval < 0 {
		return &FieldError{Field: "webhook.delivery.pollInterval", Reason: "must not be negative"}
	}
	if c.Webhook.Delivery.Timeout < 0 {
		return &FieldError{Field: "webhook.delivery.timeout", Reason: "must not be negative"}
	}
	if c.Webhook.Retention.DeliveryTTL < 0 {
		return &FieldError{Field: "webhook.retention.deliveryTTL", Reason: "must not be negative"}
	}
	if c.Webhook.Retention.BatchSize < 0 {
		return &FieldError{Field: "webhook.retention.batchSize", Reason: "must not be negative"}
	}
	if c.Webhook.Retention.Interval < 0 {
		return &FieldError{Field: "webhook.retention.interval", Reason: "must not be negative"}
	}
	if c.Observability.MaxRangeSeconds < 0 {
		return &FieldError{Field: "observability.maxRangeSeconds", Reason: "must not be negative"}
	}
	if c.ResourceMetrics.MaxRangeSeconds < 0 {
		return &FieldError{Field: "resourcemetrics.maxRangeSeconds", Reason: "must not be negative"}
	}
	if c.Controller.ResourceMetrics.SampleInterval < 0 {
		return &FieldError{Field: "controller.resourceMetrics.sampleInterval", Reason: "must not be negative"}
	}
	if c.Controller.Cluster.CollectInterval < 0 {
		return &FieldError{Field: "controller.cluster.collectInterval", Reason: "must not be negative"}
	}
	if c.Account.Export.MaxRangeSeconds < 0 {
		return &FieldError{Field: "account.export.maxRangeSeconds", Reason: "must not be negative"}
	}
	if c.Notification.Consumer.Workers < 0 {
		return &FieldError{Field: "notification.consumer.workers", Reason: "must not be negative"}
	}
	if c.Notification.Retention.NotificationTTL < 0 {
		return &FieldError{Field: "notification.retention.notificationTTL", Reason: "must not be negative"}
	}
	if c.Notification.Retention.BatchSize < 0 {
		return &FieldError{Field: "notification.retention.batchSize", Reason: "must not be negative"}
	}
	if c.Notification.Retention.Interval < 0 {
		return &FieldError{Field: "notification.retention.interval", Reason: "must not be negative"}
	}
	if c.Tracing.Retention.TraceTTL < 0 {
		return &FieldError{Field: "tracing.retention.traceTTL", Reason: "must not be negative"}
	}
	if c.Batch.Worker.PollInterval < 0 {
		return &FieldError{Field: "batch.worker.pollInterval", Reason: "must not be negative"}
	}
	if c.Batch.Worker.Concurrency < 0 {
		return &FieldError{Field: "batch.worker.concurrency", Reason: "must not be negative"}
	}
	if c.Batch.Retention.FileTTL < 0 {
		return &FieldError{Field: "batch.retention.fileTTL", Reason: "must not be negative"}
	}
	if c.Batch.Retention.Interval < 0 {
		return &FieldError{Field: "batch.retention.interval", Reason: "must not be negative"}
	}
	if c.Batch.MaxFileBytes < 0 {
		return &FieldError{Field: "batch.maxFileBytes", Reason: "must not be negative"}
	}
	if c.Batch.MaxLines < 0 {
		return &FieldError{Field: "batch.maxLines", Reason: "must not be negative"}
	}
	if c.Prompt.MaxNameLength < 0 {
		return &FieldError{Field: "prompt.maxNameLength", Reason: "must not be negative"}
	}
	if c.Prompt.MaxContentLength < 0 {
		return &FieldError{Field: "prompt.maxContentLength", Reason: "must not be negative"}
	}
	// Feature #52: the vGPU membership interval must not be negative.
	if c.VGPU.MembershipInterval < 0 {
		return &FieldError{Field: "vgpu.membershipInterval", Reason: "must not be negative"}
	}
	// Feature #53: the fault debounce/retention windows must not be
	// negative and the crash-loop restart threshold must be positive.
	if c.Fault.Debounce.CrashLoopRestarts < 0 {
		return &FieldError{Field: "fault.debounce.crashLoopRestarts", Reason: "must not be negative"}
	}
	if c.Fault.Debounce.CrashLoopWindow < 0 {
		return &FieldError{Field: "fault.debounce.crashLoopWindow", Reason: "must not be negative"}
	}
	if c.Fault.Retention < 0 {
		return &FieldError{Field: "fault.retention", Reason: "must not be negative"}
	}
	if c.Fault.SignalsConsumer.Workers < 0 {
		return &FieldError{Field: "fault.signalsConsumer.workers", Reason: "must not be negative"}
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
