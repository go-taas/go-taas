// Package config provides configuration loading for all platform binaries.
//
// Configuration is merged from a glob of YAML files (e.g. configs/*.yaml),
// with environment variables overriding any value: the key "db.master.host"
// is overridden by CONFIG_DB_MASTER_HOST. After loading, GetConfig() returns
// the process-wide configuration.
package config

import (
	"time"
)

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
	BufferSize int `mapstructure:"bufferSize"`
	// FlushInterval bounds how long events may wait in the buffer.
	FlushInterval time.Duration `mapstructure:"flushInterval"`
}

// BillingConfig holds billing-module specific settings.
type BillingConfig struct {
	// SettlementInterval is the period between settlement runs.
	SettlementInterval time.Duration `mapstructure:"settlementInterval"`
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

// LogConfig holds logging settings loaded from configuration files.
type LogConfig struct {
	// Level is the minimum log level: debug, info, warn, error.
	Level string `mapstructure:"level"`
	// Encoding selects "console" or "json" output.
	Encoding string `mapstructure:"encoding"`
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
	if c.Auth.APIKeyHash.Time < 0 || c.Auth.APIKeyHash.MemoryMiB < 0 || c.Auth.APIKeyHash.Parallelism < 0 {
		return &FieldError{Field: "auth.apiKeyHash", Reason: "argon2 parameters must be non-negative"}
	}
	if c.Infer.StatusConsumer.Workers < 0 {
		return &FieldError{Field: "infer.statusConsumer.workers", Reason: "must not be negative"}
	}
	if c.Image.WarmupStatusConsumer.Workers < 0 {
		return &FieldError{Field: "image.warmupStatusConsumer.workers", Reason: "must not be negative"}
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
