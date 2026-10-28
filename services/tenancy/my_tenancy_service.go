package tenancy

import (
	"context"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
)

// GetMyOrganizations returns the caller's own organizations and roles
// (user surface). The caller is resolved from the session, never from a
// client-supplied id.
func (s *Service) GetMyOrganizations(ctx context.Context, _ *tenancyv1.GetMyOrganizationsRequest) (*tenancyv1.GetMyOrganizationsResponse, error) {
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListMemberOrgs(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgsRepo, err := s.repository()
	if err != nil {
		return nil, err
	}
	orgs := make([]*tenancyv1.MyOrganization, 0, len(rows))
	for _, row := range rows {
		if row.OrganizationID == platformOrgID {
			// The platform anchor is not a tenant a person belongs to.
			continue
		}
		// The directory shows a name and an id, so the caller's own list
		// carries the display name too; a vanished organization falls back
		// to its id rather than disappearing from the caller's view.
		name := row.OrganizationID
		if org, err := orgsRepo.FindOrganization(ctx, row.OrganizationID); err == nil {
			name = org.DisplayName
		} else if !isRecordNotFound(err) {
			return nil, err
		}
		orgs = append(orgs, &tenancyv1.MyOrganization{
			OrganizationId: row.OrganizationID,
			DisplayName:    name,
			Role:           row.Role,
		})
	}
	return &tenancyv1.GetMyOrganizationsResponse{
		Response:      okResponse(),
		Organizations: orgs,
	}, nil
}

// GetMyProjects returns the caller's own projects and roles (user
// surface). The caller is resolved from the session.
func (s *Service) GetMyProjects(ctx context.Context, _ *tenancyv1.GetMyProjectsRequest) (*tenancyv1.GetMyProjectsResponse, error) {
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListMemberProjects(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgRepo, err := s.repository()
	if err != nil {
		return nil, err
	}
	// Organization names are resolved once per owning organization.
	orgNames := make(map[string]string, len(rows))
	projects := make([]*tenancyv1.MyProject, 0, len(rows))
	for _, row := range rows {
		project := &tenancyv1.MyProject{
			ProjectId:   row.ProjectID,
			DisplayName: row.ProjectID,
			Role:        row.Role,
		}
		if p, err := orgRepo.FindProject(ctx, row.ProjectID); err == nil {
			project.DisplayName = p.DisplayName
			project.OrganizationId = p.OrganizationID
		} else if !isRecordNotFound(err) {
			return nil, err
		}
		if orgID := project.OrganizationId; orgID != "" {
			name, seen := orgNames[orgID]
			if !seen {
				name = orgID
				if org, err := orgRepo.FindOrganization(ctx, orgID); err == nil {
					name = org.DisplayName
				} else if !isRecordNotFound(err) {
					return nil, err
				}
				orgNames[orgID] = name
			}
			project.OrganizationDisplayName = name
		}
		projects = append(projects, project)
	}
	return &tenancyv1.GetMyProjectsResponse{
		Response: okResponse(),
		Projects: projects,
	}, nil
}