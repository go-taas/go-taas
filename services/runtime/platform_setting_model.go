package runtime

import (
	"strings"
	"time"
)

// platformSettingID is the fixed singleton row id of platform_settings.
const platformSettingID = "platform"

// Gateway schemes a platform setting may declare.
const (
	GatewaySchemeHTTP  = "http"
	GatewaySchemeHTTPS = "https"
)

// PlatformSetting is the GORM model of the singleton platform_settings
// row: operator-editable values that describe the installation rather
// than a tenant.
type PlatformSetting struct {
	// ID is the singleton row id (always "platform").
	ID string `gorm:"primaryKey;size:32"`
	// InferenceGatewayHostname is the data-plane hostname users call,
	// for example "api.example.com". Empty means fall back to the
	// configured endpoint or to the discovered gateway.
	InferenceGatewayHostname string `gorm:"size:255;not null;default:''"`
	// InferenceGatewayScheme is "http" or "https". It is stored because
	// it is part of the URL users are handed.
	InferenceGatewayScheme string `gorm:"size:8;not null;default:'https'"`
	// InferenceGatewayInternalURL is the in-cluster URL the platform
	// itself uses to reach the gateway. Empty means discover it.
	InferenceGatewayInternalURL string `gorm:"size:255;not null;default:''"`
	// UpdatedBy is the acting admin user's label (empty in the
	// transitional session-less path).
	UpdatedBy string `gorm:"size:255;not null;default:''"`
	// Timezone is the IANA zone every console renders timestamps in. Empty
	// means the viewer's own zone.
	Timezone string `gorm:"size:64;not null;default:''"`
	// DefaultLanguage is the console language a first-time visitor gets
	// ("en" or "zh").
	DefaultLanguage string `gorm:"size:8;not null;default:'zh'"`
	// Currency is the ISO-4217 code every console shows amounts in.
	Currency string `gorm:"size:8;not null;default:'CNY'"`
	// UpdatedAt is the last change time.
	UpdatedAt time.Time
	// CreatedAt is the row creation time.
	CreatedAt time.Time
}

// TableName overrides the default GORM table name.
func (PlatformSetting) TableName() string { return "platform_settings" }

// GatewayScheme returns the stored scheme, defaulting to https: a
// data-plane URL is public, so plaintext must be an explicit choice.
func (p *PlatformSetting) GatewayScheme() string {
	if strings.EqualFold(strings.TrimSpace(p.InferenceGatewayScheme), GatewaySchemeHTTP) {
		return GatewaySchemeHTTP
	}
	return GatewaySchemeHTTPS
}

// InferenceBaseURL composes the public base URL users are given
// ("<scheme>://<hostname>/v1"), or "" when no hostname is configured.
func (p *PlatformSetting) InferenceBaseURL() string {
	host := strings.TrimSpace(p.InferenceGatewayHostname)
	if host == "" {
		return ""
	}
	return p.GatewayScheme() + "://" + host + "/v1"
}
