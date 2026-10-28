package tenancy

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/grpc/metadata"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	"github.com/go-taas/go-taas/pkg/authctx"
	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/services/audit"
)

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional X-Organization-Id header (the auth module's pattern).
const organizationMetadataKey = "x-organization-id"

// roleAdmin is the minimum role for the admin quota RPCs (feature #51,
// AD13).
const roleAdmin = "admin"

// quotaChangeEntryJSON is the audit metadata shape for one dimension
// change (feature #51, AD16).
type quotaChangeEntryJSON struct {
	Dimension string `json:"dimension"`
	Old       int64  `json:"old"`
	New       int64  `json:"new"`
}

// requireAdminRole enforces the admin role on the admin quota RPCs
// (feature #51, AD13). The quota RPCs are platform-scoped, so when the
// session carries no active org there is nothing to resolve a role in
// and the check is skipped (the admin realm guard is the boundary).
func (s *Service) requireAdminRole(ctx context.Context) error {
	if s.roleGuard == nil {
		return nil
	}
	orgID := ""
	if s.sessionOrgResolver != nil {
		org, err := s.sessionOrgResolver.SessionActiveOrg(ctx)
		if err != nil && apierrors.CodeOf(err) != apierrors.CodeSessionInvalid {
			return err
		}
		orgID = org
	}
	if orgID == "" {
		return nil
	}
	userID, hasSession, err := s.resolveSessionActor(ctx)
	if err != nil {
		return err
	}
	if !hasSession {
		return nil
	}
	return s.roleGuard.RequireRole(ctx, orgID, userID, roleAdmin)
}

// resolveSessionActor returns the authenticated caller's user id, or
// ("", false, nil) when there is no valid session.
func (s *Service) resolveSessionActor(ctx context.Context) (string, bool, error) {
	if s.sessionResolver == nil {
		return "", false, nil
	}
	userID, err := s.sessionResolver.SessionUserID(ctx)
	if err != nil {
		if apierrors.CodeOf(err) == apierrors.CodeSessionInvalid {
			return "", false, nil
		}
		return "", false, err
	}
	return userID, true, nil
}

// actor returns the acting user id for audit, or "system" when unknown.
func (s *Service) actor(ctx context.Context) string {
	userID, ok, _ := s.resolveSessionActor(ctx)
	if ok {
		return userID
	}
	return "system"
}

// resolveOrg returns the organization context for a user-realm read
// (feature #51, AD13): the session's active org when a session is
// present, otherwise the transitional X-Organization-Id header.
func (s *Service) resolveOrg(ctx context.Context) (string, error) {
	if s.sessionOrgResolver != nil {
		if org, err := s.sessionOrgResolver.SessionActiveOrg(ctx); err != nil {
			return "", err
		} else if org != "" {
			return org, nil
		}
	}
	org, err := resolveOrganizationID(ctx)
	if err != nil && authctx.IsPlatformAdmin(ctx) {
		// A platform administrator belongs to no organization: the platform
		// console is platform-wide, so an empty organization is a valid
		// context. Reads then span every organization (or return nothing);
		// a write still needs a real, active organization.
		return "", nil
	}
	return org, err
}

// resolveOrganizationID reads the transitional caller organization from
// the x-organization-id gRPC metadata (set by the gateway from the
// X-Organization-Id HTTP header). Missing or empty values are
// unauthorized.
func resolveOrganizationID(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	values := md.Get(organizationMetadataKey)
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	return strings.TrimSpace(values[0]), nil
}

// quotaUsage assembles the best-effort live-usage snapshot for one org
// (feature #51, FR1.2/FR4.1). A COUNT error logs a warning and yields 0
// for that counter — the list never fails on usage.
func (s *Service) quotaUsage(ctx context.Context, repo *Repository, orgID string) *tenancyv1.QuotaUsage {
	usage := &tenancyv1.QuotaUsage{}
	if s.concurrency != nil {
		usage.ConcurrentRequests = s.concurrency.CurrentInFlight(orgID)
	}
	if count, err := repo.CountActiveServicesByOrg(ctx, orgID); err != nil {
		logger.S().Warnw("tenancy: service-count usage read failed", "org", orgID, "err", err)
	} else {
		usage.InferenceServices = count
	}
	if count, err := repo.CountBatchJobsSince(ctx, orgID, todayAtMidnight()); err != nil {
		logger.S().Warnw("tenancy: batch-today usage read failed", "org", orgID, "err", err)
	} else {
		usage.BatchJobsToday = count
	}
	return usage
}

// todayAtMidnight returns the local midnight of today in the configured
// quota timezone (feature #51, AD15). The timezone defaults to UTC.
func todayAtMidnight() time.Time {
	tzName := "UTC"
	if cfg := config.GetConfig(); cfg != nil && cfg.Quota.Timezone != "" {
		tzName = cfg.Quota.Timezone
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UTC()
}

// quotaProto maps an org row to the OrganizationQuota proto projection.
// updatedByName is the resolved editor label (empty falls back to the id).
func quotaProto(org *Organization, updatedByName string) *tenancyv1.OrganizationQuota {
	q := &tenancyv1.OrganizationQuota{
		OrganizationId:            org.ID,
		OrganizationName:          org.DisplayName,
		MaxConcurrentRequests:     org.MaxConcurrentRequests,
		MaxInferenceServices:      org.MaxInferenceServices,
		MaxBatchJobsPerDay:        org.MaxBatchJobsPerDay,
		MaxOutputTokensPerRequest: org.MaxOutputTokensPerRequest,
		UpdatedBy:                 org.QuotaUpdatedBy,
		UpdatedByDisplayName:      updatedByName,
	}
	if !org.QuotaUpdatedAt.IsZero() {
		q.UpdatedAt = org.QuotaUpdatedAt.Unix()
	}
	return q
}

// OrgQuota implements tenancy.OrgQuotaReader (feature #51, AD13): the
// four org quota limits off the repository. An unknown org returns the
// zero struct (all unlimited), never an error — a missing org during
// verdict construction must read as unlimited, never fail auth (AD12's
// fail-open rule).
func (s *Service) OrgQuota(ctx context.Context, orgID string) (OrgQuota, error) {
	repo, err := s.repository()
	if err != nil {
		return OrgQuota{}, err
	}
	return repo.OrgQuota(ctx, orgID)
}

// ListOrganizationQuotas returns every organization's quota row with
// best-effort live usage (feature #51, FR1.2).
func (s *Service) ListOrganizationQuotas(ctx context.Context, req *tenancyv1.ListOrganizationQuotasRequest) (*tenancyv1.ListOrganizationQuotasResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	limitState := req.GetLimitState()
	if limitState != "" && limitState != "all" && limitState != "limited" && limitState != "unlimited" {
		return nil, apierrors.Newf(apierrors.CodeQuotaInvalid, "limit_state must be all, limited or unlimited")
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListOrganizationQuotas(ctx, QuotaListFilter{
		Name:       req.GetName(),
		LimitState: limitState,
		Offset:     offset,
		Limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	quotas := make([]*tenancyv1.OrganizationQuotaRow, 0, len(rows))
	for _, org := range rows {
		quotas = append(quotas, &tenancyv1.OrganizationQuotaRow{
			Quota: quotaProto(org, s.userLabel(ctx, org.QuotaUpdatedBy)),
			Usage: s.quotaUsage(ctx, repo, org.ID),
		})
	}
	return &tenancyv1.ListOrganizationQuotasResponse{
		Response: okResponse(),
		Quotas:   quotas,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetOrganizationQuota returns one org's quota, usage and the last 20
// change-history entries (feature #51, FR1.3).
func (s *Service) GetOrganizationQuota(ctx context.Context, req *tenancyv1.GetOrganizationQuotaRequest) (*tenancyv1.GetOrganizationQuotaResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	org, err := repo.GetOrganizationQuota(ctx, req.GetOrganizationId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeQuotaNotFound)
	}
	if err != nil {
		return nil, err
	}
	changes, err := repo.ListQuotaChanges(ctx, org.ID, 20)
	if err != nil {
		return nil, err
	}
	entries := make([]*tenancyv1.QuotaChangeEntry, 0, len(changes))
	for _, ch := range changes {
		entries = append(entries, &tenancyv1.QuotaChangeEntry{
			ChangedAt: ch.ChangedAt.Unix(),
			Operator:  ch.Operator,
			Dimension: ch.Dimension,
			OldValue:  ch.OldValue,
			NewValue:  ch.NewValue,
		})
	}
	return &tenancyv1.GetOrganizationQuotaResponse{
		Response: okResponse(),
		Quota:    quotaProto(org, s.userLabel(ctx, org.QuotaUpdatedBy)),
		Usage:    s.quotaUsage(ctx, repo, org.ID),
		Changes:  entries,
	}, nil
}

// UpdateOrganizationQuota sets any subset of the four dimensions
// (feature #51, FR1.4). Out-of-range values return 13305 with the
// dimension named; a no-op patch returns the current quota without
// writing or auditing.
func (s *Service) UpdateOrganizationQuota(ctx context.Context, req *tenancyv1.UpdateOrganizationQuotaRequest) (*tenancyv1.UpdateOrganizationQuotaResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	org, err := repo.GetOrganizationQuota(ctx, req.GetOrganizationId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeQuotaNotFound)
	}
	if err != nil {
		return nil, err
	}

	// Validate each present dimension and diff against the current value.
	patch := map[string]int64{}
	changes := []quotaChangeEntryJSON{}
	apply := func(dim string, present bool, value int64, current int64) error {
		if !present {
			return nil
		}
		if err := ValidateQuota(dim, value); err != nil {
			return err
		}
		if value != current {
			patch[dim] = value
			changes = append(changes, quotaChangeEntryJSON{Dimension: dim, Old: current, New: value})
		}
		return nil
	}
	if err := apply("max_concurrent_requests", req.MaxConcurrentRequests != nil, req.GetMaxConcurrentRequests(), org.MaxConcurrentRequests); err != nil {
		return nil, err
	}
	if err := apply("max_inference_services", req.MaxInferenceServices != nil, req.GetMaxInferenceServices(), org.MaxInferenceServices); err != nil {
		return nil, err
	}
	if err := apply("max_batch_jobs_per_day", req.MaxBatchJobsPerDay != nil, req.GetMaxBatchJobsPerDay(), org.MaxBatchJobsPerDay); err != nil {
		return nil, err
	}
	if err := apply("max_output_tokens_per_request", req.MaxOutputTokensPerRequest != nil, req.GetMaxOutputTokensPerRequest(), org.MaxOutputTokensPerRequest); err != nil {
		return nil, err
	}

	if len(patch) == 0 {
		// No-op: return the current quota + usage, no write, no audit.
		return &tenancyv1.UpdateOrganizationQuotaResponse{
			Response: okResponse(),
			Quota:    quotaProto(org, s.userLabel(ctx, org.QuotaUpdatedBy)),
			Usage:    s.quotaUsage(ctx, repo, org.ID),
		}, nil
	}

	now := time.Now().UTC()
	actor := s.actor(ctx)
	if err := repo.UpdateOrganizationQuota(ctx, org.ID, patch, actor, now); err != nil {
		return nil, err
	}

	// Audit the change (best-effort, AD16).
	if s.auditRecorder != nil {
		meta, _ := json.Marshal(map[string]any{"changes": changes})
		s.auditRecorder.Record(ctx, &audit.AuditEvent{
			OrganizationID: org.ID,
			ActorUserID:    actor,
			ActorType:      "user",
			Action:         "quota.updated",
			ResourceType:   "organization",
			ResourceID:     org.ID,
			Result:         "success",
			Metadata:       string(meta),
		})
	}

	updated, err := repo.GetOrganizationQuota(ctx, org.ID)
	if err != nil {
		return nil, err
	}
	return &tenancyv1.UpdateOrganizationQuotaResponse{
		Response: okResponse(),
		Quota:    quotaProto(updated, s.userLabel(ctx, updated.QuotaUpdatedBy)),
		Usage:    s.quotaUsage(ctx, repo, org.ID),
	}, nil
}

// GetMyQuota returns the caller's own org quota with live usage
// (feature #51, FR4.1). The org is resolved from the session, never
// from a client-supplied id.
func (s *Service) GetMyQuota(ctx context.Context, _ *tenancyv1.GetMyQuotaRequest) (*tenancyv1.GetMyQuotaResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	org, err := repo.GetOrganizationQuota(ctx, orgID)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeQuotaNotFound)
	}
	if err != nil {
		return nil, err
	}
	requestIncreaseEmail := ""
	if cfg := config.GetConfig(); cfg != nil {
		requestIncreaseEmail = cfg.Quota.RequestIncreaseTo
	}
	return &tenancyv1.GetMyQuotaResponse{
		Response:             okResponse(),
		Quota:                quotaProto(org, s.userLabel(ctx, org.QuotaUpdatedBy)),
		Usage:                s.quotaUsage(ctx, repo, org.ID),
		RequestIncreaseEmail: requestIncreaseEmail,
	}, nil
}
