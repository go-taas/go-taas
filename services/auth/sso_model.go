// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// SSO federation models (feature #7): the pluggable identity-provider
// framework, identity bindings, and the platform user account.
package auth

import "time"

// SSO provider types (D1).
const (
	ProviderTypeOIDC = "oidc"
	ProviderTypeSAML = "saml"
	ProviderTypeLDAP = "ldap"
)

// SSOProvider is an SSO identity provider configuration row. Protocol
// params are type-specific nullable columns; secrets (client_secret,
// bind_dn) are write-only and masked on read.
type SSOProvider struct {
	ID                 string `gorm:"primaryKey;size:64"`
	Type               string `gorm:"size:16;not null"`
	DisplayName        string `gorm:"size:128;not null"`
	Issuer             string `gorm:"size:512"`
	ClientID           string `gorm:"size:256"`
	ClientSecret       string `gorm:"size:512"`
	RedirectURI        string `gorm:"size:512"`
	Scopes             string `gorm:"size:512"`
	MetadataURL        string `gorm:"size:512"`
	EntityID           string `gorm:"size:512"`
	ACSUrl             string `gorm:"size:512"`
	Host               string `gorm:"size:256"`
	Port               int
	BindDN             string `gorm:"size:512"`
	BaseDN             string `gorm:"size:512"`
	UserFilter         string `gorm:"size:512"`
	Enabled            bool   `gorm:"not null;default:false"`
	DefaultOrg         string `gorm:"size:64"`
	AllowAutoProvision bool   `gorm:"not null;default:true"`
	AttributeMapping   string `gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TableName returns the sso_providers table name.
func (SSOProvider) TableName() string { return "sso_providers" }

// IdentityBinding links an external identity (provider_id +
// external_subject) to a platform user. The composite is the primary
// key of the external identity (D3).
type IdentityBinding struct {
	ID              string `gorm:"primaryKey;type:uuid"`
	ProviderID      string `gorm:"size:64;not null;index:idx_identity_bindings_provider;uniqueIndex:idx_identity_bindings_provider_subject,priority:1"`
	ExternalSubject string `gorm:"size:512;not null;uniqueIndex:idx_identity_bindings_provider_subject,priority:2"`
	UserID          string `gorm:"type:uuid;not null;index:idx_identity_bindings_user"`
	CreatedAt       time.Time
}

// TableName returns the identity_bindings table name.
func (IdentityBinding) TableName() string { return "identity_bindings" }

// User is the platform account model. Roles and org memberships are not
// columns — they are derived per-session from the attribute mapping and
// the organizations table (D7).
type User struct {
	ID           string `gorm:"primaryKey;type:uuid"`
	Username     string `gorm:"size:128;not null;uniqueIndex"`
	Email        string `gorm:"size:256"`
	PasswordHash string `gorm:"size:256"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName returns the users table name.
func (User) TableName() string { return "users" }
