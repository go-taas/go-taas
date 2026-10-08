package infer

import (
	"time"
)

// Routing policy bounds (feature #45, §4.1/§5.2).
const (
	// MaxRoutingTargets bounds the ordered target list.
	MaxRoutingTargets = 10
	// MinRoutingAttempts / MaxRoutingAttempts bound max_attempts
	// (includes the first attempt).
	MinRoutingAttempts = 1
	MaxRoutingAttempts = 3
	// RoutingRevisionHistory is the number of newest revisions kept for
	// the API/UI history window.
	RoutingRevisionHistory = 20
)

// Retry category values stored in routing_policies.retry_on (feature
// #45, §5.1). They mirror the proto RetryCategory enum.
const (
	RetryOnConnectTimeout = "connect_timeout"
	RetryOnHTTP429        = "http_429"
	RetryOnHTTP5XX        = "http_5xx"
)

// RoutingPolicyPublicationState values for the outbox-derived
// publication projection.
const (
	RoutingPublicationPending   = "pending"
	RoutingPublicationPublished = "published"
)

// RoutingPolicyState values for list summaries (feature #45, §5.1).
const (
	RoutingPolicyStateDefault     = "DEFAULT"
	RoutingPolicyStateEnabled     = "ENABLED"
	RoutingPolicyStateUnavailable = "UNAVAILABLE"
	RoutingPolicyStateUnknown     = "UNKNOWN"
)

// RoutingPolicy is the current routing policy for one catalog model
// (feature #45, §4.1). Absence of a row means default routing.
type RoutingPolicy struct {
	ModelID      string    `gorm:"primaryKey;type:uuid"`
	ModelVersion string    `gorm:"size:64;not null"`
	Enabled      bool      `gorm:"not null;default:false"`
	ServiceIDs   string    `gorm:"type:jsonb;not null;default:'[]'"`
	MaxAttempts  int       `gorm:"type:smallint;not null;default:1"`
	RetryOn      string    `gorm:"type:jsonb;not null;default:'[]'"`
	Revision     int64     `gorm:"not null;default:0"`
	UpdatedBy    string    `gorm:"size:64;not null;default:''"`
	CreatedAt    time.Time `gorm:"not null"`
	UpdatedAt    time.Time `gorm:"not null"`
}

// TableName implements the GORM table name.
func (RoutingPolicy) TableName() string { return "routing_policies" }

// RoutingPolicyRevision is one immutable history entry (feature #45,
// §4.1). The primary key is (model_id, revision).
type RoutingPolicyRevision struct {
	ModelID      string    `gorm:"primaryKey;type:uuid;index:idx_routing_policy_revisions_model_rev,priority:1"`
	Revision     int64     `gorm:"primaryKey;index:idx_routing_policy_revisions_model_rev,priority:2"`
	ModelVersion string    `gorm:"size:64;not null"`
	Enabled      bool      `gorm:"not null"`
	ServiceIDs   string    `gorm:"type:jsonb;not null"`
	MaxAttempts  int       `gorm:"type:smallint;not null"`
	RetryOn      string    `gorm:"type:jsonb;not null"`
	Actor        string    `gorm:"size:64;not null;default:''"`
	Summary      string    `gorm:"size:256;not null;default:''"`
	BeforeJSON   string    `gorm:"type:jsonb;not null;default:'{}'"`
	AfterJSON    string    `gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt    time.Time `gorm:"not null"`
}

// TableName implements the GORM table name.
func (RoutingPolicyRevision) TableName() string { return "routing_policy_revisions" }

// RoutingPolicyOutbox is the durable publication intent for one
// committed revision (feature #45, §4.1). Unique (model_id, revision)
// makes retries idempotent.
type RoutingPolicyOutbox struct {
	ID          string     `gorm:"primaryKey;type:uuid"`
	ModelID     string     `gorm:"type:uuid;not null;uniqueIndex:idx_routing_policy_outbox_model_rev,priority:1"`
	Revision    int64      `gorm:"not null;uniqueIndex:idx_routing_policy_outbox_model_rev,priority:2"`
	Snapshot    string     `gorm:"type:jsonb;not null"`
	CreatedAt   time.Time  `gorm:"not null"`
	PublishedAt *time.Time `gorm:"index"`
	Attempts    int        `gorm:"not null;default:0"`
	LastError   string     `gorm:"size:512;not null;default:''"`
}

// TableName implements the GORM table name.
func (RoutingPolicyOutbox) TableName() string { return "routing_policy_outbox" }
