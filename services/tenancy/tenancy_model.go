// Package tenancy implements the tenancy service: the organization and
// project directory, lifecycle state transitions, the first-boot
// default-organization seed, and the OrgGuard read interface consumed
// by the auth/infer/metering/billing modules (feature #6).
package tenancy

import "time"

// Lifecycle states shared by organizations and projects (D2/D3).
const (
	// StateActive is the normal operating state.
	StateActive = "active"
	// StateDisabled blocks new resource creation; history stays
	// readable.
	StateDisabled = "disabled"
)

// Field length limits (architecture Section 4.3 validation matrix).
const (
	maxIDLen          = 64
	maxDisplayNameLen = 128
	maxDescriptionLen = 1024
)

// Organization is a tenant row. The id is caller-supplied and
// immutable; display name and description are mutable; state moves
// only through the disable/enable endpoints (D1/D2).
type Organization struct {
	ID          string    `gorm:"primaryKey;size:64"`
	DisplayName string    `gorm:"size:128;not null"`
	Description string    `gorm:"size:1024;not null;default:''"`
	State       string    `gorm:"size:16;not null;default:'active'"`
	CreatedAt   time.Time `gorm:"index"`
	UpdatedAt   time.Time
}

// TableName implements the GORM Tabler interface.
func (Organization) TableName() string { return "organizations" }

// Project is a directory row under an organization. The id is
// caller-supplied, globally unique, and immutable (architecture
// Section 3.2); no resource table references it in v1 (D6).
type Project struct {
	ID             string    `gorm:"primaryKey;size:64"`
	OrganizationID string    `gorm:"size:64;not null;index:idx_projects_org_created,priority:1"`
	DisplayName    string    `gorm:"size:128;not null"`
	Description    string    `gorm:"size:1024;not null;default:''"`
	State          string    `gorm:"size:16;not null;default:'active'"`
	CreatedAt      time.Time `gorm:"index:idx_projects_org_created,priority:2,sort:DESC"`
	UpdatedAt      time.Time
}

// TableName implements the GORM Tabler interface.
func (Project) TableName() string { return "projects" }
