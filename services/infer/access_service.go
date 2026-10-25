package infer

import (
	"context"
	"strings"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// SubjectLabeller resolves access-policy subject ids to display labels so
// the console shows names instead of UUIDs. It is implemented by auth
// (users) and tenancy (organizations, projects) and injected at wiring
// time. Nil until wired: the raw id is reported.
type SubjectLabeller interface {
	// LabelSubjects returns a label per (subject type, id). Missing
	// entries mean "no label known".
	LabelSubjects(ctx context.Context, subjectType string, ids []string) (map[string]string, error)
}

// SetSubjectLabeller injects the access-policy label resolver.
func (s *Service) SetSubjectLabeller(l SubjectLabeller) { s.subjectLabeller = l }

// GetInferenceServiceAccess returns who may use a service. An empty
// subject list means every platform user may use it, which is the default
// and needs no rows.
func (s *Service) GetInferenceServiceAccess(ctx context.Context, req *inferv1.GetInferenceServiceAccessRequest) (*inferv1.GetInferenceServiceAccessResponse, error) {
	if err := s.requirePlatformOperator(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := s.findService(ctx, repo, req.GetServiceId()); err != nil {
		return nil, err
	}
	rows, err := repo.ListAccess(ctx, req.GetServiceId())
	if err != nil {
		return nil, err
	}
	subjects := s.labelAccessSubjects(ctx, rows)
	return &inferv1.GetInferenceServiceAccessResponse{
		Response:   okResponse(),
		Restricted: len(rows) > 0,
		Subjects:   subjects,
	}, nil
}

// SetInferenceServiceAccess replaces a service's access policy.
//
// `restricted=false` clears the policy, which means "every platform user".
// `restricted=true` with an empty list is rejected rather than stored: an
// empty restricted policy would mean nobody can use the service, which is
// never what an operator means and is indistinguishable from the default
// in the table.
func (s *Service) SetInferenceServiceAccess(ctx context.Context, req *inferv1.SetInferenceServiceAccessRequest) (*inferv1.SetInferenceServiceAccessResponse, error) {
	if err := s.requirePlatformOperator(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := s.findService(ctx, repo, req.GetServiceId()); err != nil {
		return nil, err
	}

	subjects := make([]*InferenceServiceAccess, 0, len(req.GetSubjects()))
	if req.GetRestricted() {
		if len(req.GetSubjects()) == 0 {
			return nil, apierrors.Newf(apierrors.CodeInferAccessInvalid,
				"infer: a restricted access policy needs at least one organization, project or user; send restricted=false to allow everyone")
		}
		seen := make(map[string]struct{}, len(req.GetSubjects()))
		for _, subject := range req.GetSubjects() {
			kind := strings.ToLower(strings.TrimSpace(subject.GetSubjectType()))
			id := strings.TrimSpace(subject.GetSubjectId())
			if !validAccessSubject(kind) {
				return nil, apierrors.Newf(apierrors.CodeInferAccessInvalid,
					"infer: unknown access subject type %q; use org, project or user", subject.GetSubjectType())
			}
			if id == "" {
				return nil, apierrors.Newf(apierrors.CodeInferAccessInvalid,
					"infer: access subject of type %s needs an id", kind)
			}
			key := kind + "/" + id
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			subjects = append(subjects, &InferenceServiceAccess{SubjectType: kind, SubjectID: id})
		}
	}

	actor, _, _ := s.resolveSessionActor(ctx)
	if err := repo.ReplaceAccess(ctx, req.GetServiceId(), actor, subjects); err != nil {
		return nil, err
	}
	rows, err := repo.ListAccess(ctx, req.GetServiceId())
	if err != nil {
		return nil, err
	}
	return &inferv1.SetInferenceServiceAccessResponse{
		Response:   okResponse(),
		Restricted: len(rows) > 0,
		Subjects:   s.labelAccessSubjects(ctx, rows),
	}, nil
}

// labelAccessSubjects projects stored rows onto the wire shape, resolving
// display labels per subject type.
func (s *Service) labelAccessSubjects(ctx context.Context, rows []*InferenceServiceAccess) []*inferv1.InferenceServiceAccessSubject {
	out := make([]*inferv1.InferenceServiceAccessSubject, 0, len(rows))
	byType := map[string][]string{}
	for _, row := range rows {
		byType[row.SubjectType] = append(byType[row.SubjectType], row.SubjectID)
	}
	labels := map[string]map[string]string{}
	if s.subjectLabeller != nil {
		for subjectType, ids := range byType {
			resolved, err := s.subjectLabeller.LabelSubjects(ctx, subjectType, ids)
			if err == nil {
				labels[subjectType] = resolved
			}
		}
	}
	for _, row := range rows {
		label := ""
		if resolved, ok := labels[row.SubjectType]; ok {
			label = resolved[row.SubjectID]
		}
		out = append(out, &inferv1.InferenceServiceAccessSubject{
			SubjectType: row.SubjectType,
			SubjectId:   row.SubjectID,
			Label:       label,
		})
	}
	return out
}

// requirePlatformOperator gates the access-policy RPCs. A service belongs
// to the platform, so deciding who may use it is a platform decision, not
// an organization one.
func (s *Service) requirePlatformOperator(ctx context.Context) error {
	if s.platformAdmin == nil {
		return nil
	}
	ok, err := s.platformAdmin.IsPlatformAdmin(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return apierrors.Newf(apierrors.CodeForbidden,
			"infer: managing an inference service's access policy requires a platform administrator")
	}
	return nil
}
