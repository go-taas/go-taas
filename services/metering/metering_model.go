package metering

import (
	"time"
)

// Voucher is one inference request's token-usage record — the audit
// atom every aggregate is derived from. Rows are immutable after
// ingestion except for the settlement runner's settled_usage_record_id
// marking; the retention runner is the only deleter (D1, FR2).
type Voucher struct {
	// ID is the server-generated UUID v4, exposed as voucher_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// RequestID is the inference request id from the gateway — the
	// idempotency key (D1).
	RequestID string `gorm:"size:128;not null;uniqueIndex"`
	// OrganizationID is the owning organization (transitional plain
	// string, as on api_keys).
	OrganizationID string `gorm:"size:64;not null;index:idx_vouchers_org_completed,priority:1"`
	// APIKeyID is the API Key that made the request.
	APIKeyID string `gorm:"size:64;not null;index:idx_vouchers_key_completed,priority:1"`
	// OwnerUserID is the key's personal owner at request time; NULL for
	// an organization-shared key. Snapshot, not a join: the attribution
	// must survive a key transfer or deletion.
	OwnerUserID *string `gorm:"type:uuid;index:idx_vouchers_org_owner_completed,priority:2"`
	// ProjectID is the key's project at request time; NULL when the key
	// is not assigned to one.
	ProjectID *string `gorm:"size:64;index:idx_vouchers_org_project_completed,priority:2"`
	// ModelID is the model that served the request.
	ModelID string `gorm:"size:128;not null"`
	// ServiceID optionally names the inference service that served the
	// request.
	ServiceID *string `gorm:"size:64"`
	// PromptTokens is the input token count.
	PromptTokens int64 `gorm:"not null;default:0"`
	// CompletionTokens is the output token count.
	CompletionTokens int64 `gorm:"not null;default:0"`
	// CachedTokens is the cache-hit token count.
	CachedTokens int64 `gorm:"not null;default:0"`
	// ReasoningTokens is the reasoning-trace token count.
	ReasoningTokens int64 `gorm:"not null;default:0"`
	// CompletedAt is the request completion time (from the event).
	CompletedAt time.Time `gorm:"not null;index:idx_vouchers_org_completed,priority:2;index:idx_vouchers_key_completed,priority:2"`
	// CreatedAt is the voucher write time (UTC).
	CreatedAt time.Time
	// SettledUsageRecordID links the settlement's usage record; NULL
	// means pending (FR2.1).
	SettledUsageRecordID *string `gorm:"size:64;index"`
}

// TableName overrides the default GORM table name.
func (Voucher) TableName() string { return "vouchers" }

// UsageRecord is the settled aggregate for one (api_key_id, hour)
// bucket — the billing evidence feature #5 prices. Rows are never
// deleted (D7, FR5.2).
type UsageRecord struct {
	// ID is the server-generated UUID v4, exposed as usage_record_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// OrganizationID is the owning organization.
	OrganizationID string `gorm:"size:64;not null;index"`
	// APIKeyID is the settled API Key.
	APIKeyID string `gorm:"size:64;not null;uniqueIndex:idx_usage_records_key_period,priority:1"`
	// OwnerUserID is the key's personal owner at settlement time.
	OwnerUserID *string `gorm:"type:uuid;index:idx_usage_records_org_owner,priority:2"`
	// ProjectID is the key's project at settlement time.
	ProjectID *string `gorm:"size:64;index:idx_usage_records_org_project,priority:2"`
	// PeriodStart is the hour start, unix seconds (UTC).
	PeriodStart int64 `gorm:"not null;uniqueIndex:idx_usage_records_key_period,priority:2"`
	// PeriodEnd is PeriodStart + 3600.
	PeriodEnd int64 `gorm:"not null"`
	// PromptTokens is the summed input tokens over the hour.
	PromptTokens int64 `gorm:"not null;default:0"`
	// CompletionTokens is the summed output tokens over the hour.
	CompletionTokens int64 `gorm:"not null;default:0"`
	// CachedTokens is the summed cache-hit tokens over the hour.
	CachedTokens int64 `gorm:"not null;default:0"`
	// ReasoningTokens is the summed reasoning tokens over the hour.
	ReasoningTokens int64 `gorm:"not null;default:0"`
	// RequestCount is the number of vouchers settled into the record.
	RequestCount int64 `gorm:"not null;default:0"`
	// SettledAt is when the runner committed the record.
	SettledAt time.Time
	// SettlementEventPublished gates settlement-event re-publishing
	// (FR3.4).
	SettlementEventPublished bool `gorm:"not null;default:false"`
}

