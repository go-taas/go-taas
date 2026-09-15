package config

import (
	"os"
	"path/filepath"
	"testing"
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
