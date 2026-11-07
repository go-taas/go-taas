package infer

import (
	"time"

	"gorm.io/datatypes"
)

// Deployment event types (feature #34 AD2). The create/update/scale/
// rollback/delete event types are shared with the change-publisher
// constants (EventTypeUpsert/EventTypeDelete/EventTypeUpdateVersion/
// EventTypeRollback); the deployment trail uses its own closed set.
const (
	DeploymentEventCreate   = "create"
	DeploymentEventUpdate   = "update"
	DeploymentEventScale    = "scale"
	DeploymentEventRollback = "rollback"
	DeploymentEventDelete   = "delete"
	DeploymentEventStop     = "stop"
	DeploymentEventStart    = "start"
)

// DeploymentEvent is the GORM model of the deployment_events table and
// the single source of truth for its schema (feature #34, AD2). It
// records the inference-service lifecycle trail with a field-level
// before/after diff, complementing the general audit_events spine.
type DeploymentEvent struct {
	// ID is the server-generated UUID v4 exposed as event_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// ServiceID is the inference service the event belongs to.
	ServiceID string `gorm:"type:uuid;not null;index:idx_deployment_events_service_created,priority:1"`
	// ServiceName is the service name (denormalized for display).
	ServiceName string `gorm:"size:63;not null"`
	// EventType is create / update / scale / rollback / delete.
	EventType string `gorm:"size:16;not null;index"`
	// Actor is the user id or "system".
	Actor string `gorm:"size:128;not null"`
	// Before is the field-level diff before (mutable spec fields).
	Before datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// After is the field-level diff after (mutable spec fields).
	After datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// CreatedAt is the event time (UTC).
	CreatedAt time.Time `gorm:"index:idx_deployment_events_service_created,priority:2,sort:DESC;index:idx_deployment_events_created,sort:DESC"`
}

// TableName returns the table name of DeploymentEvent.
func (DeploymentEvent) TableName() string { return "deployment_events" }
