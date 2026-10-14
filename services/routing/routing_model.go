// Package routing implements the intelligent-model-routing module
// (feature #49). It owns the routing-group registry (candidates, intent
// rules), the request-time virtual-name resolver, the decision log and
// the group grant list. The admin surface serves the registry under
// /api/v1/admin/routing-groups/*; the resolver is an internal RPC
// consumed by the data-plane gateway and the playground seam.
package routing

import (
	"database/sql/driver"
	"encoding/json"
	"time"
)

// Strategy values (design FR2).
const (
	StrategyManual          = "manual"
	StrategyWeighted        = "weighted"
	StrategyCostPriority    = "cost_priority"
	StrategyLatencyPriority = "latency_priority"
)

// Strategies is the ordered list of valid strategy values.
var Strategies = []string{StrategyManual, StrategyWeighted, StrategyCostPriority, StrategyLatencyPriority}

// Match type values (design FR4).
const (
	MatchKeyword = "keyword"
	MatchRegex   = "regex"
)

// Skip reasons (design FR3.6).
const (
	SkipReasonUnavailable  = "skipped_unavailable"
	SkipReasonUnauthorized = "skipped_unauthorized"
)

// Decision candidate outcomes (design FR5).
const (
	OutcomeServed          = "served"
	OutcomeFailedRetryable = "failed_retryable"
	OutcomeNotAttempted    = "not_attempted"
)

// Bounds constants (design §5.4 validation contract).
const (
	// maxDescriptionLen is the group description bound.
	maxDescriptionLen = 256
	// minCandidateEntries / maxCandidateEntries bound the candidate list.
	minCandidateEntries = 1
	maxCandidateEntries = 20
	// maxRuleEntries bounds the rule list (0 allowed).
	maxRuleEntries = 50
	// minWeight / maxWeight bound the candidate weight.
	minWeight = 1
	maxWeight = 100
	// defaultWeight is applied when the input weight is 0.
	defaultWeight = 10
	// minTier / maxTier bound the cost/latency tiers (0 = undeclared).
	minTier = 1
	maxTier = 5
	// missingTier is the sort key of an undeclared tier (design AD3).
	missingTier = 6
	// maxRuleNameLen bounds the rule name.
	maxRuleNameLen = 64
	// maxPatternLen bounds the rule pattern.
	maxPatternLen = 256
	// maxAttempts is the failover attempt budget (AD7).
	maxAttempts = 3
)

