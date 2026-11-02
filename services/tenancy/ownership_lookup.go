package tenancy

import (
	"context"
)

// Project and membership lookups consumed by the auth module when it
// validates and displays API key ownership. They are read-only and
// deliberately narrow: auth never queries the tenancy tables directly.

// ProjectInOrg returns the project's display name and whether it
// belongs to the organization. A missing project reports (\"\", false,
// nil) — the caller maps it to its own validation error.
func (s *Service) ProjectInOrg(ctx context.Context, projectID, orgID string) (string, bool, error) {
	if projectID == "" || orgID == "" {
		return "", false, nil
	}
	repo, err := s.repository()
	if err != nil {
		return "", false, err
	}
	project, err := repo.FindProject(ctx, projectID)
	if isRecordNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if project.OrganizationID != orgID {
		return "", false, nil
	}
	return project.DisplayName, true, nil
}

// ProjectName returns the project's display name, or "" when unknown.
func (s *Service) ProjectName(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", nil
	}
	repo, err := s.repository()
	if err != nil {
		return "", err
	}
	project, err := repo.FindProject(ctx, projectID)
	if isRecordNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return project.DisplayName, nil
}

// UserInProject reports whether the user belongs to the project.
func (s *Service) UserInProject(ctx context.Context, projectID, userID string) (bool, error) {
	if projectID == "" || userID == "" {
		return false, nil
	}
	mrepo, err := s.membershipRepo()
	if err != nil {
		return false, err
	}
	member, err := mrepo.FindProjectMember(ctx, projectID, userID)
	if err != nil {
		return false, err
	}
	return member != nil, nil
}

// UserInOrg reports whether the user belongs to the organization.
func (s *Service) UserInOrg(ctx context.Context, orgID, userID string) (bool, error) {
	if orgID == "" || userID == "" {
		return false, nil
	}
	mrepo, err := s.membershipRepo()
	if err != nil {
		return false, err
	}
	member, err := mrepo.FindMember(ctx, orgID, userID)
	if err != nil {
		return false, err
	}
	return member != nil, nil
}
