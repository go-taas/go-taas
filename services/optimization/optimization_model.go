// Package optimization implements the cost-optimization-recommendations
// module (feature #50). It owns the deterministic rule engine (R1
// cheaper-model substitution, R2 batch migration, R3 idle-service
// scale-to-zero, R4 cache-hit improvement), the recommendation store
// and the dismiss/restore lifecycle. The admin surface serves the
// fleet worklist under /api/v1/admin/optimization/*; the end-user
// surface serves the tenant's own masked projection under
// /api/v1/optimization/*. The engine reads sibling-module data
// read-only and writes nothing outside its own tables (the advice-only
// invariant).
package optimization

import (
	"time"

	"gorm.io/datatypes"
)

// Rule identifiers (design FR1.1).
const (
	RuleSubstitution = "r1_substitution"
	RuleBatch        = "r2_batch"
	RuleIdleService  = "r3_idle_service"
	RuleCache        = "r4_cache"
)

// Rules is the ordered list of the four rule identifiers.
var Rules = []string{RuleSubstitution, RuleBatch, RuleIdleService, RuleCache}

// UserRules is the user-surface rule subset (R3 is admin-only, AD8).
var UserRules = []string{RuleSubstitution, RuleBatch, RuleCache}

// Target type values (design FR1.1).
const (
	TargetOrgModel = "org_model"
	TargetAPIKey   = "api_key"
	TargetService  = "service"
)

// Surface values (AD11): the dismiss state is keyed (surface,
// fingerprint) so each surface keeps its own dismiss lifecycle.
const (
	SurfaceAdmin = "admin"
	SurfaceUser  = "user"
)

// Status values (derived at read time, never stored).
const (
	StatusActive    = "active"
	StatusDismissed = "dismissed"
	StatusAll       = "all"
)

// Dismiss reason values (design FR3.1).
const (
	ReasonNotApplicable = "not_applicable"
	ReasonPlanned       = "planned"
	ReasonOther         = "other"
)

// DismissReasons is the closed set of valid dismiss reasons.
var DismissReasons = []string{ReasonNotApplicable, ReasonPlanned, ReasonOther}

// Run trigger values.
const (
	TriggerTicker = "ticker"
	TriggerAdmin  = "admin"
)

// Bounds constants (architecture Section 4).
const (
	// maxDismissNoteLen bounds the dismiss free-text note.
	maxDismissNoteLen = 256
	// maxRunErrorLen bounds the run error text.
	maxRunErrorLen = 512
	// defaultCardType is the price-matrix fallback cell (billing D6).
	defaultCardType = "default"
)

