// Package provider implements the external-model-provider module
// (feature #48). It owns the third-party LLM provider registry, the
// write-only AES-256-GCM encrypted API keys, upstream-model curation, the
// publish linkage into the model catalog, the scheduled five-state
// connectivity probe and the external chat proxy. The module is
// admin-surface only (/api/v1/admin/providers/*).
package provider

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Provider type values (design FR1.2).
const (
	TypeDeepSeek         = "deepseek"
	TypeOpenAI           = "openai"
	TypeOpenAICompatible = "openai_compatible"
)

// Probe status values (design FR3.3).
const (
	ProbeUnknown      = "unknown"
	ProbeOK           = "ok"
	ProbeUnauthorized = "unauthorized"
	ProbeUnreachable  = "unreachable"
	ProbeError        = "error"
)

// Provider is the GORM model of the providers table. The schema is the
// single source of truth (AutoMigrate, no hand-written DDL).
type Provider struct {
	// ID is the server-generated UUID v4 exposed as provider_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// Name is the provider name, 1-64 chars, unique across providers.
	Name string `gorm:"size:64;not null;uniqueIndex"`
	// Type is deepseek | openai | openai_compatible.
	Type string `gorm:"size:32;not null"`
	// BaseURL is an absolute http(s) URL with a host; the trailing slash
	// is stripped at use.
	BaseURL string `gorm:"size:512;not null"`
	// Enabled is the operator enable switch; default false on create.
	Enabled bool `gorm:"not null;default:false"`
	// RequestTimeoutSeconds is the per-provider request timeout (1-120).
	RequestTimeoutSeconds int `gorm:"not null;default:10"`
	// ExtraHeaders holds the extra request headers. The values are
	// AES-256-GCM ciphertexts; reads expose the names only.
	ExtraHeaders StringMap `gorm:"type:jsonb"`
	// APIKeyCiphertext is AES-256-GCM nonce||ciphertext of the API key;
	// it is never selected into a response.
	APIKeyCiphertext []byte `gorm:"type:bytea"`
	// APIKeyHint is the masked hint ("••••3f9a") computed at create/rotate;
	// reads return it and never decrypt.
	APIKeyHint string `gorm:"size:16;not null;default:''"`
	// ProbeStatus is the last probe status (five states); default unknown.
	ProbeStatus string `gorm:"size:16;not null;default:'unknown'"`
	// ProbeLatencyMs is the last probe latency in milliseconds.
	ProbeLatencyMs int64 `gorm:"not null;default:0"`
	// ProbeHTTPStatus is the last probe HTTP status (0 when no response).
	ProbeHTTPStatus int `gorm:"not null;default:0"`
	// ProbeMessage is the sanitized last probe message.
	ProbeMessage string `gorm:"size:256;not null;default:''"`
	// ProbeCheckedAt is the last probe time (nil before the first probe).
	ProbeCheckedAt *time.Time
	// CreatedBy / UpdatedBy are the acting admin user ids.
	CreatedBy string `gorm:"size:64;not null;default:''"`
	UpdatedBy string `gorm:"size:64;not null;default:''"`
	// CreatedAt / UpdatedAt are UTC timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName overrides the default GORM table name.
func (Provider) TableName() string { return "providers" }

// ProviderModel is the GORM model of the provider_models table: one
// curated upstream model row. The upstream-model-id uniqueness within a
// provider is enforced by the composite unique index.
type ProviderModel struct { //nolint:revive // provider.ProviderModel is the documented domain name
	// ID is the server-generated UUID v4 exposed as model_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// ProviderID is the owning provider's id.
	ProviderID string `gorm:"type:uuid;not null;uniqueIndex:idx_provider_models_provider_upstream,priority:1;index"`
	// UpstreamModelID is the provider's model id; unique per provider.
	UpstreamModelID string `gorm:"size:128;not null;uniqueIndex:idx_provider_models_provider_upstream,priority:2"`
	// DisplayName is the optional display name, at most 128 chars.
	DisplayName string `gorm:"size:128;not null;default:''"`
	// ContextWindow is the context window in tokens (0 = unknown).
	ContextWindow int64 `gorm:"not null;default:0"`
	// Enabled marks the row as a publish candidate; the service defaults
	// it to true on add. No DB default is used so an explicit false is
	// persisted (a GORM default tag would override a zero value).
	Enabled bool `gorm:"not null"`
	// Published is set by publish; the catalog model id binds it.
	Published bool `gorm:"not null;default:false"`
	// CatalogModelID is the published models.id (empty when unpublished).
	// It is a plain string column (not uuid) so the empty default is valid
	// on PostgreSQL.
	CatalogModelID string `gorm:"size:64;not null;default:'';index"`
	// CreatedAt / UpdatedAt are UTC timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName overrides the default GORM table name.
func (ProviderModel) TableName() string { return "provider_models" }

// StringMap is a string map persisted in a jsonb column. It is
// JSON-serialized so it works on PostgreSQL (jsonb) and SQLite (text).
type StringMap map[string]string

// Value implements driver.Valuer.
func (m StringMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("provider: marshal string map: %w", err)
	}
	return string(b), nil
}

// Scan implements sql.Scanner.
func (m *StringMap) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*m = nil
		return nil
	case []byte:
		if len(v) == 0 {
			*m = nil
			return nil
		}
		return json.Unmarshal(v, m)
	case string:
		if v == "" {
			*m = nil
			return nil
		}
		return json.Unmarshal([]byte(v), m)
	default:
		return fmt.Errorf("provider: cannot scan %T into StringMap", src)
	}
}

// validProviderType reports whether t is one of the three supported types.
func validProviderType(t string) bool {
	switch t {
	case TypeDeepSeek, TypeOpenAI, TypeOpenAICompatible:
		return true
	default:
		return false
	}
}

// headerNames returns the (sorted) names of the stored extra headers.
func headerNames(m StringMap) []string {
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
