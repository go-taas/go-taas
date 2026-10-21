package fault

import (
	"time"

	"gorm.io/datatypes"
)

// Incident lifecycle states (closed set, feature #53).
const (
	StatusDetected   = "detected"
	StatusDiagnosing = "diagnosing"
	StatusRecovering = "recovering"
	StatusResolved   = "resolved"
	StatusFailed     = "failed"
)

// Severity values (closed set).
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Root-cause categories (closed set, AD12).
const (
	CauseServiceCrashLoop               = "service_crash_loop"
	CauseNodeNotReady                   = "node_not_ready"
	CauseAcceleratorDevicePluginFailure = "accelerator_device_plugin"
	CauseOOM                            = "oom"
	CauseImagePullFailure               = "image_pull_failure"
	CauseUnknown                        = "unknown"
)

// Target types (closed set).
const (
	TargetService     = "service"
	TargetNode        = "node"
	TargetAccelerator = "accelerator"
)

// Step types (AD11).
const (
	StepDetected          = "detected"
	StepDiagnosed         = "diagnosed"
	StepRecoveryStarted   = "recovery_started"
	StepRecoverySucceeded = "recovery_succeeded"
	StepRecoveryFailed    = "recovery_failed"
	StepResolved          = "resolved"
	StepOverridden        = "overridden"
	StepManualAction      = "manual_action"
)

// Recovery actions (AD14).
const (
	ActionRestart    = "restart"
	ActionReschedule = "reschedule"
	ActionDrain      = "drain"
)

// Recovery action statuses.
const (
	ActionStatusPending    = "pending"
	ActionStatusRunning    = "running"
	ActionStatusSucceeded  = "succeeded"
	ActionStatusFailed     = "failed"
	ActionStatusOverridden = "overridden"
)

// Incident is the GORM model of the incidents table (feature #53,
// Section 4.1). It is the single source of truth for its schema
// (created by AutoMigrate, no hand-written DDL).
type Incident struct {
	// ID is the server-generated UUID v4 exposed as incident_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// Severity is critical | warning | info.
	Severity string `gorm:"size:16;not null"`
	// Status is the lifecycle state.
	Status string `gorm:"size:16;not null;default:'detected';index:idx_incidents_status_started,priority:1"`
	// RootCause is the classified root cause.
	RootCause string `gorm:"size:32;not null"`
	// TargetType is service | node | accelerator.
	TargetType string `gorm:"size:16;not null;index:idx_incidents_target,priority:1"`
	// TargetName is the affected service/node/accelerator name.
	TargetName string `gorm:"size:128;not null;index:idx_incidents_target,priority:2"`
	// StartedAt is the detection time (UTC).
	StartedAt time.Time `gorm:"index:idx_incidents_status_started,priority:2,sort:DESC"`
	// ResolvedAt is the resolution time; NULL while active.
	ResolvedAt *time.Time
	// AutoRecovered is whether an automatic recovery action succeeded.
	AutoRecovered bool `gorm:"not null;default:false"`
	// CreatedAt is the creation time (UTC).
	CreatedAt time.Time
	// UpdatedAt is the last status change.
	UpdatedAt time.Time
}

// TableName returns the table name of Incident.
func (Incident) TableName() string { return "incidents" }

// IncidentStep is the GORM model of the incident_steps table (feature
// #53, Section 4.2).
type IncidentStep struct {
	// ID is the server-generated UUID v4.
	ID string `gorm:"primaryKey;type:uuid"`
	// IncidentID is the parent incident.
	IncidentID string `gorm:"type:uuid;not null;index:idx_incident_steps_incident,priority:1"`
	// StepType is the timeline step type.
	StepType string `gorm:"size:16;not null"`
	// Actor is system or an operator user id.
	Actor string `gorm:"size:128;not null"`
	// Detail is the step's detail.
	Detail string `gorm:"size:512;not null"`
	// CreatedAt is the step time (UTC).
	CreatedAt time.Time `gorm:"index:idx_incident_steps_incident,priority:2"`
}

// TableName returns the table name of IncidentStep.
func (IncidentStep) TableName() string { return "incident_steps" }

// FaultPolicy is the GORM model of the fault_policy table (feature #53,
// Section 4.3). It is a single-row policy (AD15).
type FaultPolicy struct { //nolint:revive // fault.FaultPolicy is the documented domain name
	// ID is always 1 (single-row policy).
	ID int `gorm:"primaryKey"`
	// AutoRestart is the per-category auto-restart toggle map.
	AutoRestart datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// AutoReschedule is the per-category auto-reschedule toggle map.
	AutoReschedule datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// DrainAlwaysConfirm is always true (FR4.4).
	DrainAlwaysConfirm bool `gorm:"not null;default:true"`
	// UpdatedAt is the last change.
	UpdatedAt time.Time
}

// TableName returns the table name of FaultPolicy.
func (FaultPolicy) TableName() string { return "fault_policy" }
