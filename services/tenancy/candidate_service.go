// Roster candidates: the accounts an administrator may still add.
//
// A person belongs to at most one organization and one project, so the
// pick-list for a roster is exactly the set of accounts that has not joined
// one yet:
//
//	organization roster   active accounts with no organization
//	project roster        active accounts of that project's organization
//	                      with no project
//
// The membership tables live in this module; the account rows live in the
// auth module, so the directory is an injected seam (see
// CandidateUserDirectory) and the filtering happens here.

package tenancy

import (
	"context"
	"strings"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
)

// CandidateUser is one account a roster may still add.
type CandidateUser struct {
	UserID      string
	Username    string
	DisplayName string
	Email       string
}

// CandidateUserDirectory lists the accounts a roster may pick from. It is
// implemented by the auth module (which owns the users table) and injected
// at wiring time. Nil until wired: the candidate lists are empty rather than
// wrong.
type CandidateUserDirectory interface {
	// ListActiveUsers returns every active account, newest first.
	ListActiveUsers(ctx context.Context) ([]CandidateUser, error)
}

// SetCandidateUserDirectory wires the roster-candidate directory.
func (s *Service) SetCandidateUserDirectory(d CandidateUserDirectory) {
	s.candidateDirectory = d
}

// candidateUsers returns the directory's active accounts, or nil when the
// seam is not wired.
func (s *Service) candidateUsers(ctx context.Context) ([]CandidateUser, error) {
	if s.candidateDirectory == nil {
		return nil, nil
	}
	return s.candidateDirectory.ListActiveUsers(ctx)
}

// ListOrgMemberCandidates returns the accounts that may still join the
// organization (active accounts with no organization).
func (s *Service) ListOrgMemberCandidates(ctx context.Context, req *tenancyv1.ListOrgMemberCandidatesRequest) (*tenancyv1.ListOrgMemberCandidatesResponse, error) {
	orgID := req.GetOrganizationId()
	if err := s.requireRosterAdmin(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	taken, err := repo.AllOrgMemberUserIDs(ctx)
	if err != nil {
		return nil, err
	}
	users, err := s.candidateUsers(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]*tenancyv1.MemberCandidate, 0, len(users))
	for _, u := range users {
		if taken[u.UserID] {
			continue
		}
		candidates = append(candidates, summarizeCandidate(u))
	}
	return &tenancyv1.ListOrgMemberCandidatesResponse{
		Response:   okResponse(),
		Candidates: candidates,
	}, nil
}

// ListProjectMemberCandidates returns the organization's members that may
// still join the project (active accounts of the owning organization with no
// project).
func (s *Service) ListProjectMemberCandidates(ctx context.Context, req *tenancyv1.ListProjectMemberCandidatesRequest) (*tenancyv1.ListProjectMemberCandidatesResponse, error) {
	projectID := strings.TrimSpace(req.GetProjectId())
	if projectID == "" {
		return nil, apierrors.New(apierrors.CodeProjectNotFound)
	}
	project, err := s.projectForRoster(ctx, projectID)
	if err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	orgMembers, err := repo.OrgMemberUserIDs(ctx, project.OrganizationID)
	if err != nil {
		return nil, err
	}
	inProject, err := repo.AllProjectMemberUserIDs(ctx)
	if err != nil {
		return nil, err
	}
	users, err := s.candidateUsers(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]*tenancyv1.MemberCandidate, 0, len(users))
	for _, u := range users {
		// Only the owning organization's members can join its projects, and
		// a person belongs to at most one project.
		if !orgMembers[u.UserID] || inProject[u.UserID] {
			continue
		}
		candidates = append(candidates, summarizeCandidate(u))
	}
	return &tenancyv1.ListProjectMemberCandidatesResponse{
		Response:   okResponse(),
		Candidates: candidates,
	}, nil
}

// projectForRoster gates the caller (project admin, or admin/owner of the
// owning organization) and returns the project, so a candidate list never
// leaks the organization's roster to someone who cannot administer it.
func (s *Service) projectForRoster(ctx context.Context, projectID string) (*Project, error) {
	if err := s.requireProjectAdmin(ctx, projectID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	project, err := repo.FindProject(ctx, projectID)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeProjectNotFound)
	}
	if err != nil {
		return nil, err
	}
	return project, nil
}

// summarizeCandidate maps a directory account to the wire message.
func summarizeCandidate(u CandidateUser) *tenancyv1.MemberCandidate {
	return &tenancyv1.MemberCandidate{
		UserId:      u.UserID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Email:       u.Email,
	}
}
