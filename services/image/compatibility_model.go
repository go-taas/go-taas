package image

import (
	"context"
	"time"
)

// Compatibility statuses (closed set, feature #19 AD2).
const (
	StatusSupported    = "supported"
	StatusExperimental = "experimental"
	StatusUnsupported  = "unsupported"
)

// CardType is one (vendor, card_type) pair present in the fleet. It is
// the narrow read model the accelerator module returns to the image
// module for the compatibility matrix's card-type axis and the
// not_in_fleet derivation (feature #19, AD11).
type CardType struct {
	// Vendor is the accelerator vendor (nvidia, iluvatar, metax).
	Vendor string
	// CardType is the card type name (e.g. A800, H800).
	CardType string
}

// CardTypesProvider returns the live card-type set with vendors from the
// accelerator inventory projection cache. It is the narrow cross-module
// read seam (feature #19, Section 14.3): the image module stays free of
// an accelerator dependency, mirroring the WarmupTasksForNodeProvider
// pattern (accelerator-inventory §5.3). The accelerator module implements
// this interface.
type CardTypesProvider interface {
	// ListCardTypes returns the distinct (vendor, card_type) pairs
	// present in the fleet, sorted by vendor then card type.
	ListCardTypes(ctx context.Context) ([]CardType, error)
}

// CardTypesProviderFunc adapts a function to the CardTypesProvider
// interface.
type CardTypesProviderFunc func(ctx context.Context) ([]CardType, error)

// ListCardTypes implements CardTypesProvider.
func (f CardTypesProviderFunc) ListCardTypes(ctx context.Context) ([]CardType, error) {
	return f(ctx)
}

// CompatibilityCell is one (model, engine, card_type) matrix cell
// (feature #19, Section 5.1). The GORM model is the single source of
// truth for the schema (created by AutoMigrate, no hand-written DDL).
type CompatibilityCell struct {
	// ID is the server-generated UUID v4.
	ID string `gorm:"primaryKey;type:uuid"`
	// ModelID is the model dimension (FK → models.id, no hard FK — a
	// cell must survive a model being deleted).
	ModelID string `gorm:"size:64;not null;uniqueIndex:idx_compat_cell,priority:1"`
	// Engine is the engine dimension (a distinct engine value from
	// images).
	Engine string `gorm:"size:64;not null;uniqueIndex:idx_compat_cell,priority:2"`
	// CardType is the card-type dimension (a card type from the
	// accelerator inventory).
	CardType string `gorm:"size:64;not null;uniqueIndex:idx_compat_cell,priority:3"`
	// Status is the closed set supported/experimental/unsupported (AD2).
	Status string `gorm:"size:16;not null"`
	// Note is the optional operator note, ≤ 512 chars.
	Note string `gorm:"size:512;not null;default:''"`
	// CreatedAt is the first materialization time (UTC).
	CreatedAt time.Time
	// UpdatedAt is the last curation time (UTC).
	UpdatedAt time.Time
}

// TableName overrides the default GORM table name.
func (CompatibilityCell) TableName() string { return "compatibility_cells" }

// IsValidCompatibilityStatus reports whether status is in the closed
// set (AD2).
func IsValidCompatibilityStatus(status string) bool {
	switch status {
	case StatusSupported, StatusExperimental, StatusUnsupported:
		return true
	default:
		return false
	}
}
