package infer

import "context"

// UserLabels resolves user ids to display labels (auth's ResolveUserLabels).
type UserLabels interface {
	ResolveUserLabels(ctx context.Context, userIDs []string) (map[string]string, error)
}

// OrgProjectLabels resolves organization and project ids to display
// labels (tenancy's SubjectLabeller).
type OrgProjectLabels interface {
	LabelOrganizations(ctx context.Context, ids []string) (map[string]string, error)
	LabelProjects(ctx context.Context, ids []string) (map[string]string, error)
}

// subjectLabeller dispatches the SubjectLabeller seam per subject type, so
// the infer module never imports auth's or tenancy's models.
type subjectLabeller struct {
	users UserLabels
	orgs  OrgProjectLabels
}

// NewSubjectLabeller composes the per-type resolvers into the seam the
// access-policy RPCs use.
func NewSubjectLabeller(users UserLabels, orgs OrgProjectLabels) SubjectLabeller {
	return &subjectLabeller{users: users, orgs: orgs}
}

// LabelSubjects implements SubjectLabeller.
func (l *subjectLabeller) LabelSubjects(ctx context.Context, subjectType string, ids []string) (map[string]string, error) {
	switch subjectType {
	case AccessSubjectUser:
		if l.users == nil {
			return map[string]string{}, nil
		}
		return l.users.ResolveUserLabels(ctx, ids)
	case AccessSubjectOrg:
		if l.orgs == nil {
			return map[string]string{}, nil
		}
		return l.orgs.LabelOrganizations(ctx, ids)
	case AccessSubjectProject:
		if l.orgs == nil {
			return map[string]string{}, nil
		}
		return l.orgs.LabelProjects(ctx, ids)
	default:
		return map[string]string{}, nil
	}
}
