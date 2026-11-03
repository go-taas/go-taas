package tenancy

import (
	"context"
	"time"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// Per-member project quotas (project administrator console).
//
// A project administrator budgets the people in the project, not the
// project as a whole: each member carries their own limits, so a generous
// cap for one person never silently applies to the next. A 0 limit is
// unlimited, matching the organization and project quota conventions.
//
// This is the member tier of the quota ladder (organization → project →
// user). It is stored on the membership row because the budget is scoped to
// the project: the same person can carry a different cap in every project, and
// losing the membership drops the budget with it. The effective value is the
// tighter of this tier and the person's organization-wide budget, so a member
// cap can only tighten what the tenant already allows.

// ListProjectMemberQuotas returns the project's roster with each member's
// quota. Requires project admin or org admin/owner.
func (s *Service) ListProjectMemberQuotas(ctx context.Context, req *tenancyv1.ListProjectMemberQuotasRequest) (*tenancyv1.ListProjectMemberQuotasResponse, error) {
	projectID := req.GetProjectId()
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
	mrepo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	members, err := mrepo.ListAllProjectMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	userIDs := make([]string, 0, len(members))
	editorIDs := make([]string, 0, len(members))
	for _, m := range members {
		userIDs = append(userIDs, m.UserID)
		if m.QuotaUpdatedBy != "" {
			editorIDs = append(editorIDs, m.QuotaUpdatedBy)
		}
	}
	labels := s.labelsForUsers(ctx, append(userIDs, editorIDs...))
	quotas := make([]*tenancyv1.ProjectMemberQuota, 0, len(members))
	for _, m := range members {
		quota := summarizeMemberQuota(m, project, labels)
		if effective, effErr := s.EffectiveQuota(ctx, project.OrganizationID, projectID, m.UserID); effErr == nil {
			quota.Effective = effectiveQuotaProto(effective)
		}
		quotas = append(quotas, quota)
	}
	return &tenancyv1.ListProjectMemberQuotasResponse{Response: okResponse(), Quotas: quotas}, nil
}

// UpdateProjectMemberQuota sets one member's quota. Requires project
// admin or org admin/owner. A nil field is left unchanged; 0 is
// unlimited.
func (s *Service) UpdateProjectMemberQuota(ctx context.Context, req *tenancyv1.UpdateProjectMemberQuotaRequest) (*tenancyv1.UpdateProjectMemberQuotaResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdmin(ctx, projectID); err != nil {
		return nil, err
	}
	userID := req.GetUserId()
	if userID == "" {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
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
	mrepo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	member, err := mrepo.FindProjectMember(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		// A quota belongs to a member: assigning one to somebody outside
		// the project would create a phantom roster entry.
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	if v, ok := optionalQuota(req.MaxConcurrentRequests); ok {
		member.MaxConcurrentRequests = v
	}
	if v, ok := optionalQuota(req.MaxBatchJobsPerDay); ok {
		member.MaxBatchJobsPerDay = v
	}
	if v, ok := optionalQuota(req.MaxOutputTokensPerRequest); ok {
		member.MaxOutputTokensPerRequest = v
	}
	if v, ok := optionalQuota(req.MonthlySpendLimitCents); ok {
		member.MonthlySpendLimitCents = v
	}
	actor, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	member.QuotaUpdatedBy = actor
	member.QuotaUpdatedAt = time.Now().UTC()
	if err := mrepo.UpdateProjectMemberQuota(ctx, projectID, userID, member); err != nil {
		return nil, err
	}
	// Re-read so the response carries the stored row rather than the
	// request's view of it.
	if stored, err := mrepo.FindProjectMember(ctx, projectID, userID); err == nil && stored != nil {
		member = stored
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: project.OrganizationID,
		ActorUserID:    member.QuotaUpdatedBy,
		ActorType:      "user",
		Action:         "project.member_quota_update",
		ResourceType:   "project_member",
		ResourceID:     projectID + "/" + userID,
		Result:         "success",
	})
	labels := s.labelsForUsers(ctx, []string{userID, member.QuotaUpdatedBy})
	quota := summarizeMemberQuota(member, project, labels)
	// The response carries the combined ladder, so the console can show what
	// the data plane will actually enforce next to the value that was just
	// stored (a member cap looser than the personal budget is not the limit).
	if effective, effErr := s.EffectiveQuota(ctx, project.OrganizationID, projectID, userID); effErr == nil {
		quota.Effective = effectiveQuotaProto(effective)
	}
	return &tenancyv1.UpdateProjectMemberQuotaResponse{
		Response: okResponse(),
		Quota:    quota,
	}, nil
}

// summarizeMemberQuota maps a roster row to the quota message.
func summarizeMemberQuota(m *ProjectMember, p *Project, labels map[string]string) *tenancyv1.ProjectMemberQuota {
	q := &tenancyv1.ProjectMemberQuota{
		ProjectId:                 p.ID,
		OrganizationId:            p.OrganizationID,
		UserId:                    m.UserID,
		DisplayName:               labels[m.UserID],
		Role:                      m.Role,
		MaxConcurrentRequests:     m.MaxConcurrentRequests,
		MaxBatchJobsPerDay:        m.MaxBatchJobsPerDay,
		MaxOutputTokensPerRequest: m.MaxOutputTokensPerRequest,
		MonthlySpendLimitCents:    m.MonthlySpendLimitCents,
		UpdatedBy:                 m.QuotaUpdatedBy,
		UpdatedByDisplayName:      labels[m.QuotaUpdatedBy],
	}
	if q.DisplayName == "" {
		q.DisplayName = m.UserID
	}
	if !m.QuotaUpdatedAt.IsZero() {
		q.UpdatedAt = m.QuotaUpdatedAt.Unix()
	}
	return q
}

// labelsForUsers resolves user ids to display labels. It is the
// id-list form of userLabels (which takes roster rows); a nil resolver
// yields no labels and the callers fall back to the ids.
func (s *Service) labelsForUsers(ctx context.Context, userIDs []string) map[string]string {
	if s.userLabelResolver == nil || len(userIDs) == 0 {
		return nil
	}
	labels, err := s.userLabelResolver.ResolveUserLabels(ctx, userIDs)
	if err != nil {
		return nil
	}
	return labels
}
