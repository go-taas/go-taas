// Package runtime implements the distributed-inference-runtime module
// (feature #47). It owns the platform serving scheme (persisted in the
// single-row runtime_settings table) and the in-memory runtime-component
// and registered-model projections refreshed by the controller's periodic
// full snapshot. The module is admin-surface only
// (/api/v1/admin/runtime/*).
package runtime

import "time"

// Serving scheme values (AD3). The scheme is a platform-level setting.
const (
	// SchemePlain is the default plain-Deployment serving scheme.
	SchemePlain = "plain"
	// SchemeAIBrix selects the AIBrix distributed serving scheme.
	SchemeAIBrix = "aibrix"
)

// runtimeSettingID is the fixed singleton row id.
const runtimeSettingID = "runtime"

// RuntimeSetting is the GORM model of the singleton runtime_settings
// table (AD6). The row id is always "runtime" so there is exactly one row.
type RuntimeSetting struct { //nolint:revive // runtime.RuntimeSetting is the documented domain name
	// ID is the singleton row id (always "runtime").
	ID string `gorm:"primaryKey;size:32"`
	// Scheme is the persisted serving scheme (plain | aibrix).
	Scheme string `gorm:"size:16;not null;default:'plain'"`
	// UpdatedBy is the acting admin user id (empty in the transitional
	// session-less path).
	UpdatedBy string `gorm:"size:64;not null;default:''"`
	// UpdatedAt is the last change time.
	UpdatedAt time.Time
	// CreatedAt is the row creation time.
	CreatedAt time.Time
}

// TableName overrides the default GORM table name.
func (RuntimeSetting) TableName() string { return "runtime_settings" }

// validScheme reports whether s is a supported serving scheme.
func validScheme(s string) bool { return s == SchemePlain || s == SchemeAIBrix }
