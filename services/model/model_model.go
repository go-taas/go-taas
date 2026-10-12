package model

import "time"

// Model is the GORM model of the models table and the single source of
// truth for its schema (created by AutoMigrate, no hand-written DDL).
//
// The catalog identity is the name; versions are separate rows in
// model_versions. latest_version is derived at read time (max version by
// the ordering rule), not stored — a stored latest_version column would
// need a transactional update on every registration and could drift.
type Model struct {
	// ID is the server-generated UUID v4 exposed as model_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// Name is the model name, 1-128 chars, unique across the catalog.
	Name string `gorm:"size:128;not null;uniqueIndex"`
	// Description is a free-text description, at most 1024 chars.
	Description string `gorm:"size:1024;not null;default:''"`
	// External marks a published external model (feature #48). An external
	// model has no model_versions row and stays version-less.
	External bool `gorm:"not null;default:false"`
	// ProviderID is the owning external provider's id (empty for a
	// self-hosted model). It is a plain string column (not uuid) so the
	// empty default is valid on PostgreSQL.
	ProviderID string `gorm:"size:64;not null;default:'';index"`
	// UpstreamModelID is the provider's model id for an external model.
	UpstreamModelID string `gorm:"size:128;not null;default:''"`
	// Published is false for an unpublished external model. It defaults to
	// true so every existing self-hosted model keeps today's behavior with
	// no backfill (feature #48, Section 4.3).
	Published bool `gorm:"not null;default:true"`
	// CreatedAt is the first registration time (UTC).
	CreatedAt time.Time
	// UpdatedAt is the last touch (new version registered).
	UpdatedAt time.Time
}

// TableName returns the table name of Model.
func (Model) TableName() string { return "models" }

// Version is the GORM model of the model_versions table.
type Version struct {
	// ID is the server-generated UUID v4.
	ID string `gorm:"primaryKey;type:uuid"`
	// ModelID is the owning model's id.
	ModelID string `gorm:"type:uuid;not null;uniqueIndex:idx_model_versions_model_version,priority:1;uniqueIndex:idx_model_versions_active,priority:1"`
	// Version is the version string, 1-64 chars.
	Version string `gorm:"size:64;not null;uniqueIndex:idx_model_versions_model_version,priority:2;index:idx_model_versions_model_created,priority:2,sort:DESC"`
	// WeightPath is the object-storage path of the model weights,
	// syntax-validated at registration (existence is checked by the
	// controller at deploy time).
	WeightPath string `gorm:"size:512;not null"`
	// IsActive marks this version as the active/default version for new
	// deployments (feature #32, AD2). At most one version per model is
	// active, enforced by the partial unique index (model_id) WHERE
	// is_active.
	IsActive bool `gorm:"not null;default:false;uniqueIndex:idx_model_versions_active,priority:2,where:is_active"`
	// CreatedAt is the registration time (UTC).
	CreatedAt time.Time `gorm:"index:idx_model_versions_model_created,priority:1"`
}

// TableName returns the table name of Version.
func (Version) TableName() string { return "model_versions" }
