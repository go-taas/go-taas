package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestParseConfigsMergesAndValidates(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
    maxOpenConns: 10
    maxIdleConns: 5
redis:
  host: localhost
  port: 6379
mq:
  driver: nats
  url: nats://localhost:4222
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Databases.Master.Host != "localhost" {
		t.Fatalf("unexpected db host: %s", cfg.Databases.Master.Host)
	}
	if cfg.MQ.Driver != "nats" {
		t.Fatalf("unexpected mq driver: %s", cfg.MQ.Driver)
	}
}

func TestParseConfigsRejectsInvalidPool(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("ParseConfigs should panic on invalid config")
		}
	}()
	path := writeTempConfig(t, `
db:
  master:
    maxOpenConns: 5
    maxIdleConns: 10
`)
	ParseConfigs(path)
}

func TestParseConfigsSkipsHiddenFiles(t *testing.T) {
	dir := t.TempDir()
	hidden := filepath.Join(dir, ".hidden.yaml")
	normal := filepath.Join(dir, "a.yaml")
	if err := os.WriteFile(hidden, []byte("db:\n  master:\n    maxOpenConns: 1\n    maxIdleConns: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(normal, []byte("redis:\n  host: r1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ParseConfigs(filepath.Join(dir, "*.yaml"))
	cfg := GetConfig()
	if cfg.Databases.Master.MaxOpenConns != 0 {
		t.Fatal("hidden file should be skipped")
	}
	if cfg.Redis.Host != "r1" {
		t.Fatalf("normal file not merged: %+v", cfg.Redis)
	}
}

func TestValidate(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	cfg.Databases.Master.MaxOpenConns = 5
	cfg.Databases.Master.MaxIdleConns = 6
	if err := cfg.Validate(); err == nil {
		t.Fatal("idle > open should fail validation")
	}
	cfg.Databases.Master.MaxIdleConns = 5
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cfg.Redis.MaxActive = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative pool size should fail validation")
	}
	cfg = &Configuration{}
	if err := cfg.Validate(); err == nil {
		t.Fatal("empty billing currency should fail validation")
	}
	cfg.Billing.Currency = "USD"
	cfg.Billing.Reconciliation.Interval = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative reconciliation interval should fail validation")
	}
}

func TestValidateAutoscalingConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	// Negative concurrency workers fails.
	cfg.Infer.Autoscaling.ConcurrencyConsumer.Workers = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative concurrency workers should fail validation")
	}
	cfg.Infer.Autoscaling.ConcurrencyConsumer.Workers = 2
	// Negative status-report interval fails.
	cfg.Infer.Autoscaling.StatusReportInterval = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative status report interval should fail validation")
	}
	cfg.Infer.Autoscaling.StatusReportInterval = 5 * time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid autoscaling config rejected: %v", err)
	}
}

func TestValidateAcceleratorConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	// Negative collect interval fails.
	cfg.Accelerator.CollectInterval = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative collect interval should fail validation")
	}
	cfg.Accelerator.CollectInterval = 30 * time.Second
	// Negative snapshot consumer workers fails.
	cfg.Accelerator.SnapshotConsumer.Workers = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative snapshot consumer workers should fail validation")
	}
	cfg.Accelerator.SnapshotConsumer.Workers = 1
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid accelerator config rejected: %v", err)
	}
}

func TestAcceleratorDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Accelerator.CollectInterval != 30*time.Second {
		t.Fatalf("collect interval default = %v, want 30s", cfg.Accelerator.CollectInterval)
	}
	if cfg.Accelerator.SnapshotConsumer.Workers != 1 {
		t.Fatalf("snapshot consumer workers default = %d, want 1", cfg.Accelerator.SnapshotConsumer.Workers)
	}
}

func TestCompatibilityDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Image.Compatibility.LazySeedDefault != "experimental" {
		t.Fatalf("lazySeedDefault default = %q, want experimental", cfg.Image.Compatibility.LazySeedDefault)
	}
	// An explicit lazySeedDefault is preserved.
	cfg.Image.Compatibility.LazySeedDefault = "supported"
	cfg.applyDefaults()
	if cfg.Image.Compatibility.LazySeedDefault != "supported" {
		t.Fatalf("explicit lazySeedDefault overwritten: %q", cfg.Image.Compatibility.LazySeedDefault)
	}
}

func TestAdminRolesDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	want := []string{"platform-admin", "org-admin", "admin", "owner"}
	if len(cfg.Auth.AdminRoles) != len(want) {
		t.Fatalf("adminRoles default = %v, want %v", cfg.Auth.AdminRoles, want)
	}
	for i, r := range want {
		if cfg.Auth.AdminRoles[i] != r {
			t.Fatalf("adminRoles[%d] = %q, want %q", i, cfg.Auth.AdminRoles[i], r)
		}
	}
	// An explicit adminRoles is preserved.
	cfg.Auth.AdminRoles = []string{"superadmin"}
	cfg.applyDefaults()
	if len(cfg.Auth.AdminRoles) != 1 || cfg.Auth.AdminRoles[0] != "superadmin" {
		t.Fatalf("explicit adminRoles overwritten: %v", cfg.Auth.AdminRoles)
	}
}

func TestValidateAdminRoles(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	// An empty adminRoles (raw zero-value config) is allowed through.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty adminRoles should pass validation: %v", err)
	}
	// A list containing an empty string fails.
	cfg.Auth.AdminRoles = []string{"admin", ""}
	if err := cfg.Validate(); err == nil {
		t.Fatal("adminRoles with an empty string should fail validation")
	}
	// A valid list passes.
	cfg.Auth.AdminRoles = []string{"admin", "owner"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid adminRoles rejected: %v", err)
	}
}

func TestParseConfigsAdminRoles(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
auth:
  adminRoles:
    - platform-admin
    - admin
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if len(cfg.Auth.AdminRoles) != 2 || cfg.Auth.AdminRoles[0] != "platform-admin" || cfg.Auth.AdminRoles[1] != "admin" {
		t.Fatalf("adminRoles from file = %v, want [platform-admin admin]", cfg.Auth.AdminRoles)
	}
}

func TestParseConfigsCompatibilitySection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
image:
  compatibility:
    seedOnBoot: false
    lazySeedDefault: "unsupported"
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Image.Compatibility.SeedOnBoot {
		t.Fatal("seedOnBoot should be false from the file")
	}
	if cfg.Image.Compatibility.LazySeedDefault != "unsupported" {
		t.Fatalf("lazySeedDefault = %q, want unsupported", cfg.Image.Compatibility.LazySeedDefault)
	}
}

func TestParseConfigsCompatibilitySeedOnBootDefaultsTrue(t *testing.T) {
	// An absent seedOnBoot key defaults to true (AD3), while an explicit
	// false is preserved (covered by TestParseConfigsCompatibilitySection).
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if !cfg.Image.Compatibility.SeedOnBoot {
		t.Fatal("seedOnBoot should default to true when the key is absent")
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := "# comment\nEMPTY_LINE=1\nFOO=bar\nQUOTED=\"hello world\"\nEXISTING=fromfile\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXISTING", "fromenv")
	if err := LoadDotEnv(envPath); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("FOO"); got != "bar" {
		t.Fatalf("FOO = %q", got)
	}
	if got := os.Getenv("QUOTED"); got != "hello world" {
		t.Fatalf("QUOTED = %q", got)
	}
	if got := os.Getenv("EXISTING"); got != "fromenv" {
		t.Fatalf("existing env must take precedence, got %q", got)
	}
	// Missing file is not an error.
	if err := LoadDotEnv(filepath.Join(dir, "nope.env")); err != nil {
		t.Fatalf("missing .env should not error: %v", err)
	}
}

func TestWebhookDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Webhook.Delivery.Workers != 4 {
		t.Fatalf("webhook delivery workers default = %d, want 4", cfg.Webhook.Delivery.Workers)
	}
	if cfg.Webhook.Delivery.PollInterval != 5*time.Second {
		t.Fatalf("webhook poll interval default = %v, want 5s", cfg.Webhook.Delivery.PollInterval)
	}
	if cfg.Webhook.Delivery.Timeout != 10*time.Second {
		t.Fatalf("webhook timeout default = %v, want 10s", cfg.Webhook.Delivery.Timeout)
	}
	if cfg.Webhook.Retention.DeliveryTTL != 2160*time.Hour {
		t.Fatalf("webhook delivery TTL default = %v, want 2160h", cfg.Webhook.Retention.DeliveryTTL)
	}
	if cfg.Webhook.Retention.BatchSize != 1000 {
		t.Fatalf("webhook retention batch size default = %d, want 1000", cfg.Webhook.Retention.BatchSize)
	}
	if cfg.Webhook.Retention.Interval != time.Hour {
		t.Fatalf("webhook retention interval default = %v, want 1h", cfg.Webhook.Retention.Interval)
	}
}

func TestValidateWebhookConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	// Negative delivery workers fails.
	cfg.Webhook.Delivery.Workers = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative webhook delivery workers should fail validation")
	}
	cfg.Webhook.Delivery.Workers = 4
	// Negative retention TTL fails.
	cfg.Webhook.Retention.DeliveryTTL = -time.Hour
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative webhook retention TTL should fail validation")
	}
	cfg.Webhook.Retention.DeliveryTTL = 2160 * time.Hour
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid webhook config rejected: %v", err)
	}
}

func TestParseConfigsWebhookSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
webhook:
  delivery:
    workers: 8
    pollInterval: 10s
    timeout: 15s
  retention:
    enabled: false
    deliveryTTL: 720h
    batchSize: 500
    interval: 30m
  secretEncryptionKey: "test-key"
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Webhook.Delivery.Workers != 8 {
		t.Fatalf("webhook delivery workers = %d, want 8", cfg.Webhook.Delivery.Workers)
	}
	if cfg.Webhook.Delivery.PollInterval != 10*time.Second {
		t.Fatalf("webhook poll interval = %v, want 10s", cfg.Webhook.Delivery.PollInterval)
	}
	if cfg.Webhook.Retention.Enabled {
		t.Fatal("webhook retention should be disabled from the file")
	}
	if cfg.Webhook.Retention.DeliveryTTL != 720*time.Hour {
		t.Fatalf("webhook delivery TTL = %v, want 720h", cfg.Webhook.Retention.DeliveryTTL)
	}
	if cfg.Webhook.SecretEncryptionKey != "test-key" {
		t.Fatalf("webhook secret key = %q, want test-key", cfg.Webhook.SecretEncryptionKey)
	}
}

func TestObservabilityDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Observability.MaxRangeSeconds != 92*24*3600 {
		t.Fatalf("observability max range default = %d, want %d", cfg.Observability.MaxRangeSeconds, 92*24*3600)
	}
	if cfg.Observability.Status.StaleAfterSeconds != 60 {
		t.Fatalf("observability status staleAfterSeconds default = %d, want 60", cfg.Observability.Status.StaleAfterSeconds)
	}
}

func TestValidateObservabilityConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	// Negative max range fails.
	cfg.Observability.MaxRangeSeconds = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative observability max range should fail validation")
	}
	cfg.Observability.MaxRangeSeconds = 92 * 24 * 3600
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid observability config rejected: %v", err)
	}
}

func TestParseConfigsObservabilitySection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
observability:
  maxRangeSeconds: 604800
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Observability.MaxRangeSeconds != 604800 {
		t.Fatalf("observability max range = %d, want 604800", cfg.Observability.MaxRangeSeconds)
	}
}

func TestResourceMetricsDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.ResourceMetrics.MaxRangeSeconds != 92*24*3600 {
		t.Fatalf("resourcemetrics max range default = %d, want %d", cfg.ResourceMetrics.MaxRangeSeconds, 92*24*3600)
	}
	if cfg.Controller.ResourceMetrics.SampleInterval != 30*time.Second {
		t.Fatalf("controller resourceMetrics sampleInterval default = %v, want 30s", cfg.Controller.ResourceMetrics.SampleInterval)
	}
}

func TestValidateResourceMetricsConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	cfg.ResourceMetrics.MaxRangeSeconds = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative resourcemetrics max range should fail validation")
	}
	cfg.ResourceMetrics.MaxRangeSeconds = 92 * 24 * 3600
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid resourcemetrics config rejected: %v", err)
	}
}

func TestParseConfigsResourceMetricsSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
resourcemetrics:
  maxRangeSeconds: 604800
controller:
  resourceMetrics:
    sampleInterval: 15s
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.ResourceMetrics.MaxRangeSeconds != 604800 {
		t.Fatalf("resourcemetrics max range = %d, want 604800", cfg.ResourceMetrics.MaxRangeSeconds)
	}
	if cfg.Controller.ResourceMetrics.SampleInterval != 15*time.Second {
		t.Fatalf("controller resourceMetrics sampleInterval = %v, want 15s", cfg.Controller.ResourceMetrics.SampleInterval)
	}
}

func TestDocsDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Docs.CatalogVersion != "v1" {
		t.Fatalf("docs catalogVersion default = %q, want v1", cfg.Docs.CatalogVersion)
	}
}

func TestValidateDocsConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	cfg.Docs.CatalogVersion = "v1"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid docs config rejected: %v", err)
	}
}

func TestParseConfigsDocsSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
docs:
  catalogVersion: v2
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Docs.CatalogVersion != "v2" {
		t.Fatalf("docs catalogVersion = %q, want v2", cfg.Docs.CatalogVersion)
	}
}

func TestClusterDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Controller.Cluster.CollectInterval != 30*time.Second {
		t.Fatalf("controller cluster collectInterval default = %v, want 30s", cfg.Controller.Cluster.CollectInterval)
	}
}

func TestValidateClusterConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	cfg.Controller.Cluster.CollectInterval = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative cluster collectInterval should fail validation")
	}
	cfg.Controller.Cluster.CollectInterval = 30 * time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid cluster config rejected: %v", err)
	}
}

func TestParseConfigsClusterSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
cluster:
  defaultClusterId: cluster-1
controller:
  cluster:
    collectInterval: 15s
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Cluster.DefaultClusterID != "cluster-1" {
		t.Fatalf("cluster defaultClusterId = %q, want cluster-1", cfg.Cluster.DefaultClusterID)
	}
	if cfg.Controller.Cluster.CollectInterval != 15*time.Second {
		t.Fatalf("controller cluster collectInterval = %v, want 15s", cfg.Controller.Cluster.CollectInterval)
	}
}

func TestAccountExportDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Account.Export.GeneratorInterval != 5*time.Second {
		t.Fatalf("account export generatorInterval default = %v, want 5s", cfg.Account.Export.GeneratorInterval)
	}
	if cfg.Account.Export.MaxRangeSeconds != 92*24*3600 {
		t.Fatalf("account export maxRangeSeconds default = %d, want %d", cfg.Account.Export.MaxRangeSeconds, 92*24*3600)
	}
}

func TestValidateAccountExportConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	cfg.Account.Export.MaxRangeSeconds = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative account export maxRangeSeconds should fail validation")
	}
	cfg.Account.Export.MaxRangeSeconds = 92 * 24 * 3600
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid account export config rejected: %v", err)
	}
}

func TestParseConfigsAccountExportSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
account:
  export:
    generatorInterval: 10s
    maxRangeSeconds: 604800
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Account.Export.GeneratorInterval != 10*time.Second {
		t.Fatalf("account export generatorInterval = %v, want 10s", cfg.Account.Export.GeneratorInterval)
	}
	if cfg.Account.Export.MaxRangeSeconds != 604800 {
		t.Fatalf("account export maxRangeSeconds = %d, want 604800", cfg.Account.Export.MaxRangeSeconds)
	}
}

func TestNotificationDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Notification.Consumer.Workers != 4 {
		t.Fatalf("notification consumer workers default = %d, want 4", cfg.Notification.Consumer.Workers)
	}
	if cfg.Notification.Retention.NotificationTTL != 2160*time.Hour {
		t.Fatalf("notification retention TTL default = %v, want 2160h", cfg.Notification.Retention.NotificationTTL)
	}
	if cfg.Notification.Retention.BatchSize != 1000 {
		t.Fatalf("notification retention batch size default = %d, want 1000", cfg.Notification.Retention.BatchSize)
	}
	if cfg.Notification.Retention.Interval != time.Hour {
		t.Fatalf("notification retention interval default = %v, want 1h", cfg.Notification.Retention.Interval)
	}
}

func TestValidateNotificationConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	// Negative consumer workers fails.
	cfg.Notification.Consumer.Workers = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative notification consumer workers should fail validation")
	}
	cfg.Notification.Consumer.Workers = 4
	// Negative retention TTL fails.
	cfg.Notification.Retention.NotificationTTL = -time.Hour
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative notification retention TTL should fail validation")
	}
	cfg.Notification.Retention.NotificationTTL = 2160 * time.Hour
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid notification config rejected: %v", err)
	}
}

func TestParseConfigsNotificationSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
notification:
  consumer:
    workers: 8
  retention:
    enabled: false
    notificationTTL: 720h
    batchSize: 500
    interval: 30m
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Notification.Consumer.Workers != 8 {
		t.Fatalf("notification consumer workers = %d, want 8", cfg.Notification.Consumer.Workers)
	}
	if cfg.Notification.Retention.Enabled {
		t.Fatal("notification retention should be disabled from the file")
	}
	if cfg.Notification.Retention.NotificationTTL != 720*time.Hour {
		t.Fatalf("notification retention TTL = %v, want 720h", cfg.Notification.Retention.NotificationTTL)
	}
}

func TestTracingDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Tracing.Retention.TraceTTL != 720*time.Hour {
		t.Fatalf("tracing retention TTL default = %v, want 720h", cfg.Tracing.Retention.TraceTTL)
	}
}

func TestValidateTracingConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	// Negative retention TTL fails.
	cfg.Tracing.Retention.TraceTTL = -time.Hour
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative tracing retention TTL should fail validation")
	}
	cfg.Tracing.Retention.TraceTTL = 720 * time.Hour
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid tracing config rejected: %v", err)
	}
}

func TestParseConfigsTracingSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
tracing:
  retention:
    traceTTL: 360h
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Tracing.Retention.TraceTTL != 360*time.Hour {
		t.Fatalf("tracing retention TTL = %v, want 360h", cfg.Tracing.Retention.TraceTTL)
	}
}

func TestForecastDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Billing.Forecast.HorizonDefaultDays != 30 {
		t.Fatalf("forecast horizon default days = %d, want 30", cfg.Billing.Forecast.HorizonDefaultDays)
	}
	if cfg.Billing.Forecast.HorizonMaxDays != 90 {
		t.Fatalf("forecast horizon max days = %d, want 90", cfg.Billing.Forecast.HorizonMaxDays)
	}
	if cfg.Billing.Forecast.RangeMaxDays != 92 {
		t.Fatalf("forecast range max days = %d, want 92", cfg.Billing.Forecast.RangeMaxDays)
	}
}

func TestParseConfigsForecastSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
billing:
  forecast:
    horizonDefaultDays: 14
    horizonMaxDays: 60
    rangeMaxDays: 45
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Billing.Forecast.HorizonDefaultDays != 14 {
		t.Fatalf("forecast horizon default days = %d, want 14", cfg.Billing.Forecast.HorizonDefaultDays)
	}
	if cfg.Billing.Forecast.HorizonMaxDays != 60 {
		t.Fatalf("forecast horizon max days = %d, want 60", cfg.Billing.Forecast.HorizonMaxDays)
	}
	if cfg.Billing.Forecast.RangeMaxDays != 45 {
		t.Fatalf("forecast range max days = %d, want 45", cfg.Billing.Forecast.RangeMaxDays)
	}
}

func TestBatchDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Batch.Worker.PollInterval != 5*time.Second {
		t.Fatalf("batch worker pollInterval default = %v, want 5s", cfg.Batch.Worker.PollInterval)
	}
	if cfg.Batch.Worker.Concurrency != 4 {
		t.Fatalf("batch worker concurrency default = %d, want 4", cfg.Batch.Worker.Concurrency)
	}
	if cfg.Batch.Retention.FileTTL != 720*time.Hour {
		t.Fatalf("batch retention fileTTL default = %v, want 720h", cfg.Batch.Retention.FileTTL)
	}
	if cfg.Batch.Retention.Interval != time.Hour {
		t.Fatalf("batch retention interval default = %v, want 1h", cfg.Batch.Retention.Interval)
	}
	if cfg.Batch.MaxFileBytes != 524288000 {
		t.Fatalf("batch maxFileBytes default = %d, want 524288000", cfg.Batch.MaxFileBytes)
	}
	if cfg.Batch.MaxLines != 50000 {
		t.Fatalf("batch maxLines default = %d, want 50000", cfg.Batch.MaxLines)
	}
	if cfg.Batch.StoreDir != "/data/batch" {
		t.Fatalf("batch storeDir default = %q, want /data/batch", cfg.Batch.StoreDir)
	}
}

func TestValidateBatchConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	cfg.Batch.Worker.Concurrency = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative batch worker concurrency should fail validation")
	}
	cfg.Batch.Worker.Concurrency = 4
	cfg.Batch.MaxFileBytes = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative batch maxFileBytes should fail validation")
	}
	cfg.Batch.MaxFileBytes = 524288000
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid batch config rejected: %v", err)
	}
}

func TestParseConfigsBatchSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
batch:
  worker:
    pollInterval: 10s
    concurrency: 8
  retention:
    fileTTL: 48h
    interval: 30m
  maxFileBytes: 1048576
  maxLines: 1000
  storeDir: /tmp/batch
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Batch.Worker.PollInterval != 10*time.Second {
		t.Fatalf("batch worker pollInterval = %v, want 10s", cfg.Batch.Worker.PollInterval)
	}
	if cfg.Batch.Worker.Concurrency != 8 {
		t.Fatalf("batch worker concurrency = %d, want 8", cfg.Batch.Worker.Concurrency)
	}
	if cfg.Batch.Retention.FileTTL != 48*time.Hour {
		t.Fatalf("batch retention fileTTL = %v, want 48h", cfg.Batch.Retention.FileTTL)
	}
	if cfg.Batch.MaxFileBytes != 1048576 {
		t.Fatalf("batch maxFileBytes = %d, want 1048576", cfg.Batch.MaxFileBytes)
	}
	if cfg.Batch.StoreDir != "/tmp/batch" {
		t.Fatalf("batch storeDir = %q, want /tmp/batch", cfg.Batch.StoreDir)
	}
}

func TestPromptDefaults(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	if cfg.Prompt.MaxNameLength != 128 {
		t.Fatalf("prompt maxNameLength default = %d, want 128", cfg.Prompt.MaxNameLength)
	}
	if cfg.Prompt.MaxContentLength != 6144 {
		t.Fatalf("prompt maxContentLength default = %d, want 6144", cfg.Prompt.MaxContentLength)
	}
}

func TestValidatePromptConfig(t *testing.T) {
	cfg := &Configuration{}
	cfg.Billing.Currency = "USD"
	cfg.Prompt.MaxNameLength = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative prompt maxNameLength should fail validation")
	}
	cfg.Prompt.MaxNameLength = 128
	cfg.Prompt.MaxContentLength = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative prompt maxContentLength should fail validation")
	}
	cfg.Prompt.MaxContentLength = 6144
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid prompt config rejected: %v", err)
	}
}

func TestParseConfigsPromptSection(t *testing.T) {
	path := writeTempConfig(t, `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
prompt:
  maxNameLength: 64
  maxContentLength: 4096
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig returned nil after ParseConfigs")
	}
	if cfg.Prompt.MaxNameLength != 64 {
		t.Fatalf("prompt maxNameLength = %d, want 64", cfg.Prompt.MaxNameLength)
	}
	if cfg.Prompt.MaxContentLength != 4096 {
		t.Fatalf("prompt maxContentLength = %d, want 4096", cfg.Prompt.MaxContentLength)
	}
}
