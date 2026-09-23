package config

import (
	"testing"
	"time"
)

func TestArgon2ParamsDefaults(t *testing.T) {
	// Zero values fall back to the shipped defaults at the use site.
	p := Argon2Params{}
	d := p.WithDefaults()
	if d.Algorithm != "argon2id" {
		t.Fatalf("default algorithm: %s", d.Algorithm)
	}
	if d.Time != 1 {
		t.Fatalf("default time: %d", d.Time)
	}
	if d.MemoryMiB != 64 {
		t.Fatalf("default memoryMiB: %d", d.MemoryMiB)
	}
	if d.Parallelism != 1 {
		t.Fatalf("default parallelism: %d", d.Parallelism)
	}
	// Explicit values are preserved.
	p = Argon2Params{Algorithm: "argon2id", Time: 2, MemoryMiB: 128, Parallelism: 2}
	d = p.WithDefaults()
	if d.Time != 2 || d.MemoryMiB != 128 || d.Parallelism != 2 {
		t.Fatalf("explicit values not preserved: %+v", d)
	}
}

func TestValidateAuthNegativeValues(t *testing.T) {
	cfg := &Configuration{}
	cfg.Auth.APIKeyHash.Time = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative argon2 time must fail validation")
	}
	cfg = &Configuration{}
	cfg.Auth.APIKeyHash.MemoryMiB = -64
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative argon2 memory must fail validation")
	}
	cfg = &Configuration{}
	cfg.Auth.APIKeyHash.Parallelism = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative argon2 parallelism must fail validation")
	}
	cfg = &Configuration{}
	cfg.Auth.APIKeyCacheTTL = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative apiKeyCacheTTL must fail validation")
	}
	// Valid config passes.
	cfg = &Configuration{}
	cfg.Auth.APIKeyHash = Argon2Params{Algorithm: "argon2id", Time: 1, MemoryMiB: 64, Parallelism: 1}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid auth config rejected: %v", err)
	}
}

func TestParseConfigsArgon2Section(t *testing.T) {
	path := writeTempConfig(t, `
auth:
  sessionTTL: 24h
  apiKeyCacheTTL: 30s
  apiKeyHash:
    algorithm: argon2id
    time: 1
    memoryMiB: 64
    parallelism: 1
`)
	ParseConfigs(path)
	cfg := GetConfig()
	if cfg.Auth.APIKeyCacheTTL != 30*time.Second {
		t.Fatalf("apiKeyCacheTTL: %v", cfg.Auth.APIKeyCacheTTL)
	}
	if cfg.Auth.APIKeyHash.Algorithm != "argon2id" {
		t.Fatalf("algorithm: %s", cfg.Auth.APIKeyHash.Algorithm)
	}
	if cfg.Auth.APIKeyHash.Time != 1 || cfg.Auth.APIKeyHash.MemoryMiB != 64 || cfg.Auth.APIKeyHash.Parallelism != 1 {
		t.Fatalf("argon2 params: %+v", cfg.Auth.APIKeyHash)
	}
}
