// Package account implements the data-export job lifecycle and the
// account-data bundle (feature #41). It serves the end-user Data Export
// page, creating/monitoring/downloading GDPR-style data exports of a
// tenant's usage, billing, request logs, and account profile.
package account

import "time"

// Data export types (AD3).
const (
	ExportTypeUsage       = "usage"
	ExportTypeBilling     = "billing"
	ExportTypeRequestLogs = "request_logs"
	ExportTypeAccount     = "account"
)

// Data export formats (AD3).
const (
	ExportFormatJSON = "json"
	ExportFormatCSV  = "csv"
)

// Data export statuses (AD2).
const (
	ExportStatusPending = "pending"
	ExportStatusReady   = "ready"
	ExportStatusFailed  = "failed"
)

// DataExport is the GORM model for the data_exports table (AD2).
type DataExport struct { //nolint:revive // account.DataExport is the documented domain name
	ID             string `gorm:"primaryKey"`
	OrganizationID string `gorm:"size:64;not null;index"`
	Type           string `gorm:"size:16;not null"`
	Since          int64  `gorm:"not null;default:0"`
	Until          int64  `gorm:"not null;default:0"`
	Format         string `gorm:"size:8;not null"`
	Status         string `gorm:"size:8;not null;index"`
	RowCount       int64  `gorm:"not null;default:0"`
	File           string `gorm:"type:text"`
	Error          string `gorm:"size:512;not null;default:''"`
	CreatedAt      time.Time
}

// TableName overrides the default GORM table name.
func (DataExport) TableName() string { return "data_exports" }

// validExportType reports whether a type is supported.
func validExportType(t string) bool {
	switch t {
	case ExportTypeUsage, ExportTypeBilling, ExportTypeRequestLogs, ExportTypeAccount:
		return true
	default:
		return false
	}
}

// validExportFormat reports whether a format is supported for a type.
// The account type is always JSON (AD3).
func validExportFormat(t, format string) bool {
	if t == ExportTypeAccount {
		return format == ExportFormatJSON
	}
	return format == ExportFormatJSON || format == ExportFormatCSV
}