// Recommendation is the GORM model of the optimization_recommendations
// table (AD11): one row is one computed condition in the latest
// snapshot. The fingerprint is the stable identity; recommendation_id
// and first_seen_at are preserved across refreshes by the upsert. The
// status is derived at read time from the dismissals table, never
// stored.
type Recommendation struct {
	// ID is the server-generated UUID v4 exposed as recommendation_id;
	// stable while the fingerprint persists.
	ID string `gorm:"primaryKey;type:uuid"`
	// Fingerprint is sha256(rule | target_type | target_id | suggestion)
	// hex; the upsert key.
	Fingerprint string `gorm:"size:64;not null;uniqueIndex"`
	// Rule is r1_substitution | r2_batch | r3_idle_service | r4_cache.
	Rule string `gorm:"size:32;not null;index"`
	// TargetType is org_model | api_key | service.
	TargetType string `gorm:"size:16;not null"`
	// TargetID is org_id/model_id (R1/R4), the api-key id (R2) or the
	// service id (R3).
	TargetID string `gorm:"size:256;not null"`
	// TargetDisplay is the denormalized display name.
	TargetDisplay string `gorm:"size:256;not null;default:''"`
	// OrganizationID is the denormalized filter column (FR2.1).
	OrganizationID string `gorm:"size:64;not null;index"`
	// ModelID is the denormalized filter column (R1/R2/R4; empty for
	// R3).
	ModelID string `gorm:"size:128;not null;default:'';index"`
	// APIKeyID is denormalized (R2).
	APIKeyID string `gorm:"size:64;not null;default:''"`
	// ServiceID is denormalized (R3).
	ServiceID string `gorm:"size:64;not null;default:''"`
	// Suggestion is the proposed change, human-readable.
	Suggestion string `gorm:"size:512;not null"`
	// EstimatedMonthlySaving is rounded to 2 decimals at compute time;
	// serialized as a JSON string with 2 places.
	EstimatedMonthlySaving float64 `gorm:"not null;default:0"`
	// Currency is the platform's single settlement currency.
	Currency string `gorm:"size:8;not null;default:''"`
	// Inputs is the rule-specific inputs object (the drawer's "Why"
	// table).
	Inputs datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// Arithmetic is the formula with the numbers substituted.
	Arithmetic string `gorm:"size:512;not null;default:''"`
	// Alternatives is R1's runner-up list, saving-desc; "[]" otherwise.
	Alternatives datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'"`
	// ActingPageAdmin is the admin acting link {route, label} (AD16).
	ActingPageAdmin datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// ActingPageUser is the end-user acting link {route, label} (AD16).
	ActingPageUser datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// FirstSeenAt is preserved by the upsert: the first time this
	// fingerprint appeared.
	FirstSeenAt time.Time
	// LastComputedAt is bumped every refresh that recomputes the
	// fingerprint.
	LastComputedAt time.Time
	// CreatedAt / UpdatedAt are UTC timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName overrides the default GORM table name.
func (Recommendation) TableName() string { return "optimization_recommendations" }

// Dismissal is the GORM model of the optimization_dismissals table
// (AD11): one row is one surface's dismissal of one fingerprint. Rows
// survive snapshot replacement and condition disappearance — the
// fingerprint, not the recommendation row, is the identity (FR3.3).
type Dismissal struct {
	// ID is the server-generated UUID v4.
	ID string `gorm:"primaryKey;type:uuid"`
	// Surface is admin | user.
	Surface string `gorm:"size:8;not null;uniqueIndex:idx_optimization_dismissals_surface_fp,priority:1;index:idx_optimization_dismissals_surface_time,priority:1"`
	// Fingerprint is the other half of the composite unique key.
	Fingerprint string `gorm:"size:64;not null;uniqueIndex:idx_optimization_dismissals_surface_fp,priority:2"`
	// Reason is not_applicable | planned | other.
	Reason string `gorm:"size:16;not null"`
	// Note is required for other, empty otherwise; <= 256 chars.
	Note string `gorm:"size:256;not null;default:''"`
	// DismissedBy is the acting user id (admin or tenant actor per
	// surface).
	DismissedBy string `gorm:"size:64;not null;default:''"`
	// DismissedAt is the dismissal time (UTC).
	DismissedAt time.Time `gorm:"index:idx_optimization_dismissals_surface_time,priority:2"`
}

// TableName overrides the default GORM table name.
func (Dismissal) TableName() string { return "optimization_dismissals" }

// Run is the GORM model of the optimization_runs table (AD11): one row
// per engine pass — the refresh log and the source of the "last
// refresh" card, correct even when a pass emits zero recommendations.
type Run struct {
	// ID is the server-generated UUID v4.
	ID string `gorm:"primaryKey;type:uuid"`
	// StartedAt / FinishedAt are UTC timestamps.
	StartedAt  time.Time
	FinishedAt time.Time
	// DurationMs is the pass duration in milliseconds.
	DurationMs int64 `gorm:"not null;default:0"`
	// RulesEvaluated is the rule count run (4).
	RulesEvaluated int `gorm:"not null;default:0"`
	// RecommendationsEmitted is the size of the new snapshot.
	RecommendationsEmitted int `gorm:"not null;default:0"`
	// Trigger is ticker | admin.
	Trigger string `gorm:"size:16;not null;default:''"`
	// Success reports whether the pass completed.
	Success bool `gorm:"not null;default:false"`
	// Error is the failure text; empty on success.
	Error string `gorm:"size:512;not null;default:''"`
}

// TableName overrides the default GORM table name.
func (Run) TableName() string { return "optimization_runs" }
