package infer

import (
	"context"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// ProjectMemberLister reports a user's project memberships, so a project
// grant in an access policy can be matched. It is implemented by tenancy
// and injected at wiring time; nil means project grants cannot be resolved
// and are treated as not matching.
type ProjectMemberLister interface {
	ProjectsForUser(ctx context.Context, userID string) ([]string, error)
}

// SetProjectMemberLister injects the project-membership lookup.
func (s *Service) SetProjectMemberLister(l ProjectMemberLister) { s.projectMembers = l }

// authorizeServiceUse decides whether the caller may use a service.
//
// A service with no access policy is open to every platform user, which is
// the default. A restricted service admits a caller who belongs to a listed
// organization, one of whose projects is listed, or who is listed by id.
// Each denial names the reason so an operator can tell "you are not on the
// list" from "the list is empty".
func (s *Service) authorizeServiceUse(ctx context.Context, repo *InferenceServiceRepository, serviceID, orgID, userID string) error {
	rows, err := repo.ListAccess(ctx, serviceID)
	if err != nil {
		// The policy is in the database the request already depends on; a
		// read failure means we cannot prove the caller is allowed, so fail
		// closed rather than widening access.
		return apierrors.Newf(apierrors.CodeInferAccessInvalid,
			"infer: could not read the access policy of service %s: %v", serviceID, err)
	}
	if len(rows) == 0 {
		return nil
	}
	var projects []string
	if userID != "" && s.projectMembers != nil {
		projects, _ = s.projectMembers.ProjectsForUser(ctx, userID)
	}
	projectSet := make(map[string]struct{}, len(projects))
	for _, id := range projects {
		projectSet[id] = struct{}{}
	}
	for _, row := range rows {
		switch row.SubjectType {
		case AccessSubjectOrg:
			if orgID != "" && row.SubjectID == orgID {
				return nil
			}
		case AccessSubjectProject:
			if _, ok := projectSet[row.SubjectID]; ok {
				return nil
			}
		case AccessSubjectUser:
			if userID != "" && row.SubjectID == userID {
				return nil
			}
		}
	}
	return apierrors.Newf(apierrors.CodeForbidden,
		"infer: service %s is restricted; ask a platform administrator to grant your organization, project or account", serviceID)
}
