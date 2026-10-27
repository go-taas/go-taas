package tenancy

import (
	"context"

	"gorm.io/gorm"
)

// MemberOrg is one (org, role) membership of a user — the session
// derivation input (feature #10, AD2/AD11).
type MemberOrg struct {
	OrganizationID string
	Role           string
}

// MembershipResolver is the read interface the auth module consumes to
// derive a session's accessible orgs and per-org roles from org_members
// (authoritative over IdP claims, AD2). It is injected into the auth
// service at wiring time (the SetOrgGuard pattern).
type MembershipResolver struct {
	repo *MembershipRepository
}

// NewMembershipResolver constructs a MembershipResolver bound to a GORM
// database.
func NewMembershipResolver(db *gorm.DB) *MembershipResolver {
	return &MembershipResolver{repo: NewMembershipRepository(db)}
}

// Affiliation is the organization and project a platform user belongs to,
// with their display names. Both are empty when the user has not joined
// one; an account belongs to at most one organization and one project.
type Affiliation struct {
	OrganizationID   string
	OrganizationName string
	ProjectID        string
	ProjectName      string
}

// UserAffiliation resolves the organization and project a user belongs to.
// The dedicated platform organization is not an affiliation: it is the
// tenancy anchor of the platform itself, not a tenant a person joins.
func (r *MembershipResolver) UserAffiliation(ctx context.Context, userID string) (Affiliation, error) {
	var out Affiliation
	db := r.repo.db.DB(ctx)
	var org struct {
		OrganizationID string
		DisplayName    string
	}
	if err := db.Table("org_members AS m").
		Select("m.organization_id AS organization_id, o.display_name AS display_name").
		Joins("JOIN organizations o ON o.id = m.organization_id").
		Where("m.user_id = ? AND m.organization_id <> ?", userID, platformOrgID).
		Order("m.created_at ASC").
		Limit(1).
		Scan(&org).Error; err != nil {
		return out, err
	}
	out.OrganizationID = org.OrganizationID
	out.OrganizationName = org.DisplayName
	var project struct {
		ProjectID   string
		DisplayName string
	}
	if err := db.Table("project_members AS pm").
		Select("pm.project_id AS project_id, p.display_name AS display_name").
		Joins("JOIN projects p ON p.id = pm.project_id").
		Where("pm.user_id = ?", userID).
		Order("pm.created_at ASC").
		Limit(1).
		Scan(&project).Error; err != nil {
		return out, err
	}
	out.ProjectID = project.ProjectID
	out.ProjectName = project.DisplayName
	return out, nil
}

// AccessibleOrgsAndRoles returns the user's (org, role) memberships.
func (r *MembershipResolver) AccessibleOrgsAndRoles(ctx context.Context, userID string) ([]MemberOrg, error) {
	rows, err := r.repo.ListMemberOrgs(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]MemberOrg, 0, len(rows))
	for _, row := range rows {
		out = append(out, MemberOrg{OrganizationID: row.OrganizationID, Role: row.Role})
	}
	return out, nil
}
