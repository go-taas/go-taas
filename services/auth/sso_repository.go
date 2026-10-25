// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// SSO federation repository (feature #7): provider CRUD, identity
// binding CRUD, and user create/find on top of the generic repository
// base.
package auth

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// SSORepository persists SSO providers, identity bindings, and users.
type SSORepository struct {
	*database.BaseRepository[SSOProvider]
	db *database.Manager
}

// NewSSORepository constructs an SSORepository bound to a database
// Manager.
func NewSSORepository(db *gorm.DB) *SSORepository {
	mgr := database.NewManager(db)
	return &SSORepository{
		BaseRepository: database.NewBaseRepository[SSOProvider](mgr),
		db:             mgr,
	}
}

// ProviderFilter narrows ListProviders.
type ProviderFilter struct {
	Type    string // "" = all types
	Enabled *bool  // nil = all states
}

// BindingFilter narrows ListBindings.
type BindingFilter struct {
	ProviderID string // "" = all providers
	UserID     string // "" = all users
}

// UserFilter narrows ListUsers.
type UserFilter struct {
	State string // "" = all states
}

// CreateProvider inserts a new provider. A unique violation on the
// primary key maps to CodeSSOProviderExists.
//
// The insert uses a map so an explicit `allow_auto_provision: false` is
// preserved: GORM's `default:true` tag on the bool column would
// otherwise coerce a false zero-value back to true on a struct Create
// (feature-22 JIT-disabled providers).
//
// attribute_mapping is only included when non-empty so an omitted
// mapping falls back to the jsonb column default '{}' (BUG-UL-001):
// writing the empty string into the jsonb column is rejected by
// Postgres with SQLSTATE 22P02.
func (r *SSORepository) CreateProvider(ctx context.Context, p *SSOProvider) error {
	row := map[string]any{
		"id":                   p.ID,
		"type":                 p.Type,
		"display_name":         p.DisplayName,
		"issuer":               p.Issuer,
		"client_id":            p.ClientID,
		"client_secret":        p.ClientSecret,
		"redirect_uri":         p.RedirectURI,
		"scopes":               p.Scopes,
		"metadata_url":         p.MetadataURL,
		"entity_id":            p.EntityID,
		"acs_url":              p.ACSUrl,
		"host":                 p.Host,
		"port":                 p.Port,
		"bind_dn":              p.BindDN,
		"base_dn":              p.BaseDN,
		"user_filter":          p.UserFilter,
		"enabled":              p.Enabled,
		"default_org":          p.DefaultOrg,
		"allow_auto_provision": p.AllowAutoProvision,
		"password_login":       p.PasswordLogin,
	}
	if p.AttributeMapping != "" {
		row["attribute_mapping"] = p.AttributeMapping
	}
	if err := r.db.DB(ctx).Model(&SSOProvider{}).Create(row).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeSSOProviderExists)
		}
		return err
	}
	return nil
}

