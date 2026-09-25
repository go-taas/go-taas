package audit

import (
	"time"
)

// AuditEvent is one control-plane mutation or auth event (feature #15,
// AD1/AD2). It is a separate table from request_logs: request logs are
// per-inference data-plane diagnostics (30-day retention); audit events
// are control-plane actions (365-day retention, compliance value). Rows
// are immutable — no API updates or deletes them; the audit retention
// runner is the only deleter (AD6).
type AuditEvent struct { //nolint:revive // audit.AuditEvent is the domain name
	// ID is the server-generated UUID v4, exposed as audit_event_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// OrganizationID is the owning organization; empty for platform-level
	// events (e.g. a platform admin acting outside any org).
	OrganizationID string `gorm:"size:64;index:idx_audit_events_org_created,priority:1"`
	// ActorUserID is the user who performed the action; "system" for
	// system-initiated events.
	ActorUserID string `gorm:"size:64;not null;index:idx_audit_events_actor_created,priority:1"`
	// ActorType is user / system / api_key.
	ActorType string `gorm:"size:16;not null"`
	// Action is the curated action name (AD4), e.g. "api_key.revoke".
	Action string `gorm:"size:64;not null;index"`
	// ResourceType is the resource class, e.g. api_key, model, member,
	// price.
	ResourceType string `gorm:"size:32;not null;index"`
	// ResourceID is the resource instance id.
	ResourceID string `gorm:"size:128;not null"`
	// Result is success / failure (AD5).
	Result string `gorm:"size:16;not null;index"`
	// IPAddress is the caller's source IP, when known.
	IPAddress string `gorm:"size:64;not null;default:''"`
	// UserAgent is the caller's user agent, when known.
	UserAgent string `gorm:"size:512;not null;default:''"`
	// Metadata is extra context as a JSON string (e.g. the old/new value
	// of a changed field).
	Metadata string `gorm:"type:jsonb;not null;default:'{}'"`
	// CreatedAt is the event time (UTC).
	CreatedAt time.Time `gorm:"not null;index:idx_audit_events_org_created,priority:2;index:idx_audit_events_actor_created,priority:2"`
}

// TableName overrides the default GORM table name.
func (AuditEvent) TableName() string { return "audit_events" }

// AuditEventFilter scopes an audit-event list query (FR3.1/FR4.1).
type AuditEventFilter struct { //nolint:revive // audit.AuditEventFilter is the domain name
	OrganizationID string
	ActorUserID    string
	Action         string
	ResourceType   string
	Result         string
	Since          int64
	Until          int64
	Offset         int
	Limit          int
}
