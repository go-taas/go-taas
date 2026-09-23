package billing

import (
	"time"

	"gorm.io/datatypes"
)

// PriceEntry is one version of a (model, accelerator_type) price cell —
// the effective-dated matrix row (D1). Rows are upserted in place on
// (model, card, effective_from); superseded versions remain as history
// (no delete in v1).
type PriceEntry struct {
	// ID is the server-generated UUID v4, exposed as price_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// ModelID is the model the price applies to.
	ModelID string `gorm:"size:128;not null;uniqueIndex:idx_price_entries_cell_version,priority:1"`
	// AcceleratorType is the card type; the sentinel "default" is the
	// per-model fallback (D6).
	AcceleratorType string `gorm:"size:64;not null;uniqueIndex:idx_price_entries_cell_version,priority:2"`
	// InputPricePerMillion is the input token rate per 1M tokens.
	InputPricePerMillion float64 `gorm:"not null"`
	// OutputPricePerMillion is the output token rate per 1M tokens.
	OutputPricePerMillion float64 `gorm:"not null"`
	// CachedPricePerMillion is the cache-hit token rate per 1M tokens
	// (D2).
	CachedPricePerMillion float64 `gorm:"not null;default:0"`
	// Currency is the platform billing currency at write time (D4).
	Currency string `gorm:"size:8;not null"`
	// EffectiveFrom is the version's start of validity, unix seconds
	// (D1).
	EffectiveFrom int64 `gorm:"not null;uniqueIndex:idx_price_entries_cell_version,priority:3"`
	// TiersJSON is the ordered PriceTier array (D3); "[]" = flat.
	TiersJSON datatypes.JSON `gorm:"not null"`
	// CreatedAt is the row write time (UTC).
	CreatedAt time.Time
	// UpdatedAt is bumped on in-place update (FR1.1).
	UpdatedAt time.Time
}

// TableName overrides the default GORM table name.
func (PriceEntry) TableName() string { return "price_entries" }

// UsageLine is one inference request's billable usage record — the
// charging atom, written by billing's own metering.events consumer (D5).
// Rows are immutable after ingestion except for the charging engine's
// charged_charge_id marking.
type UsageLine struct {
	// ID is the server-generated UUID v4, exposed as line_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// RequestID is the inference request id — the idempotency key (D5).
	RequestID string `gorm:"size:128;not null;uniqueIndex"`
	// OrganizationID is the owning organization.
	OrganizationID string `gorm:"size:64;not null;index:idx_usage_lines_org_completed,priority:1"`
	// APIKeyID is the API Key that made the request.
	APIKeyID string `gorm:"size:64;not null;index:idx_usage_lines_key_completed,priority:1"`
	// ModelID is the model that served the request.
	ModelID string `gorm:"size:128;not null"`
	// ServiceID optionally names the inference service.
	ServiceID *string `gorm:"size:64"`
	// AcceleratorType is the resolved card type (D6); "default" when
	// unresolvable.
	AcceleratorType string `gorm:"size:64;not null"`
	// PromptTokens is the input token count.
	PromptTokens int64 `gorm:"not null;default:0"`
	// CompletionTokens is the output token count.
	CompletionTokens int64 `gorm:"not null;default:0"`
	// CachedTokens is the cache-hit token count.
	CachedTokens int64 `gorm:"not null;default:0"`
	// ReasoningTokens is the reasoning-trace token count.
	ReasoningTokens int64 `gorm:"not null;default:0"`
	// CompletedAt is the request completion time.
	CompletedAt time.Time `gorm:"not null;index:idx_usage_lines_org_completed,priority:2;index:idx_usage_lines_key_completed,priority:2"`
	// CreatedAt is the line write time (UTC).
	CreatedAt time.Time
	// ChargedChargeID links the charge record; NULL means uncharged
	// (FR4.2).
	ChargedChargeID *string `gorm:"size:64;index"`
}

// TableName overrides the default GORM table name.
func (UsageLine) TableName() string { return "usage_lines" }

