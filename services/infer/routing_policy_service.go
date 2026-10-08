package infer

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// routingPolicyRepo lazily resolves the routing-policy repository.
func (s *Service) routingPolicyRepository() (*RoutingPolicyRepository, error) {
	if s.routingPolicyRepo != nil {
		return s.routingPolicyRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.routingPolicyRepo = NewRoutingPolicyRepository(db)
	return s.routingPolicyRepo, nil
}

// requireRoutingPolicyAdmin enforces the admin/owner role on every
// routing-policy RPC (feature #45, AD6). Unlike the transitional admin
// RPCs, a missing session context is NOT authorized: the platform-wide
// policy data is not organization-scoped, so the caller must be an
// authenticated admin of the active organization.
func (s *Service) requireRoutingPolicyAdmin(ctx context.Context) (string, error) {
	if s.roleGuard == nil || s.sessionUserResolver == nil {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	userID, err := s.sessionUserResolver.SessionUserID(ctx)
	if err != nil {
		return "", err
	}
	if userID == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	orgID := ""
	if s.sessionOrgResolver != nil {
		if org, orgErr := s.sessionOrgResolver.SessionActiveOrg(ctx); orgErr == nil {
			orgID = org
		}
	}
	if err := s.roleGuard.RequireRole(ctx, orgID, userID, roleAdmin); err != nil {
		return "", err
	}
	return userID, nil
}

// routingPolicyInvalid returns the 10312 business error.
func routingPolicyInvalid() error { return apierrors.New(apierrors.CodeRoutingPolicyInvalid) }

// retryCategoryToString maps the proto enum to the stored value.
func retryCategoryToString(c inferv1.RetryCategory) (string, bool) {
	switch c {
	case inferv1.RetryCategory_RETRY_CATEGORY_CONNECT_TIMEOUT:
		return RetryOnConnectTimeout, true
	case inferv1.RetryCategory_RETRY_CATEGORY_HTTP_429:
		return RetryOnHTTP429, true
	case inferv1.RetryCategory_RETRY_CATEGORY_HTTP_5XX:
		return RetryOnHTTP5XX, true
	default:
		return "", false
	}
}

// stringToRetryCategory maps the stored value to the proto enum.
func stringToRetryCategory(v string) inferv1.RetryCategory {
	switch v {
	case RetryOnConnectTimeout:
		return inferv1.RetryCategory_RETRY_CATEGORY_CONNECT_TIMEOUT
	case RetryOnHTTP429:
		return inferv1.RetryCategory_RETRY_CATEGORY_HTTP_429
	case RetryOnHTTP5XX:
		return inferv1.RetryCategory_RETRY_CATEGORY_HTTP_5XX
	default:
		return inferv1.RetryCategory_RETRY_CATEGORY_UNSPECIFIED
	}
}

// parseIDs decodes a jsonb string array.
func parseIDs(raw string) []string {
	var ids []string
	_ = json.Unmarshal([]byte(raw), &ids)
	if ids == nil {
		ids = []string{}
	}
	return ids
}

// parseRetryOn decodes a jsonb retry-category array.
func parseRetryOn(raw string) []string {
	var values []string
	_ = json.Unmarshal([]byte(raw), &values)
	if values == nil {
		values = []string{}
	}
	return values
}

// routingPolicyEligible resolves the model's active version and the
// eligible (non-terminated, exact model+version) services (feature
// #45, §5.2). A model without an active catalog version has no
// eligible targets: the policy pins to the empty version and every
// target is rejected.
func (s *Service) routingPolicyEligible(ctx context.Context, modelID string) (string, []InferenceService, error) {
	modelRepo, err := s.modelRepository()
	if err != nil {
		return "", nil, err
	}
	if _, err := modelRepo.GetModel(ctx, modelID); err != nil {
		return "", nil, err
	}
	version, err := modelRepo.ActiveVersion(ctx, modelID)
	if err != nil {
		return "", nil, err
	}
	repo, err := s.routingPolicyRepository()
	if err != nil {
		return "", nil, err
	}
	if version == nil {
		return "", nil, nil
	}
	services, err := repo.ListEligibleServices(ctx, modelID, version.Version)
	if err != nil {
		return "", nil, err
	}
	return version.Version, services, nil
}

// routingPolicyToProto projects the stored row plus live counts.
func (s *Service) routingPolicyToProto(ctx context.Context, row *RoutingPolicy, modelVersion string, eligible, ready int) *inferv1.RoutingPolicy {
	repo, err := s.routingPolicyRepository()
	if err != nil {
		repo = nil
	}
	publication := ""
	if repo != nil {
		publication = repo.PublicationState(ctx, row.ModelID, row.Revision)
	}
	categories := make([]inferv1.RetryCategory, 0, 4)
	for _, v := range parseRetryOn(row.RetryOn) {
		categories = append(categories, stringToRetryCategory(v))
	}
	version := row.ModelVersion
	if version == "" {
		version = modelVersion
	}
	return &inferv1.RoutingPolicy{
		ModelId:          row.ModelID,
		ModelVersion:     version,
		Enabled:          row.Enabled,
		ServiceIds:       parseIDs(row.ServiceIDs),
		MaxAttempts:      clampToInt32(row.MaxAttempts),
		RetryOn:          categories,
		Revision:         row.Revision,
		UpdatedAt:        row.UpdatedAt.UTC().Format(time.RFC3339),
		PublicationState: publication,
		EligibleCount:    clampToInt32(eligible),
		ReadyCount:       clampToInt32(ready),
	}
}

// ListRoutingPolicies returns paginated model/policy summaries
// (feature #45, §5).
func (s *Service) ListRoutingPolicies(ctx context.Context, req *inferv1.ListRoutingPoliciesRequest) (*inferv1.ListRoutingPoliciesResponse, error) {
	if _, err := s.requireRoutingPolicyAdmin(ctx); err != nil {
		return nil, err
	}
	repo, err := s.routingPolicyRepository()
	if err != nil {
		return nil, err
	}
	offset, limit := routingPageBounds(req.GetPage())
	rows, total, err := repo.ListSummaries(ctx, RoutingPolicyFilter{
		Search:      strings.TrimSpace(req.GetSearch()),
		State:       strings.TrimSpace(req.GetState()),
		Accelerator: strings.TrimSpace(req.GetAccelerator()),
		Sort:        strings.TrimSpace(req.GetSort()),
		Offset:      offset,
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	policies := make([]*inferv1.RoutingPolicySummary, 0, len(rows))
	for i := range rows {
		row := rows[i]
		state := RoutingPolicyStateDefault
		switch {
		case row.Enabled && row.ReadyCount > 0:
			state = RoutingPolicyStateEnabled
		case row.Enabled:
			state = RoutingPolicyStateUnavailable
		}
		updatedAt := ""
		if row.UpdatedAt != nil {
			updatedAt = row.UpdatedAt.UTC().Format(time.RFC3339)
		}
		policies = append(policies, &inferv1.RoutingPolicySummary{
			ModelId:      row.ModelID,
			ModelName:    row.ModelName,
			ModelVersion: row.ModelVersion,
			Enabled:      row.Enabled,
			PolicyState:  state,
			TargetCount:  clampToInt32(row.TargetCount),
			ReadyCount:   clampToInt32(row.ReadyCount),
			MaxAttempts:  clampToInt32(row.MaxAttempts),
			UpdatedAt:    updatedAt,
		})
	}
	return &inferv1.ListRoutingPoliciesResponse{
		Response: okResponse(),
		Policies: policies,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// routingPageBounds normalizes the pagination window.
func routingPageBounds(page *commonv1.PageRequest) (int, int) {
	offset, limit := 0, 20
	if page != nil {
		if page.Offset > 0 {
			offset = int(page.Offset)
		}
		if page.Limit > 0 {
			limit = int(page.Limit)
		}
		if limit > 100 {
			limit = 100
		}
	}
	return offset, limit
}

// GetRoutingPolicy returns the current policy or the default state
// (feature #45, §5). Absence is not an error: it projects revision 0,
// enabled=false.
func (s *Service) GetRoutingPolicy(ctx context.Context, req *inferv1.GetRoutingPolicyRequest) (*inferv1.GetRoutingPolicyResponse, error) {
	if _, err := s.requireRoutingPolicyAdmin(ctx); err != nil {
		return nil, err
	}
	modelID := strings.TrimSpace(req.GetModelId())
	if _, err := uuid.Parse(modelID); err != nil {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	repo, err := s.routingPolicyRepository()
	if err != nil {
		return nil, err
	}
	version, services, err := s.routingPolicyEligible(ctx, modelID)
	if err != nil {
		return nil, err
	}
	ready := 0
	for i := range services {
		if services[i].State == StateRunning {
			ready++
		}
	}
	row, err := repo.GetCurrent(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		row = &RoutingPolicy{ModelID: modelID, ModelVersion: version, MaxAttempts: MinRoutingAttempts}
	}
	return &inferv1.GetRoutingPolicyResponse{
		Response: okResponse(),
		Policy:   s.routingPolicyToProto(ctx, row, version, len(services), ready),
	}, nil
}

// UpdateRoutingPolicy validates and atomically persists a new policy
// revision (feature #45, §5.2, AD5).
func (s *Service) UpdateRoutingPolicy(ctx context.Context, req *inferv1.UpdateRoutingPolicyRequest) (*inferv1.UpdateRoutingPolicyResponse, error) {
	actor, err := s.requireRoutingPolicyAdmin(ctx)
	if err != nil {
		return nil, err
	}
	modelID := strings.TrimSpace(req.GetModelId())
	if _, err := uuid.Parse(modelID); err != nil {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	repo, err := s.routingPolicyRepository()
	if err != nil {
		return nil, err
	}

	// Resolve the model's active version and eligible services
	// server-side (feature #45, §5): the RPC never trusts client
	// selector results.
	version, services, err := s.routingPolicyEligible(ctx, modelID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*InferenceService, len(services))
	ready := 0
	for i := range services {
		byID[services[i].ID] = &services[i]
		if services[i].State == StateRunning {
			ready++
		}
	}

	// Validation matrix (feature #45, §5.2).
	if strings.TrimSpace(req.GetModelVersion()) == "" {
		return nil, routingPolicyInvalid()
	}
	if req.GetMaxAttempts() < MinRoutingAttempts || req.GetMaxAttempts() > MaxRoutingAttempts {
		return nil, routingPolicyInvalid()
	}
	seenIDs := make(map[string]bool, len(req.GetServiceIds()))
	for _, id := range req.GetServiceIds() {
		if seenIDs[id] {
			return nil, routingPolicyInvalid()
		}
		seenIDs[id] = true
		svc, ok := byID[id]
		if !ok || svc.State == StateTerminated {
			return nil, routingPolicyInvalid()
		}
	}
	if len(seenIDs) > MaxRoutingTargets {
		return nil, routingPolicyInvalid()
	}
	seenCategories := make(map[inferv1.RetryCategory]bool, len(req.GetRetryOn()))
	retryOn := make([]string, 0, len(req.GetRetryOn()))
	for _, c := range req.GetRetryOn() {
		if c == inferv1.RetryCategory_RETRY_CATEGORY_UNSPECIFIED {
			return nil, routingPolicyInvalid()
		}
		if seenCategories[c] {
			return nil, routingPolicyInvalid()
		}
		seenCategories[c] = true
		value, ok := retryCategoryToString(c)
		if !ok {
			return nil, routingPolicyInvalid()
		}
		retryOn = append(retryOn, value)
	}
	if req.GetMaxAttempts() > MinRoutingAttempts && len(retryOn) == 0 {
		return nil, routingPolicyInvalid()
	}
	// Enabling requires at least one currently ready eligible target
	// (feature #45, §5.2).
	if req.GetEnabled() && ready == 0 {
		return nil, routingPolicyInvalid()
	}

	current, err := repo.GetCurrent(ctx, modelID)
	if err != nil {
		return nil, err
	}
	expected := int64(0)
	if current != nil {
		expected = current.Revision
	}
	if req.GetExpectedRevision() != expected {
		return nil, apierrors.New(apierrors.CodeRoutingPolicyRevisionConflict)
	}

	// Concise change summary for the immutable revision row.
	summary := routingChangeSummary(current, req, version)

	// The audit event joins the same transaction as the revision
	// (feature #45, §13.1): the audit repository resolves the tx from
	// the ctx the repository passes to auditFn.
	auditFn := func(txCtx context.Context) error {
		if s.auditRecorder == nil {
			return nil
		}
		ev := &audit.AuditEvent{
			ActorUserID:  actor,
			ActorType:    "user",
			Action:       "routing_policy.updated",
			ResourceType: "routing_policy",
			ResourceID:   modelID,
			Result:       "success",
			Metadata:     routingAuditMetadata(req, version, expected+1),
		}
		if recorder, ok := s.auditRecorder.(interface {
			RecordInTx(context.Context, *audit.AuditEvent) error
		}); ok {
			return recorder.RecordInTx(txCtx, ev)
		}
		// Fall back to the best-effort recorder; the revision remains
		// the authoritative history.
		s.auditRecorder.Record(txCtx, ev)
		return nil
	}

	stored, _, err := repo.CompareAndSwapUpdate(ctx, RoutingPolicyChange{
		ModelID:      modelID,
		ModelVersion: version,
		Enabled:      req.GetEnabled(),
		ServiceIDs:   req.GetServiceIds(),
		MaxAttempts:  int(req.GetMaxAttempts()),
		RetryOn:      retryOn,
		ExpectedRev:  expected,
		Actor:        actor,
		Summary:      summary,
	}, auditFn)
	if err != nil {
		return nil, err
	}
	return &inferv1.UpdateRoutingPolicyResponse{
		Response: okResponse(),
		Policy:   s.routingPolicyToProto(ctx, stored, version, len(services), ready),
	}, nil
}

// routingChangeSummary renders the concise revision summary.
func routingChangeSummary(current *RoutingPolicy, req *inferv1.UpdateRoutingPolicyRequest, version string) string {
	if current == nil || current.Revision == 0 {
		return "policy created"
	}
	parts := make([]string, 0, 4)
	if current.Enabled != req.GetEnabled() {
		if req.GetEnabled() {
			parts = append(parts, "enabled")
		} else {
			parts = append(parts, "disabled")
		}
	}
	if current.MaxAttempts != int(req.GetMaxAttempts()) {
		parts = append(parts, "attempts changed")
	}
	if len(parseIDs(current.ServiceIDs)) != len(req.GetServiceIds()) {
		parts = append(parts, "targets changed")
	}
	if len(parts) == 0 {
		parts = append(parts, "updated")
	}
	return strings.Join(parts, ", ") + " (" + version + ")"
}

// routingAuditMetadata builds the audit event metadata.
func routingAuditMetadata(req *inferv1.UpdateRoutingPolicyRequest, version string, revision int64) string {
	categories := make([]string, 0, len(req.GetRetryOn()))
	for _, c := range req.GetRetryOn() {
		if v, ok := retryCategoryToString(c); ok {
			categories = append(categories, v)
		}
	}
	meta, _ := json.Marshal(map[string]any{
		"model_version": version,
		"enabled":       req.GetEnabled(),
		"service_ids":   req.GetServiceIds(),
		"max_attempts":  req.GetMaxAttempts(),
		"retry_on":      categories,
		"revision":      revision,
	})
	return string(meta)
}

// ListRoutingTargetHealth returns the masked readiness projection of
// the model's eligible services (feature #45, §5). No endpoint URLs or
// pod identifiers are exposed.
func (s *Service) ListRoutingTargetHealth(ctx context.Context, req *inferv1.ListRoutingTargetHealthRequest) (*inferv1.ListRoutingTargetHealthResponse, error) {
	if _, err := s.requireRoutingPolicyAdmin(ctx); err != nil {
		return nil, err
	}
	modelID := strings.TrimSpace(req.GetModelId())
	if _, err := uuid.Parse(modelID); err != nil {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	repo, err := s.routingPolicyRepository()
	if err != nil {
		return nil, err
	}
	_, services, err := s.routingPolicyEligible(ctx, modelID)
	if err != nil {
		return nil, err
	}
	clusterIDs := make([]string, 0, len(services))
	for i := range services {
		if services[i].ClusterID != "" {
			clusterIDs = append(clusterIDs, services[i].ClusterID)
		}
	}
	clusterNames, err := repo.ClusterNames(ctx, clusterIDs)
	if err != nil {
		return nil, err
	}
	targets := make([]*inferv1.RoutingTargetHealth, 0, len(services))
	for i := range services {
		svc := services[i]
		readyReplicas := svc.AutoscalingCurrentReplicas
		if svc.State == StateRunning && readyReplicas == 0 {
			readyReplicas = svc.Replicas
		}
		targets = append(targets, &inferv1.RoutingTargetHealth{
			ServiceId:       svc.ID,
			ServiceName:     svc.Name,
			Accelerator:     svc.Accelerator,
			AcceleratorType: svc.AcceleratorType,
			ClusterId:       svc.ClusterID,
			ClusterName:     clusterNames[svc.ClusterID],
			DesiredReplicas: clampToInt32(svc.Replicas),
			ReadyReplicas:   clampToInt32(readyReplicas),
			EndpointReady:   svc.State == StateRunning && len(svc.Endpoints) > 0,
			UpdatedAt:       svc.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return &inferv1.ListRoutingTargetHealthResponse{
		Response: okResponse(),
		Targets:  targets,
	}, nil
}

// ListRoutingPolicyRevisions returns the newest 20 immutable revisions
// with before/after values (feature #45, §5).
func (s *Service) ListRoutingPolicyRevisions(ctx context.Context, req *inferv1.ListRoutingPolicyRevisionsRequest) (*inferv1.ListRoutingPolicyRevisionsResponse, error) {
	if _, err := s.requireRoutingPolicyAdmin(ctx); err != nil {
		return nil, err
	}
	modelID := strings.TrimSpace(req.GetModelId())
	if _, err := uuid.Parse(modelID); err != nil {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	repo, err := s.routingPolicyRepository()
	if err != nil {
		return nil, err
	}
	offset, limit := routingPageBounds(req.GetPage())
	if limit > RoutingRevisionHistory {
		limit = RoutingRevisionHistory
	}
	rows, total, err := repo.ListRevisions(ctx, modelID, offset, limit)
	if err != nil {
		return nil, err
	}
	revisions := make([]*inferv1.RoutingPolicyRevision, 0, len(rows))
	for i := range rows {
		row := rows[i]
		revisions = append(revisions, &inferv1.RoutingPolicyRevision{
			Revision:  row.Revision,
			Actor:     row.Actor,
			UpdatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
			Summary:   row.Summary,
			Before:    routingRevisionSnapshotToProto(row.BeforeJSON),
			After:     routingRevisionSnapshotToProto(row.AfterJSON),
		})
	}
	return &inferv1.ListRoutingPolicyRevisionsResponse{
		Response:  okResponse(),
		Revisions: revisions,
		PageMeta:  &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// routingRevisionSnapshotToProto decodes a stored snapshot JSON into
// the proto policy projection.
func routingRevisionSnapshotToProto(raw string) *inferv1.RoutingPolicy {
	var snap struct {
		ModelID      string   `json:"model_id"`
		ModelVersion string   `json:"model_version"`
		Enabled      bool     `json:"enabled"`
		ServiceIDs   []string `json:"service_ids"`
		MaxAttempts  int      `json:"max_attempts"`
		RetryOn      []string `json:"retry_on"`
		Revision     int64    `json:"revision"`
	}
	_ = json.Unmarshal([]byte(raw), &snap)
	categories := make([]inferv1.RetryCategory, 0, len(snap.RetryOn))
	for _, v := range snap.RetryOn {
		categories = append(categories, stringToRetryCategory(v))
	}
	if snap.ServiceIDs == nil {
		snap.ServiceIDs = []string{}
	}
	return &inferv1.RoutingPolicy{
		ModelId:      snap.ModelID,
		ModelVersion: snap.ModelVersion,
		Enabled:      snap.Enabled,
		ServiceIds:   snap.ServiceIDs,
		MaxAttempts:  clampToInt32(snap.MaxAttempts),
		RetryOn:      categories,
		Revision:     snap.Revision,
	}
}
