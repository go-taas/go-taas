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
	orgs := make([]*tenancyv1.MyOrganization, 0, len(rows))
	for _, row := range rows {
		orgs = append(orgs, &tenancyv1.MyOrganization{
			OrganizationId: row.OrganizationID,
			DisplayName:    row.OrganizationID,
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
	projects := make([]*tenancyv1.MyProject, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, &tenancyv1.MyProject{
			ProjectId:      row.ProjectID,
			OrganizationId: "",
			DisplayName:    row.ProjectID,
			Role:           row.Role,
		})
	}
	return &tenancyv1.GetMyProjectsResponse{
		Response: okResponse(),
		Projects: projects,
	}, nil
}