// ChargeRecord is the charged aggregate for one (api_key, model, card,
// hour) group — the immutable billing evidence (D7/D8). Rows are never
// updated or deleted in v1.
type ChargeRecord struct {
	// ID is the server-generated UUID v4, exposed as charge_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// OrganizationID is the owning organization.
	OrganizationID string `gorm:"size:64;not null;index:idx_charge_records_org_period,priority:1"`
	// APIKeyID is the charged API Key.
	APIKeyID string `gorm:"size:64;not null;uniqueIndex:idx_charge_records_group_period,priority:1"`
	// ModelID is the model group.
	ModelID string `gorm:"size:128;not null;uniqueIndex:idx_charge_records_group_period,priority:2"`
	// AcceleratorType is the card group.
	AcceleratorType string `gorm:"size:64;not null;uniqueIndex:idx_charge_records_group_period,priority:3"`
	// PeriodStart is the hour start, unix seconds (UTC).
	PeriodStart int64 `gorm:"not null;uniqueIndex:idx_charge_records_group_period,priority:4;index:idx_charge_records_org_period,priority:2"`
	// PeriodEnd is PeriodStart + 3600.
	PeriodEnd int64 `gorm:"not null"`
	// PromptTokens is the summed input tokens over the group-hour.
	PromptTokens int64 `gorm:"not null;default:0"`
	// CompletionTokens is the summed output tokens over the group-hour.
	CompletionTokens int64 `gorm:"not null;default:0"`
	// CachedTokens is the summed cache-hit tokens over the group-hour.
	CachedTokens int64 `gorm:"not null;default:0"`
	// ReasoningTokens is the summed reasoning tokens over the group-hour.
	ReasoningTokens int64 `gorm:"not null;default:0"`
	// RequestCount is the number of lines charged.
	RequestCount int64 `gorm:"not null;default:0"`
	// Amount is the D2 formula result, rounded to 2 decimals.
	Amount float64 `gorm:"not null;default:0"`
	// Currency is the price entry's currency (or config when unpriced).
	Currency string `gorm:"size:8;not null"`
	// PriceID names the applied price version; NULL when unpriced (D8).
	PriceID *string `gorm:"type:uuid"`
	// TierIndex is the applied tier; -1 = flat rate. No gorm default
	// tag: a default would make GORM skip the zero value 0 (tier 0)
	// and store -1 instead.
	TierIndex int `gorm:"not null"`
	// Priced reports whether a price applied (D8).
	Priced bool `gorm:"not null;default:false"`
	// MonthToDateTokens is the org's cumulative monthly volume for this
	// (model, card) before this charge — the tier-selection evidence
	// (D3).
	MonthToDateTokens int64 `gorm:"not null;default:0"`
	// ChargedAt is when the charge committed.
	ChargedAt time.Time
}

// TableName overrides the default GORM table name.
func (ChargeRecord) TableName() string { return "charge_records" }

// PriceTier is one volume tier of a price entry (D3): rates apply once
// the organization's month-to-date cumulative token volume for (model,
// card) reaches the tier. The last tier has UpToTokens = 0 (unbounded).
type PriceTier struct {
	// UpToTokens is the exclusive upper bound, in cumulative monthly
	// tokens; 0 on the last tier means unbounded.
	UpToTokens int64 `json:"up_to_tokens"`
	// InputPricePerMillion is the tier's input token rate.
	InputPricePerMillion float64 `json:"input_price_per_million"`
	// OutputPricePerMillion is the tier's output token rate.
	OutputPricePerMillion float64 `json:"output_price_per_million"`
}

// UsageGroup is one (model, card) group of uncharged usage lines within
// an (api_key, hour) bucket — the charging unit.
type UsageGroup struct {
	OrganizationID   string
	APIKeyID         string
	ModelID          string
	AcceleratorType  string
	PeriodStart      int64
	PeriodEnd        int64
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ReasoningTokens  int64
	RequestCount     int64
}

// HourBucket identifies one charging bucket: the API key and the
// hour-start unix seconds its uncharged usage lines fall into.
type HourBucket struct {
	APIKeyID       string
	OrganizationID string
	PeriodStart    int64
}

// PriceFilter scopes a price list query (FR2).
type PriceFilter struct {
	ModelID         string
	AcceleratorType string
	IncludeHistory  bool
	Now             int64
	Offset          int
	Limit           int
}

// ChargeFilter scopes a charge-record list query (FR5.1).
type ChargeFilter struct {
	OrganizationID string
	APIKeyID       string
	ModelID        string
	Since          int64
	Until          int64
	Offset         int
	Limit          int
}

// BillSummaryRow is one aggregated monthly bill row (D9).
type BillSummaryRow struct {
	MonthStart    int64
	Amount        float64
	Currency      string
	ChargeCount   int64
	UnpricedCount int64
}