// TableName overrides the default GORM table name.
func (UsageRecord) TableName() string { return "usage_records" }

// RequestLog is one per-request metadata log (feature #12, AD1). It is
// a separate table from vouchers: vouchers are immutable audit atoms
// with 90-day retention and settlement coupling; request logs are
// diagnostic metadata with 30-day retention and no settlement role.
// Rows are immutable; the request-log retention runner is the only
// deleter (AD4).
type RequestLog struct {
	// ID is the server-generated UUID v4, exposed as request_log_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// RequestID is the inference request id — the idempotency key
	// (shared with the voucher).
	RequestID string `gorm:"size:128;not null;uniqueIndex"`
	// OrganizationID is the owning organization.
	OrganizationID string `gorm:"size:64;not null;index:idx_request_logs_org_created,priority:1"`
	// APIKeyID is the API Key that made the request.
	APIKeyID string `gorm:"size:64;not null;index:idx_request_logs_key_created,priority:1"`
	// OwnerUserID is the key's personal owner at request time.
	OwnerUserID *string `gorm:"type:uuid;index:idx_request_logs_org_owner,priority:2"`
	// ProjectID is the key's project at request time.
	ProjectID *string `gorm:"size:64;index:idx_request_logs_org_project,priority:2"`
	// ModelID is the model that served the request.
	ModelID string `gorm:"size:128;not null"`
	// ServiceID optionally names the inference service.
	ServiceID *string `gorm:"size:64"`
	// PromptTokens is the input token count.
	PromptTokens int64 `gorm:"not null;default:0"`
	// CompletionTokens is the output token count.
	CompletionTokens int64 `gorm:"not null;default:0"`
	// CachedTokens is the cache-hit token count.
	CachedTokens int64 `gorm:"not null;default:0"`
	// ReasoningTokens is the reasoning-trace token count.
	ReasoningTokens int64 `gorm:"not null;default:0"`
	// LatencyMs is the request latency in milliseconds.
	LatencyMs int64 `gorm:"not null;default:0"`
	// Status is success / error / streaming.
	Status string `gorm:"size:16;not null;index"`
	// Error is the failure reason; empty on success.
	Error string `gorm:"size:512;not null;default:''"`
	// CreatedAt is the log write time (UTC).
	CreatedAt time.Time `gorm:"not null;index:idx_request_logs_org_created,priority:2;index:idx_request_logs_key_created,priority:2"`
}

// TableName returns the request_logs table name.
func (RequestLog) TableName() string { return "request_logs" }

// HourBucket identifies one settlement bucket: the API key and the
// hour-start unix seconds its unsettled vouchers fall into.
type HourBucket struct {
	APIKeyID       string
	OrganizationID string
	PeriodStart    int64
	// OwnerUserID and ProjectID are the key's attribution snapshot for
	// the bucket's first observed voucher.
	OwnerUserID *string
	ProjectID   *string
}

// UsageSummaryRow is one aggregated row of the usage summary query:
// token sums, request count and the settled/pending hour split for one
// group (api key or model).
type UsageSummaryRow struct {
	GroupKey         string
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ReasoningTokens  int64
	RequestCount     int64
	SettledHours     int64
	PendingHours     int64
}

// VoucherFilter scopes a voucher list query (FR4.2).
type VoucherFilter struct {
	OrganizationID string
	APIKeyID       string
	ModelID        string
	OwnerUserID    string
	ProjectID      string
	Since          int64
	Until          int64
	Offset         int
	Limit          int
}

// UsageRecordFilter scopes a usage-record list query (FR4.4).
type UsageRecordFilter struct {
	OrganizationID string
	APIKeyID       string
	OwnerUserID    string
	ProjectID      string
	Since          int64
	Until          int64
	Offset         int
	Limit          int
}

// optionalID converts an empty ownership id to a NULL column value, so
// a shared key and an unassigned project stay distinguishable from an
// empty string.
func optionalID(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