// FindProvider returns the provider with the given id.
// gorm.ErrRecordNotFound passes through; the caller maps it to
// CodeSSOProviderNotFound.
func (r *SSORepository) FindProvider(ctx context.Context, id string) (*SSOProvider, error) {
	var row SSOProvider
	err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListProviders returns providers newest first, paginated, with the
// total count before pagination.
func (r *SSORepository) ListProviders(ctx context.Context, filter ProviderFilter, offset, limit int) ([]*SSOProvider, int64, error) {
	query := r.db.DB(ctx).Model(&SSOProvider{})
	if filter.Type != "" {
		query = query.Where("type = ?", filter.Type)
	}
	if filter.Enabled != nil {
		query = query.Where("enabled = ?", *filter.Enabled)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*SSOProvider
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// UpdateProvider updates the mutable fields and re-selects the row.
func (r *SSORepository) UpdateProvider(ctx context.Context, id string, p *SSOProvider) (*SSOProvider, error) {
	updates := map[string]any{
		"display_name":         p.DisplayName,
		"issuer":               p.Issuer,
		"client_id":            p.ClientID,
		"client_secret":        p.ClientSecret,
		"redirect_uri":         p.RedirectURI,
		"scopes":               p.Scopes,
		"metadata_url":         p.MetadataURL,
		"entity_id":            p.EntityID,
		"acs_url":              p.ACSUrl,
		"host":                 p.Host,
		"port":                 p.Port,
		"bind_dn":              p.BindDN,
		"base_dn":              p.BaseDN,
		"user_filter":          p.UserFilter,
		"default_org":          p.DefaultOrg,
		"allow_auto_provision": p.AllowAutoProvision,
		"attribute_mapping":    p.AttributeMapping,
		"password_login":       p.PasswordLogin,
	}
	res := r.db.DB(ctx).Model(&SSOProvider{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return r.FindProvider(ctx, id)
}

// SetProviderEnabled flips the enabled flag (idempotent) and re-selects
// the row.
func (r *SSORepository) SetProviderEnabled(ctx context.Context, id string, enabled bool) (*SSOProvider, error) {
	res := r.db.DB(ctx).Model(&SSOProvider{}).Where("id = ?", id).Updates(map[string]any{
		"enabled": enabled,
	})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return r.FindProvider(ctx, id)
}

// DeleteProvider removes a provider. The caller gates on bindings.
func (r *SSORepository) DeleteProvider(ctx context.Context, id string) error {
	return r.db.DB(ctx).Where("id = ?", id).Delete(&SSOProvider{}).Error
}

// CountBindingsByProvider counts the provider's identity bindings.
func (r *SSORepository) CountBindingsByProvider(ctx context.Context, providerID string) (int64, error) {
	var count int64
	err := r.db.DB(ctx).Model(&IdentityBinding{}).
		Where("provider_id = ?", providerID).
		Count(&count).Error
	return count, err
}

// CreateBinding inserts a new identity binding. A unique violation on
// (provider_id, external_subject) maps to CodeIdentityBindingExists.
func (r *SSORepository) CreateBinding(ctx context.Context, b *IdentityBinding) error {
	if err := r.db.DB(ctx).Create(b).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeIdentityBindingExists)
		}
		return err
	}
	return nil
}

// FindBindingBySubject returns the binding for (provider_id,
// external_subject). gorm.ErrRecordNotFound passes through.
func (r *SSORepository) FindBindingBySubject(ctx context.Context, providerID, externalSubject string) (*IdentityBinding, error) {
	var row IdentityBinding
	err := r.db.DB(ctx).
		Where("provider_id = ? AND external_subject = ?", providerID, externalSubject).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListBindings returns bindings newest first, paginated, with the total
// count before pagination.
func (r *SSORepository) ListBindings(ctx context.Context, filter BindingFilter, offset, limit int) ([]*IdentityBinding, int64, error) {
	query := r.db.DB(ctx).Model(&IdentityBinding{})
	if filter.ProviderID != "" {
		query = query.Where("provider_id = ?", filter.ProviderID)
	}
	if filter.UserID != "" {
		query = query.Where("user_id = ?", filter.UserID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*IdentityBinding
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// DeleteBinding removes a binding (idempotent). The user is kept.
func (r *SSORepository) DeleteBinding(ctx context.Context, bindingID string) error {
	return r.db.DB(ctx).Where("id = ?", bindingID).Delete(&IdentityBinding{}).Error
}

// CreateUser inserts a new user. A unique violation on username maps to
// CodeUserExists.
func (r *SSORepository) CreateUser(ctx context.Context, u *User) error {
	if err := r.db.DB(ctx).Create(u).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeUserExists)
		}
		return err
	}
	return nil
}

// FindUser returns the user with the given id. gorm.ErrRecordNotFound
// passes through; the caller maps it to CodeOrganizationNotFound
// (reused: unknown user).
func (r *SSORepository) FindUser(ctx context.Context, id string) (*User, error) {
	var row User
	err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindUserByUsername returns the user with the given username.
// gorm.ErrRecordNotFound passes through; the caller maps it to
// CodeOrganizationNotFound (reused: unknown user). Used by the compose
// seeding (feature-22 AD10).
func (r *SSORepository) FindUserByUsername(ctx context.Context, username string) (*User, error) {
	var row User
	err := r.db.DB(ctx).Where("username = ?", username).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindUserByEmail returns the user with the given email.
// gorm.ErrRecordNotFound passes through; the caller maps it to
// CodeOrganizationNotFound (reused: unknown user). Used to link an SSO
// identity to an existing local account by email.
func (r *SSORepository) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	var row User
	err := r.db.DB(ctx).Where("email = ?", email).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListUsers returns the user directory, optionally filtered by state,
// ordered by creation time descending.
func (r *SSORepository) ListUsers(ctx context.Context, filter UserFilter, offset, limit int) ([]*User, int64, error) {
	query := r.db.DB(ctx).Model(&User{})
	if filter.State != "" {
		query = query.Where("state = ?", filter.State)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*User
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListUsersByIDs returns the users with the given ids. Unknown ids are
// simply absent from the result; an empty input returns an empty slice
// without querying.
func (r *SSORepository) ListUsersByIDs(ctx context.Context, ids []string) ([]*User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []*User
	if err := r.db.DB(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// SetUserState flips a user's lifecycle state and re-selects the row.
// gorm.ErrRecordNotFound passes through; the caller maps it to
// CodeUserNotFound.
func (r *SSORepository) SetUserState(ctx context.Context, id string, state string) (*User, error) {
	var row User
	if err := r.db.DB(ctx).Model(&User{}).Where("id = ?", id).
		Update("state", state).Error; err != nil {
		return nil, err
	}
	if err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// DeleteUser removes a user and its identity bindings in one
// transaction. gorm.ErrRecordNotFound passes through when the user does
// not exist.
func (r *SSORepository) DeleteUser(ctx context.Context, id string) error {
	return r.db.DB(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("user_id = ?", id).Delete(&IdentityBinding{})
		if res.Error != nil {
			return res.Error
		}
		out := tx.Where("id = ?", id).Delete(&User{})
		if out.Error != nil {
			return out.Error
		}
		if out.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// SetUserPassword stores a new password hash for a user.
func (r *SSORepository) SetUserPassword(ctx context.Context, id, hash string) error {
	return r.db.DB(ctx).Model(&User{}).Where("id = ?", id).
		Update("password_hash", hash).Error
}

// UpdateUserProfile updates the mutable profile fields (display name,
// avatar) and re-selects the row. Username and email are immutable.
// gorm.ErrRecordNotFound passes through; the caller maps it to
// CodeUserNotFound.
func (r *SSORepository) UpdateUserProfile(ctx context.Context, id, displayName, avatar string) (*User, error) {
	res := r.db.DB(ctx).Model(&User{}).Where("id = ?", id).
		Updates(map[string]any{"display_name": displayName, "avatar": avatar}).Error
	if res != nil {
		return nil, res
	}
	var row User
	if err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// isUniqueViolation reports whether err is a unique-constraint
// violation across the supported dialects (PostgreSQL 23505, SQLite
// generic message).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}
