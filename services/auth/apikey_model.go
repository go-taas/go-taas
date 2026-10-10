package auth

import (
	"encoding/json"
	"time"
)

// APIKey is the GORM model of the api_keys table and the single source of
// truth for its schema (created by AutoMigrate, no hand-written DDL).
//
// Security: no plaintext column exists. The plaintext key appears only in
// the CreateAPIKey response. lookup_hash is the lowercase hex SHA-256 of
// the plaintext (the verification lookup and cache-invalidation key);
// salted_hash is Argon2id over that digest with a per-key random salt.
type APIKey struct {
	// ID is the server-generated UUID v4 exposed as key_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// Name is the display name (1-64 characters, duplicates allowed).
	Name string `gorm:"size:64;not null"`
	// Prefix is the first 8 characters of the plaintext key including
	// the sk- scheme, for masked display (e.g. "sk-a3f9k").
	Prefix string `gorm:"size:8;not null"`
	// LookupHash is the unique lowercase hex SHA-256 of the plaintext.
	LookupHash string `gorm:"size:64;not null;uniqueIndex"`
	// Salt is the base64 of 16 fresh random bytes, one per key.
	Salt string `gorm:"size:32;not null"`
	// SaltedHash is the base64 of Argon2id(key_digest, salt).
	SaltedHash string `gorm:"size:128;not null"`
	// OrganizationID is the owning organization (transitional plain
	// string; foreign key deferred to features #6/#7).
	OrganizationID string `gorm:"size:64;not null;index:idx_api_keys_org_created,priority:1"`
	// CreatedAt is the creation time (UTC).
	CreatedAt time.Time `gorm:"index:idx_api_keys_org_created,priority:2,sort:DESC"`
	// ExpiresAt is nil when the key never expires.
	ExpiresAt *time.Time
	// Revoked is the soft revocation flag.
	Revoked bool `gorm:"not null;default:false"`
	// RevokedAt is the first revocation time, preserved by idempotent
	// re-revoke.
	RevokedAt *time.Time
	// RateLimitRPM is the max requests per minute; 0 = unlimited
	// (feature #11, AD1).
	RateLimitRPM int64 `gorm:"not null;default:0"`
	// RateLimitTPM is the max tokens per minute; 0 = unlimited.
	RateLimitTPM int64 `gorm:"not null;default:0"`
	// Models is the optional model allow-list (feature #46, AD1): a
	// JSON array of catalog model IDs stored as a jsonb string, empty
	// ("[]") = all org-granted models. Encoded/decoded at the
	// repository boundary (routing_policies.service_ids convention).
	Models string `gorm:"type:jsonb;not null;default:'[]'"`
	// IsSystem marks the synthetic platform credential used by the
	// load-test runner (feature #20, AD11). The data-plane gateway
	// recognizes this flag and skips balance holds and rate/spend-limit
	// checks while still producing request logs.
	IsSystem bool `gorm:"not null;default:false;index"`
}

// encodeModelIDs serializes a model allow-list into the jsonb column
// payload (feature #46, AD1). nil and empty both encode as "[]".
func encodeModelIDs(models []string) string {
	if len(models) == 0 {
		return "[]"
	}
	b, err := json.Marshal(models)
	if err != nil {
		// json.Marshal of []string cannot fail; fall back to the
		// empty scope rather than panicking on an impossible path.
		return "[]"
	}
	return string(b)
}

// decodeModelIDs parses the jsonb column payload back into a model
// allow-list. An empty/invalid payload decodes as nil (unrestricted),
// matching the column default.
func decodeModelIDs(raw string) []string {
	if raw == "" || raw == "[]" {
		return nil
	}
	var models []string
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return nil
	}
	return models
}

// TableName returns the table name of APIKey.
func (APIKey) TableName() string { return "api_keys" }
