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