// Group is the GORM model of the routing_groups table. The schema is
// the single source of truth (AutoMigrate, no hand-written DDL).
type Group struct {
	// ID is the server-generated UUID v4 exposed as group_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// Name is the virtual model name tenants call: 3-64 chars matching
	// ^[a-z][a-z0-9-]{2,63}$, unique across groups and reserved against
	// real catalog model names.
	Name string `gorm:"size:64;not null;uniqueIndex"`
	// Description is the operator note, at most 256 chars.
	Description string `gorm:"size:256;not null;default:''"`
	// Strategy is manual | weighted | cost_priority | latency_priority.
	Strategy string `gorm:"size:32;not null;default:'weighted'"`
	// Enabled is the operator switch; a disabled group fails closed
	// with 13110 on resolution.
	Enabled bool `gorm:"not null;default:false"`
	// CreatedBy / UpdatedBy are the acting admin user ids.
	CreatedBy string `gorm:"size:64;not null;default:''"`
	UpdatedBy string `gorm:"size:64;not null;default:''"`
	// CreatedAt / UpdatedAt are UTC timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName overrides the default GORM table name.
func (Group) TableName() string { return "routing_groups" }

// Candidate is the GORM model of the routing_group_candidates table:
// one ordered real-model member of a group. The model_id uniqueness
// within a group is enforced by the composite unique index.
type Candidate struct {
	// ID is the server-generated UUID v4 exposed as candidate_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// GroupID is the owning group's id.
	GroupID string `gorm:"type:uuid;not null;uniqueIndex:idx_routing_candidates_group_model,priority:1;index"`
	// ModelID is the real catalog model id (never a group id).
	ModelID string `gorm:"size:64;not null;uniqueIndex:idx_routing_candidates_group_model,priority:2"`
	// ModelName is the denormalized catalog model name (decision-log
	// stability and list rendering without a join).
	ModelName string `gorm:"size:128;not null;default:''"`
	// Weight is the weighted-strategy draw probability (1-100).
	Weight int `gorm:"not null;default:10"`
	// CostTier is the operator-declared cost tier (1-5, 0 = undeclared).
	CostTier int `gorm:"not null;default:0"`
	// LatencyTier is the operator-declared latency tier (1-5, 0 =
	// undeclared).
	LatencyTier int `gorm:"not null;default:0"`
	// Pinned marks the manual strategy's single fixed candidate.
	Pinned bool `gorm:"not null;default:false"`
	// Position is the 0-based declared order.
	Position int `gorm:"not null;default:0"`
	// CreatedAt / UpdatedAt are UTC timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName overrides the default GORM table name.
func (Candidate) TableName() string { return "routing_group_candidates" }

// Rule is the GORM model of the routing_group_rules table: one ordered
// intent rule. The name uniqueness within a group is enforced by the
// composite unique index.
type Rule struct {
	// ID is the server-generated UUID v4 exposed as rule_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// GroupID is the owning group's id.
	GroupID string `gorm:"type:uuid;not null;uniqueIndex:idx_routing_rules_group_name,priority:1;index"`
	// Name is the rule name, 1-64 chars, unique within the group.
	Name string `gorm:"size:64;not null;uniqueIndex:idx_routing_rules_group_name,priority:2"`
	// MatchType is keyword | regex.
	MatchType string `gorm:"size:16;not null"`
	// Pattern is the keyword substring or the RE2 expression (1-256).
	Pattern string `gorm:"size:256;not null"`
	// TargetModelID is the candidate model id the rule routes to.
	TargetModelID string `gorm:"size:64;not null"`
	// TargetModelName is the denormalized target model name.
	TargetModelName string `gorm:"size:128;not null;default:''"`
	// Enabled is the rule switch; disabled rules never match.
	Enabled bool `gorm:"not null"`
	// Position is the 0-based evaluation order.
	Position int `gorm:"not null;default:0"`
	// CreatedAt / UpdatedAt are UTC timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName overrides the default GORM table name.
func (Rule) TableName() string { return "routing_group_rules" }

// GroupGrant is the GORM model of the routing_group_grants table: one
// organization's grant of a group (AD12). Grants live in their own
// table — never in model_authorizations — so the #13 default-allow
// rule over that table stays intact. Zero grants = all groups granted
// (the default-allow rule mirrored on groups).
type GroupGrant struct {
	// ID is the server-generated UUID v4.
	ID string `gorm:"primaryKey;type:uuid"`
	// GroupID is the granted group's id.
	GroupID string `gorm:"type:uuid;not null;uniqueIndex:idx_routing_group_grants_group_org,priority:1;index"`
	// OrganizationID is the granted organization.
	OrganizationID string `gorm:"size:64;not null;uniqueIndex:idx_routing_group_grants_group_org,priority:2;index"`
	// GrantedBy is the acting admin user id (audit column).
	GrantedBy string `gorm:"size:64;not null;default:''"`
	// CreatedAt is the grant time (UTC).
	CreatedAt time.Time
}

// TableName overrides the default GORM table name.
func (GroupGrant) TableName() string { return "routing_group_grants" }

// Decision is the GORM model of the routing_decisions table: one
// resolved virtual-name request (AD11). Group, rule and model names are
// denormalized so entries survive group/rule/model deletion.
type Decision struct {
	// ID is the server-generated UUID v4 exposed as decision_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// RequestID is the caller's request id.
	RequestID string `gorm:"size:64;not null;default:'';index"`
	// GroupID is the resolved group's id (survives group deletion).
	GroupID string `gorm:"type:uuid;not null;index"`
	// GroupName is the denormalized group name.
	GroupName string `gorm:"size:64;not null"`
	// Strategy is the group's strategy at decision time.
	Strategy string `gorm:"size:32;not null;index"`
	// MatchedRuleID is the matched rule's id (empty when none).
	MatchedRuleID string `gorm:"size:64;not null;default:''"`
	// MatchedRuleName is the denormalized matched rule name.
	MatchedRuleName string `gorm:"size:64;not null;default:''"`
	// SelectedModelID is the served model's id (empty on fail-closed).
	SelectedModelID string `gorm:"size:64;not null;default:'';index"`
	// SelectedModelName is the denormalized served model name.
	SelectedModelName string `gorm:"size:128;not null;default:''"`
	// Candidates is the candidates-considered jsonb (AD15): each entry
	// carries model_id, model_name, outcome and detail.
	Candidates DecisionCandidates `gorm:"type:jsonb"`
	// APIKeyID is the calling API key's id (empty for the playground
	// without a key).
	APIKeyID string `gorm:"size:64;not null;default:''"`
	// OrganizationID is the calling organization.
	OrganizationID string `gorm:"size:64;not null;default:'';index"`
	// CreatedAt is the decision time (UTC).
	CreatedAt time.Time `gorm:"index"`
}

// TableName overrides the default GORM table name.
func (Decision) TableName() string { return "routing_decisions" }

// DecisionCandidate is one entry of a decision's candidates-considered
// list (the proto's DecisionCandidate shape, persisted as jsonb).
type DecisionCandidate struct {
	ModelID   string `json:"model_id"`
	ModelName string `json:"model_name"`
	Outcome   string `json:"outcome"`
	Detail    string `json:"detail"`
}

// DecisionCandidates is the candidates-considered list persisted in a
// jsonb column. It is JSON-serialized so it works on PostgreSQL (jsonb)
// and SQLite (text).
type DecisionCandidates []*DecisionCandidate

// Value implements driver.Valuer.
func (c DecisionCandidates) Value() (driver.Value, error) {
	if c == nil {
		return "[]", nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner.
func (c *DecisionCandidates) Scan(src any) error {
	if src == nil {
		*c = nil
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return json.Unmarshal([]byte(v.(string)), c) //nolint:forcetypeassert // guarded by default
	}
	if len(raw) == 0 {
		*c = nil
		return nil
	}
	return json.Unmarshal(raw, c)
}
