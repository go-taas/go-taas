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
	ModelID string `gorm:"type:uuid;not null;uniqueIndex:idx_model_versions_model_version,priority:1"`
	// Version is the version string, 1-64 chars.
	Version string `gorm:"size:64;not null;uniqueIndex:idx_model_versions_model_version,priority:2;index:idx_model_versions_model_created,priority:2,sort:DESC"`
	// WeightPath is the object-storage path of the model weights,
	// syntax-validated at registration (existence is checked by the
	// controller at deploy time).
	WeightPath string `gorm:"size:512;not null"`
	// CreatedAt is the registration time (UTC).
	CreatedAt time.Time `gorm:"index:idx_model_versions_model_created,priority:1"`
}

// TableName returns the table name of Version.
func (Version) TableName() string { return "model_versions" }
