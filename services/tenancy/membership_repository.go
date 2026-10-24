package tenancy

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// MembershipRepository persists org members and invitations (feature
// #10). All reads and writes join an open transaction via the context.
type MembershipRepository struct {
	db *database.Manager
}

// NewMembershipRepository constructs a MembershipRepository bound to a
// database Manager.
func NewMembershipRepository(db *gorm.DB) *MembershipRepository {
	return &MembershipRepository{db: database.NewManager(db)}
}

// FindMember returns the member row for (org, user); nil when absent.
func (r *MembershipRepository) FindMember(ctx context.Context, orgID, userID string) (*OrgMember, error) {
	var row OrgMember
	err := r.db.DB(ctx).Where("organization_id = ? AND user_id = ?", orgID, userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListMembers returns one page of the org's members, newest first, and
// the total count.
func (r *MembershipRepository) ListMembers(ctx context.Context, orgID string, offset, limit int) ([]*OrgMember, int64, error) {
	query := r.db.DB(ctx).Model(&OrgMember{}).Where("organization_id = ?", orgID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*OrgMember
	if err := query.Order("joined_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// AddMember inserts a member row. A duplicate (org, user) maps to
// CodeMemberExists.
func (r *MembershipRepository) AddMember(ctx context.Context, m *OrgMember) error {
	if err := r.db.DB(ctx).Create(m).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeMemberExists)
		}
		return err
	}
	return nil
}

// SetMemberRole updates a member's role.
func (r *MembershipRepository) SetMemberRole(ctx context.Context, orgID, userID, role string) error {
	return r.db.DB(ctx).Model(&OrgMember{}).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		Update("role", role).Error
}

// RemoveMember deletes a member row (idempotent).
func (r *MembershipRepository) RemoveMember(ctx context.Context, orgID, userID string) error {
	return r.db.DB(ctx).Where("organization_id = ? AND user_id = ?", orgID, userID).
		Delete(&OrgMember{}).Error
}

// CountOwners returns the number of owner rows for the org — the
// owner-uniqueness backstop (AD3).
func (r *MembershipRepository) CountOwners(ctx context.Context, orgID string) (int64, error) {
	var count int64
	err := r.db.DB(ctx).Model(&OrgMember{}).
		Where("organization_id = ? AND role = ?", orgID, RoleOwner).
		Count(&count).Error
	return count, err
}

// ListMemberOrgs returns the (org, role) pairs for a user — the
// session-derivation lookup (AD2/AD11).
func (r *MembershipRepository) ListMemberOrgs(ctx context.Context, userID string) ([]*OrgMember, error) {
	var rows []*OrgMember
	err := r.db.DB(ctx).Where("user_id = ?", userID).Find(&rows).Error
	return rows, err
}

// FindProjectMember returns the member row for (project, user); nil
// when absent.
func (r *MembershipRepository) FindProjectMember(ctx context.Context, projectID, userID string) (*ProjectMember, error) {
	var row ProjectMember
	err := r.db.DB(ctx).Where("project_id = ? AND user_id = ?", projectID, userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListProjectMembers returns one page of the project's members, newest
// first, and the total count.
func (r *MembershipRepository) ListProjectMembers(ctx context.Context, projectID string, offset, limit int) ([]*ProjectMember, int64, error) {
	query := r.db.DB(ctx).Model(&ProjectMember{}).Where("project_id = ?", projectID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*ProjectMember
	if err := query.Order("joined_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// AddProjectMember inserts a project member row. A duplicate
// (project, user) maps to CodeMemberExists.
func (r *MembershipRepository) AddProjectMember(ctx context.Context, m *ProjectMember) error {
	if err := r.db.DB(ctx).Create(m).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeMemberExists)
		}
		return err
	}
	return nil
}

// SetProjectMemberRole updates a project member's role.
func (r *MembershipRepository) SetProjectMemberRole(ctx context.Context, projectID, userID, role string) error {
	return r.db.DB(ctx).Model(&ProjectMember{}).
		Where("project_id = ? AND user_id = ?", projectID, userID).
		Update("role", role).Error
}

// RemoveProjectMember deletes a project member row (idempotent).
func (r *MembershipRepository) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	return r.db.DB(ctx).Where("project_id = ? AND user_id = ?", projectID, userID).
		Delete(&ProjectMember{}).Error
}

// CreateInvitation inserts an invitation. A duplicate pending
// (org, email) maps to CodeInvitationExists.
func (r *MembershipRepository) CreateInvitation(ctx context.Context, inv *Invitation) error {
	pending, err := r.PendingInvitationExists(ctx, inv.OrganizationID, inv.Email)
	if err != nil {
		return err
	}
	if pending {
		return apierrors.New(apierrors.CodeInvitationExists)
	}
	if err := r.db.DB(ctx).Create(inv).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeInvitationExists)
		}
		return err
	}
	return nil
}

// FindInvitationByTokenHash returns the invitation with the given token
// hash; nil when absent.
func (r *MembershipRepository) FindInvitationByTokenHash(ctx context.Context, tokenHash string) (*Invitation, error) {
	var row Invitation
	err := r.db.DB(ctx).Where("token_hash = ?", tokenHash).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindInvitationByID returns the invitation with the given id; nil when
// absent.
func (r *MembershipRepository) FindInvitationByID(ctx context.Context, id string) (*Invitation, error) {
	var row Invitation
	err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListInvitations returns one page of the org's invitations, newest
// first, optionally filtered by status, and the total count.
func (r *MembershipRepository) ListInvitations(ctx context.Context, orgID, status string, offset, limit int) ([]*Invitation, int64, error) {
	query := r.db.DB(ctx).Model(&Invitation{}).Where("organization_id = ?", orgID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Invitation
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// SetInvitationStatus updates an invitation's status.
func (r *MembershipRepository) SetInvitationStatus(ctx context.Context, id, status string) error {
	return r.db.DB(ctx).Model(&Invitation{}).Where("id = ?", id).Update("status", status).Error
}

// UpdateInvitationToken updates an invitation's token hash and expiry
// (resend).
func (r *MembershipRepository) UpdateInvitationToken(ctx context.Context, id, tokenHash string, expiresAt int64) error {
	return r.db.DB(ctx).Model(&Invitation{}).
		Where("id = ?", id).
		Updates(map[string]any{"token_hash": tokenHash, "expires_at": expiresAt}).Error
}

// PendingInvitationExists reports whether a pending invitation exists
// for (org, email).
func (r *MembershipRepository) PendingInvitationExists(ctx context.Context, orgID, email string) (bool, error) {
	var count int64
	err := r.db.DB(ctx).Model(&Invitation{}).
		Where("organization_id = ? AND email = ? AND status = ?", orgID, email, InvitationPending).
		Count(&count).Error
	return count > 0, err
}
