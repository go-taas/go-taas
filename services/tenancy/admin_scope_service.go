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
// may apply to join (requirement 5). A person may belong to several
// organizations, so the pick-list offers every tenant organization the
// caller is not already a member of.
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
	joined := make(map[string]bool, len(memberOrgs))
	for _, m := range memberOrgs {
		joined[m.OrganizationID] = true
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
		if org.ID == platformOrgID || joined[org.ID] || pending[org.ID] {
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
// organizations they may apply to join (requirement 5). A person may
// belong to several organizations and several projects, so the pick-list
// offers every project the caller is not already a member of, across all
// organizations they belong to.
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
	memberOrgs, err := mrepo.ListMemberOrgs(ctx, userID)
	if err != nil {
		return nil, err
	}
	requested := strings.TrimSpace(req.GetOrganizationId())
	orgIDs := make([]string, 0, len(memberOrgs))
	for _, m := range memberOrgs {
		if m.OrganizationID == platformOrgID {
			continue
		}
		if requested != "" && m.OrganizationID != requested {
			continue
		}
		orgIDs = append(orgIDs, m.OrganizationID)
	}
	if len(orgIDs) == 0 {
		// Not in an organization yet: no projects to join.
		return &tenancyv1.ListJoinableProjectsResponse{Response: okResponse()}, nil
	}
	memberProjects, err := mrepo.ListMemberProjects(ctx, userID)
	if err != nil {
		return nil, err
	}
	joined := make(map[string]bool, len(memberProjects))
	for _, m := range memberProjects {
		joined[m.ProjectID] = true
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
	out := make([]*tenancyv1.JoinableProject, 0, len(orgIDs))
	for _, orgID := range orgIDs {
		rows, _, err := repo.ListProjects(ctx, ProjectFilter{OrganizationID: orgID, State: StateActive}, 0, listMaxLimit)
		if err != nil {
			return nil, err
		}
		for _, p := range rows {
			if joined[p.ID] || pending[p.ID] {
				continue
			}
			out = append(out, &tenancyv1.JoinableProject{
				ProjectId:      p.ID,
				OrganizationId: p.OrganizationID,
				DisplayName:    p.DisplayName,
			})
		}
	}
	return &tenancyv1.ListJoinableProjectsResponse{Response: okResponse(), Projects: out}, nil
}

// requireOrgAdminOrPlatform gates organization metadata writes: a
// platform administrator may always edit, otherwise the caller must be an
// admin or owner of the organization (requirement 2/3). A nil role guard
// (unit tests) is permissive, matching the package convention.
func (s *Service) requireOrgAdminOrPlatform(ctx context.Context, orgID string) error {
	if s.callerIsPlatformAdmin(ctx) {
		return nil
	}
	if s.roleGuard == nil {
		return nil
	}
	return s.requireAdminOrOwner(ctx, orgID)
}

// requireOrgAdminWrite gates a project write on the caller being an admin
// or owner of the project's owning organization. A platform administrator
// has no project management (requirement 2), so it resolves the membership
// row alone and never the platform-administrator session role. A nil role
// guard (unit tests) is permissive.
func (s *Service) requireOrgAdminWrite(ctx context.Context, orgID string) error {
	if s.roleGuard == nil {
		return nil
	}
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return err
	}
	return s.roleGuard.RequireMemberRole(ctx, orgID, userID, RoleAdmin)
}
