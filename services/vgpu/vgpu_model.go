package vgpu

import (
	"time"

	"gorm.io/datatypes"
)

// Placement policies (closed set, AD5).
const (
	PolicyBinpack = "binpack"
	PolicySpread  = "spread"
	PolicyMutex   = "mutex"
)

// VGPUPool is the GORM model of the vgpu_pools table (feature #52,
// Section 4.1). It is the single source of truth for its schema
// (created by AutoMigrate, no hand-written DDL).
type VGPUPool struct { //nolint:revive // vgpu.VGPUPool is the documented domain name
	// ID is the server-generated UUID v4 exposed as pool_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// Name is the DNS-safe pool name, 1-63 chars, unique.
	Name string `gorm:"size:63;not null;uniqueIndex"`
	// LabelKey is the node-label key that designates pool membership.
	LabelKey string `gorm:"size:128;not null"`
	// LabelValue is the node-label value that designates membership.
	LabelValue string `gorm:"size:128;not null"`
	// PlacementPolicy is the pool-level scheduling hint passed to HAMI
	// (binpack | spread | mutex).
	PlacementPolicy string `gorm:"size:16;not null;default:'binpack'"`
	// Templates holds the slice templates as a JSON array (AD3). The
	// jsonb type applies on PostgreSQL; SQLite (tests) stores the same
	// JSON text.
	Templates datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'"`
	// CreatedAt is the creation time (UTC).
	CreatedAt time.Time
	// UpdatedAt is the last update time.
	UpdatedAt time.Time
}

// TableName returns the table name of VGPUPool.
func (VGPUPool) TableName() string { return "vgpu_pools" }

// SliceTemplate is one bounded carve-up of a physical GPU (AD3). It is
// stored as a JSON element inside VGPUPool.Templates.
type SliceTemplate struct {
	Name        string `json:"name"`
	MemoryGiB   int64  `json:"memory_gib"`
	ComputePct  int64  `json:"compute_pct"`
	Description string `json:"description,omitempty"`
}