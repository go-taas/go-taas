package tenancy

import (
	"context"
	"strings"
	"time"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// Per-user quotas inside an organization (the personal tier of the
// three-level quota model).
//
// The organization budget caps the tenant, the project budget caps a
// project, and a user budget caps one person. The data plane enforces
// the minimum of the non-zero values, so a personal budget can tighten
// the tenant budget but never loosen it. A 0 limit is unlimited,
// matching the organization and project conventions.

// ListUserQuotas returns the organization's roster with each user's
// quota. Requires organization owner/admin or a platform administrator.
func (s *Service) ListUserQuotas(ctx context.Context, req *tenancyv1.ListUserQuotasRequest) (*tenancyv1.ListUserQuotasResponse, error) {
	orgID, err := s.resolveQuotaOrg(ctx, req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	if err := s.requireOrgAdminOrPlatform(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	filter := UserQuotaFilter{
		OrganizationID: orgID,
		Name:           strings.TrimSpace(req.GetName()),
		Offset:         offset,
		Limit:          limit,
	}
	// The roster is the source of who belongs to the organization: a
	// member without a stored row still appears with unlimited limits,
	// so the console can set a first budget.
	roster, total, err := s.quotaRoster(ctx, orgID, filter.Name, offset, limit)
	if err != nil {
		return nil, err
	}
	userIDs := make([]string, 0, len(roster))
	for _, m := range roster {
		userIDs = append(userIDs, m.UserID)
	}
	rows, _, err := repo.ListUserQuotas(ctx, UserQuotaFilter{
		OrganizationID: orgID,
		UserIDs:        userIDs,
	})
	if err != nil {
		return nil, err
	}
	stored := make(map[string]*UserQuota, len(rows))
	for _, row := range rows {
		stored[row.UserID] = row
	}
	labels := s.labelsForUsers(ctx, userIDs)
	quotas := make([]*tenancyv1.UserQuota, 0, len(roster))
	for _, m := range roster {
		quotas = append(quotas, summarizeUserQuota(stored[m.UserID], orgID, m.UserID, m.Role, labels))
	}
	return &tenancyv1.ListUserQuotasResponse{
		Response: okResponse(),
		Quotas:   quotas,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// quotaRoster returns one page of the organization members matching the
// name filter, plus the pre-pagination count. The quota list is
// member-driven: a budget without a member would be a row nobody can use.
func (s *Service) quotaRoster(ctx context.Context, orgID, name string, offset, limit int) ([]*OrgMember, int64, error) {
	mrepo, err := s.membershipRepo()
	if err != nil {
		return nil, 0, err
	}
	members, _, err := mrepo.ListMembers(ctx, orgID, 0, -1)
	if err != nil {
		return nil, 0, err
	}
	needle := strings.ToLower(strings.TrimSpace(name))
	filtered := make([]*OrgMember, 0, len(members))
	for _, m := range members {
		if needle != "" && !strings.Contains(strings.ToLower(m.UserID), needle) {
			continue
		}
		filtered = append(filtered, m)
	}
	total := int64(len(filtered))
	if offset >= len(filtered) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], total, nil
}

// GetUserQuota returns one user's quota inside the organization.
func (s *Service) GetUserQuota(ctx context.Context, req *tenancyv1.GetUserQuotaRequest) (*tenancyv1.GetUserQuotaResponse, error) {
	orgID, err := s.resolveQuotaOrg(ctx, req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	if err := s.requireOrgAdminOrPlatform(ctx, orgID); err != nil {
		return nil, err
	}
	userID := strings.TrimSpace(req.GetUserId())
	if userID == "" {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindUserQuota(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	role, err := s.memberRole(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	labels := s.labelsForUsers(ctx, []string{userID})
	return &tenancyv1.GetUserQuotaResponse{
		Response: okResponse(),
		Quota:    summarizeUserQuota(row, orgID, userID, role, labels),
	}, nil
}

// UpdateUserQuota sets any subset of one user's limits. Requires
// organization owner/admin or a platform administrator. A nil field is
// left unchanged; an explicit 0 is unlimited.
func (s *Service) UpdateUserQuota(ctx context.Context, req *tenancyv1.UpdateUserQuotaRequest) (*tenancyv1.UpdateUserQuotaResponse, error) {
	orgID, err := s.resolveQuotaOrg(ctx, req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	if err := s.requireOrgAdminOrPlatform(ctx, orgID); err != nil {
		return nil, err
	}
	userID := strings.TrimSpace(req.GetUserId())
	if userID == "" {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	role, err := s.memberRole(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	if role == "" {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	patch := map[string]int64{}
	for _, dim := range []struct {
		name  string
		value *int64
	}{
		{"max_concurrent_requests", req.MaxConcurrentRequests},
		{"max_inference_services", req.MaxInferenceServices},
		{"max_batch_jobs_per_day", req.MaxBatchJobsPerDay},
		{"max_output_tokens_per_request", req.MaxOutputTokensPerRequest},
		{"monthly_spend_limit_cents", req.MonthlySpendLimitCents},
	} {
		if dim.value == nil {
			continue
		}
		if err := ValidateQuota(dim.name, *dim.value); err != nil {
			return nil, err
		}
		patch[dim.name] = *dim.value
	}
	// The audit actor falls back to "system" on the transitional
	// (session-less) path; a missing session must not block an edit the
	// admin gate already authorized.
	actor := s.actor(ctx)
	now := time.Now().UTC()
	row, err := repo.FindUserQuota(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		row = &UserQuota{OrganizationID: orgID, UserID: userID}
	}
	applyUserQuotaPatch(row, patch)
	row.QuotaUpdatedBy = actor
	row.QuotaUpdatedAt = now
	if err := repo.UpsertUserQuota(ctx, row); err != nil {
		return nil, err
	}
	labels := s.labelsForUsers(ctx, []string{userID, actor})
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    actor,
		ActorType:      "user",
		Action:         "user.quota_update",
		ResourceType:   "user",
		ResourceID:     userID,
		Result:         "success",
	})
	orgLimits, err := repo.OrgQuota(ctx, orgID)
	if err != nil {
		return nil, err
	}
	effective := CombineLimits(orgLimits.Limits(), row.Limits())
	return &tenancyv1.UpdateUserQuotaResponse{
		Response:       okResponse(),
		Quota:          summarizeUserQuota(row, orgID, userID, role, labels),
		EffectiveQuota: effectiveQuotaProto(effective),
	}, nil
}

// EffectiveQuota implements tenancy.QuotaResolver: it combines the
// organization, project and personal limits into the tuple the data
// plane enforces. A missing project or user row reads as unlimited.
func (s *Service) EffectiveQuota(ctx context.Context, orgID, projectID, userID string) (EffectiveQuota, error) {
	repo, err := s.repository()
	if err != nil {
		return EffectiveQuota{}, err
	}
	orgLimits, err := repo.OrgQuota(ctx, orgID)
	if err != nil {
		return EffectiveQuota{}, err
	}
	projectLimits, err := repo.ProjectQuotaLimits(ctx, projectID)
	if err != nil {
		return EffectiveQuota{}, err
	}
	userLimits := QuotaLimits{}
	if userID != "" && orgID != "" {
		userLimits, err = repo.UserQuotaLimits(ctx, orgID, userID)
		if err != nil {
			return EffectiveQuota{}, err
		}
	}
	return CombineLimits(orgLimits.Limits(), projectLimits, userLimits), nil
}

// resolveQuotaOrg resolves the organization a quota RPC operates on:
// the explicit request field when present, else the caller's session.
func (s *Service) resolveQuotaOrg(ctx context.Context, requested string) (string, error) {
	orgID := strings.TrimSpace(requested)
	if orgID == "" {
		org, err := s.resolveOrg(ctx)
		if err != nil {
			return "", err
		}
		orgID = org
	}
	if orgID == "" {
		return "", apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if _, err := s.requireQuotaOrgExists(ctx, orgID); err != nil {
		return "", err
	}
	return orgID, nil
}

// requireQuotaOrgExists validates that the organization exists.
func (s *Service) requireQuotaOrgExists(ctx context.Context, orgID string) (*Organization, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	org, err := repo.FindOrganization(ctx, orgID)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err != nil {
		return nil, err
	}
	return org, nil
}

// memberRole returns the user's role in the organization, or "" when the
// user is not a member.
func (s *Service) memberRole(ctx context.Context, orgID, userID string) (string, error) {
	mrepo, err := s.membershipRepo()
	if err != nil {
		return "", err
	}
	m, err := mrepo.FindMember(ctx, orgID, userID)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", nil
	}
	return m.Role, nil
}

// applyUserQuotaPatch copies the present dimensions onto the row.
func applyUserQuotaPatch(row *UserQuota, patch map[string]int64) {
	if v, ok := patch["max_concurrent_requests"]; ok {
		row.MaxConcurrentRequests = v
	}
	if v, ok := patch["max_inference_services"]; ok {
		row.MaxInferenceServices = v
	}
	if v, ok := patch["max_batch_jobs_per_day"]; ok {
		row.MaxBatchJobsPerDay = v
	}
	if v, ok := patch["max_output_tokens_per_request"]; ok {
		row.MaxOutputTokensPerRequest = v
	}
	if v, ok := patch["monthly_spend_limit_cents"]; ok {
		row.MonthlySpendLimitCents = v
	}
}

// summarizeUserQuota maps a stored row (or its absence) to the wire
// projection.
func summarizeUserQuota(row *UserQuota, orgID, userID, role string, labels map[string]string) *tenancyv1.UserQuota {
	q := &tenancyv1.UserQuota{
		OrganizationId: orgID,
		UserId:         userID,
		DisplayName:    labels[userID],
		Role:           role,
	}
	if q.DisplayName == "" {
		q.DisplayName = userID
	}
	if row != nil {
		q.MaxConcurrentRequests = row.MaxConcurrentRequests
		q.MaxInferenceServices = row.MaxInferenceServices
		q.MaxBatchJobsPerDay = row.MaxBatchJobsPerDay
		q.MaxOutputTokensPerRequest = row.MaxOutputTokensPerRequest
		q.MonthlySpendLimitCents = row.MonthlySpendLimitCents
		q.UpdatedBy = row.QuotaUpdatedBy
		q.UpdatedByDisplayName = labels[row.QuotaUpdatedBy]
		if !row.QuotaUpdatedAt.IsZero() {
			q.UpdatedAt = row.QuotaUpdatedAt.Unix()
		}
	}
	return q
}

// effectiveQuotaProto maps the resolved tuple to the wire projection.
func effectiveQuotaProto(q EffectiveQuota) *tenancyv1.EffectiveQuota {
	return &tenancyv1.EffectiveQuota{
		MaxConcurrentRequests:     q.MaxConcurrentRequests,
		MaxInferenceServices:      q.MaxInferenceServices,
		MaxBatchJobsPerDay:        q.MaxBatchJobsPerDay,
		MaxOutputTokensPerRequest: q.MaxOutputTokensPerRequest,
		MonthlySpendLimitCents:    q.MonthlySpendLimitCents,
	}
}
