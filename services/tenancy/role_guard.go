package tenancy

import (
	"context"

	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// roleRank orders the fixed role set for the "at least" comparison
// (feature #10, AD6). viewer < member < admin < owner.
var roleRank = map[string]int{
	RoleViewer: 0,
	RoleMember: 1,
	RoleAdmin:  2,
	RoleOwner:  3,
	// Platform admins rank with owners for org-scoped operations.
	RolePlatformAdmin: 3,
}

// RoleGuard resolves the caller's role in the org context from
// org_members and enforces a required minimum role (feature #10, AD6).
// It composes with, not replaces, the OrgGuard: OrgGuard answers "does
// the org exist", RoleGuard answers "may this caller act in it". It is
// a read-only interface over the tenancy tables, injected into
// consuming services at wiring time (the SetDeleteModelGuard pattern).
type RoleGuard struct {
	repo *MembershipRepository
}

// NewRoleGuard constructs a RoleGuard bound to a GORM database.
func NewRoleGuard(db *gorm.DB) *RoleGuard {
	return &RoleGuard{repo: NewMembershipRepository(db)}
}

// ResolveRole returns the caller's role in the org, or "" when the
// caller is not a member.
func (g *RoleGuard) ResolveRole(ctx context.Context, orgID, userID string) (string, error) {
	m, err := g.repo.FindMember(ctx, orgID, userID)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", nil
	}
	return m.Role, nil
}

// RequireRole returns CodeForbidden when the caller's role is below the
// required minimum role, or when the caller is not a member.
func (g *RoleGuard) RequireRole(ctx context.Context, orgID, userID, minRole string) error {
	role, err := g.ResolveRole(ctx, orgID, userID)
	if err != nil {
		return err
	}
	if roleRank[role] < roleRank[minRole] {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

// RequireAdminOrOwner returns CodeForbidden unless the caller is an
// admin or owner in the org (AD7).
func (g *RoleGuard) RequireAdminOrOwner(ctx context.Context, orgID, userID string) error {
	return g.RequireRole(ctx, orgID, userID, RoleAdmin)
}

// IsOwner reports whether the caller is the org owner.
func (g *RoleGuard) IsOwner(ctx context.Context, orgID, userID string) (bool, error) {
	role, err := g.ResolveRole(ctx, orgID, userID)
	if err != nil {
		return false, err
	}
	return role == RoleOwner, nil
}
