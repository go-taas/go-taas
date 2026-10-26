package tenancy

import (
	"context"
	"strings"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
)

// Admin scope types (feature #54). The console offers one entry per
// scope so a multi-role user chooses which console to enter.
const (
	// ScopePlatform is the platform-administrator console.
	ScopePlatform = "platform"
	// ScopeOrganization is an organization-administrator console.
	ScopeOrganization = "organization"
	// ScopeProject is a project-administrator console.
	ScopeProject = "project"

	// platformOrgID is the dedicated organization that carries the
	// platform-admin role (seed). It is a system artifact, not a tenant
	// a user joins, so it is excluded from joinable lists and from the
	// one-organization rule.
	platformOrgID = "platform"
)

// GetMyAdminScopes returns the admin scopes the caller may enter: the
// platform console when the session carries platform-admin, one entry
// per organization the caller administers, and one per project the
// caller administers (feature #54, requirement 6).
func (s *Service) GetMyAdminScopes(ctx context.Context, _ *tenancyv1.GetMyAdminScopesRequest) (*tenancyv1.GetMyAdminScopesResponse, error) {
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	mrepo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	scopes := make([]*tenancyv1.AdminScope, 0, 4)

	// Platform: the session role, not a membership row.
	if ok, err := s.IsPlatformAdmin(ctx); err != nil {
		return nil, err
	} else if ok {
		scopes = append(scopes, &tenancyv1.AdminScope{
			ScopeType: ScopePlatform,
			Role:      RolePlatformAdmin,
		})
	}

	// Organizations the caller administers (owner or admin). The
	// platform org is a system artifact, not a tenant console.
	orgs, err := mrepo.ListMemberOrgs(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, m := range orgs {
		if m.OrganizationID == platformOrgID {
			continue
		}
		if m.Role != RoleOwner && m.Role != RoleAdmin {
			continue
		}
		name := m.OrganizationID
		if org, err := repo.FindOrganization(ctx, m.OrganizationID); err == nil {
			name = org.DisplayName
		} else if !isRecordNotFound(err) {
			return nil, err
		}
		scopes = append(scopes, &tenancyv1.AdminScope{
			ScopeType:      ScopeOrganization,
			OrganizationId: m.OrganizationID,
			DisplayName:    name,
			Role:           m.Role,
		})
	}

	// Projects the caller administers.
	projects, err := mrepo.ListMemberProjects(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, m := range projects {
		if m.Role != RoleAdmin {
			continue
		}
		name := m.ProjectID
		orgID := ""
		if p, err := repo.FindProject(ctx, m.ProjectID); err == nil {
			name = p.DisplayName
			orgID = p.OrganizationID
		} else if !isRecordNotFound(err) {
			return nil, err
		}
		scopes = append(scopes, &tenancyv1.AdminScope{
			ScopeType:      ScopeProject,
			OrganizationId: orgID,
			ProjectId:      m.ProjectID,
			DisplayName:    name,
			Role:           m.Role,
		})
	}

	return &tenancyv1.GetMyAdminScopesResponse{Response: okResponse(), Scopes: scopes}, nil
}

// ListJoinableOrganizations returns the active organizations the caller
// may apply to join (requirement 5). The list is empty once the caller
// belongs to an organization, so the one-organization rule is offered as
// a pick-list rather than free text.
func (s *Service) ListJoinableOrganizations(ctx context.Context, _ *tenancyv1.ListJoinableOrganizationsRequest) (*tenancyv1.ListJoinableOrganizationsResponse, error) {
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	mrepo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	memberOrgs, err := mrepo.ListMemberOrgs(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, m := range memberOrgs {
		if m.OrganizationID != platformOrgID {
			// Already in an organization: nothing to join.
			return &tenancyv1.ListJoinableOrganizationsResponse{Response: okResponse()}, nil
		}
	}
	// Exclude targets with an open request so the pick-list does not
	// offer a duplicate.
	pending := make(map[string]bool)
	requests, err := mrepo.ListJoinRequestsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, r := range requests {
		if r.TargetType == JoinTargetOrganization && r.Status == JoinStatusPending {
			pending[r.TargetID] = true
		}
	}
	rows, _, err := repo.ListOrganizations(ctx, OrgFilter{State: StateActive}, 0, listMaxLimit)
	if err != nil {
		return nil, err
	}
	out := make([]*tenancyv1.JoinableOrganization, 0, len(rows))
	for _, org := range rows {
		if org.ID == platformOrgID || pending[org.ID] {
			continue
		}
		out = append(out, &tenancyv1.JoinableOrganization{
			OrganizationId: org.ID,
			DisplayName:    org.DisplayName,
		})
	}
	return &tenancyv1.ListJoinableOrganizationsResponse{Response: okResponse(), Organizations: out}, nil
}

// ListJoinableProjects returns the active projects of the caller's
// organization they may apply to join (requirement 5). The list is empty
// once the caller belongs to a project.
func (s *Service) ListJoinableProjects(ctx context.Context, req *tenancyv1.ListJoinableProjectsRequest) (*tenancyv1.ListJoinableProjectsResponse, error) {
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	mrepo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	memberProjects, err := mrepo.ListMemberProjects(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(memberProjects) > 0 {
		return &tenancyv1.ListJoinableProjectsResponse{Response: okResponse()}, nil
	}
	orgID := strings.TrimSpace(req.GetOrganizationId())
	if orgID == "" {
		orgID, err = s.userOrganizationID(ctx, userID, mrepo)
		if err != nil {
			return nil, err
		}
	}
	if orgID == "" {
		// Not in an organization yet: no projects to join.
		return &tenancyv1.ListJoinableProjectsResponse{Response: okResponse()}, nil
	}
	pending := make(map[string]bool)
	requests, err := mrepo.ListJoinRequestsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, r := range requests {
		if r.TargetType == JoinTargetProject && r.Status == JoinStatusPending {
			pending[r.TargetID] = true
		}
	}
	rows, _, err := repo.ListProjects(ctx, ProjectFilter{OrganizationID: orgID, State: StateActive}, 0, listMaxLimit)
	if err != nil {
		return nil, err
	}
	out := make([]*tenancyv1.JoinableProject, 0, len(rows))
	for _, p := range rows {
		if pending[p.ID] {
			continue
		}
		out = append(out, &tenancyv1.JoinableProject{
			ProjectId:      p.ID,
			OrganizationId: p.OrganizationID,
			DisplayName:    p.DisplayName,
		})
	}
	return &tenancyv1.ListJoinableProjectsResponse{Response: okResponse(), Projects: out}, nil
}

// userOrganizationID returns the caller's real (non-platform)
// organization id, or "" when the caller belongs to none.
func (s *Service) userOrganizationID(ctx context.Context, userID string, mrepo *MembershipRepository) (string, error) {
	memberOrgs, err := mrepo.ListMemberOrgs(ctx, userID)
	if err != nil {
		return "", err
	}
	for _, m := range memberOrgs {
		if m.OrganizationID != platformOrgID {
			return m.OrganizationID, nil
		}
	}
	return "", nil
}

// realOrgMemberships filters out the dedicated platform organization,
// which is a system artifact rather than tenant membership. It backs the
// one-organization rule.
func realOrgMemberships(members []*OrgMember) []*OrgMember {
	out := make([]*OrgMember, 0, len(members))
	for _, m := range members {
		if m.OrganizationID == platformOrgID {
			continue
		}
		out = append(out, m)
	}
	return out
}
