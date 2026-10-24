package tenancy

import (
	"context"
	"time"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// quotaMax bounds a single quota value (the documented ranges).
const quotaMax = 1_000_000

// GetProjectQuota returns a project's quotas. Requires project admin or
// org admin/owner.
func (s *Service) GetProjectQuota(ctx context.Context, req *tenancyv1.GetProjectQuotaRequest) (*tenancyv1.GetProjectQuotaResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdmin(ctx, projectID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	p, err := repo.FindProject(ctx, projectID)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeProjectNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &tenancyv1.GetProjectQuotaResponse{Response: okResponse(), Quota: summarizeProjectQuota(p)}, nil
}

// UpdateProjectQuota updates a project's quotas. Requires project admin
// or org admin/owner. A nil field is left unchanged.
func (s *Service) UpdateProjectQuota(ctx context.Context, req *tenancyv1.UpdateProjectQuotaRequest) (*tenancyv1.UpdateProjectQuotaResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdmin(ctx, projectID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	p, err := repo.FindProject(ctx, projectID)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeProjectNotFound)
	}
	if err != nil {
		return nil, err
	}
	if v, ok := optionalQuota(req.MaxConcurrentRequests); ok {
		p.MaxConcurrentRequests = v
	}
	if v, ok := optionalQuota(req.MaxInferenceServices); ok {
		p.MaxInferenceServices = v
	}
	if v, ok := optionalQuota(req.MaxBatchJobsPerDay); ok {
		p.MaxBatchJobsPerDay = v
	}
	if v, ok := optionalQuota(req.MaxOutputTokensPerRequest); ok {
		p.MaxOutputTokensPerRequest = v
	}
	actor, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	p.QuotaUpdatedBy = actor
	p.QuotaUpdatedAt = time.Now().UTC()
	if err := repo.UpdateProjectQuotas(ctx, p); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: p.OrganizationID,
		ActorUserID:    p.QuotaUpdatedBy,
		ActorType:      "user",
		Action:         "project.quota_update",
		ResourceType:   "project",
		ResourceID:     p.ID,
		Result:         "success",
	})
	return &tenancyv1.UpdateProjectQuotaResponse{Response: okResponse(), Quota: summarizeProjectQuota(p)}, nil
}

// optionalQuota validates and unwraps an optional quota value.
func optionalQuota(v *int64) (int64, bool) {
	if v == nil {
		return 0, false
	}
	val := *v
	if val < 0 {
		val = 0
	}
	if val > quotaMax {
		val = quotaMax
	}
	return val, true
}

// summarizeProjectQuota maps a project row to the quota message.
func summarizeProjectQuota(p *Project) *tenancyv1.ProjectQuota {
	q := &tenancyv1.ProjectQuota{
		ProjectId:                 p.ID,
		OrganizationId:            p.OrganizationID,
		ProjectName:               p.DisplayName,
		MaxConcurrentRequests:     p.MaxConcurrentRequests,
		MaxInferenceServices:      p.MaxInferenceServices,
		MaxBatchJobsPerDay:        p.MaxBatchJobsPerDay,
		MaxOutputTokensPerRequest: p.MaxOutputTokensPerRequest,
		UpdatedBy:                 p.QuotaUpdatedBy,
	}
	if !p.QuotaUpdatedAt.IsZero() {
		q.UpdatedAt = p.QuotaUpdatedAt.Unix()
	}
	return q
}
