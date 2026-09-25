package infer

import (
	"context"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/image"
)

// Autoscaling policy bounds (feature #16, §5.3).
const (
	autoscalingMaxReplicas     = 100
	autoscalingMinTarget       = 1
	autoscalingMaxTarget       = 1000
	autoscalingMinCooldown     = 0
	autoscalingMaxCooldown     = 3600
	autoscalingDefaultTarget   = 32
	autoscalingDefaultCooldown = 300
)

// validateAutoscalingPolicy runs the synchronous validation matrix
// (feature #16, §5.3) in order. The first failure returns immediately;
// nothing is published (AD10).
func validateAutoscalingPolicy(p *inferv1.AutoscalingPolicy) error {
	if p == nil {
		return nil
	}
	min := int(p.GetMinReplicas())
	max := int(p.GetMaxReplicas())
	target := int(p.GetTargetConcurrency())
	cooldown := int(p.GetCooldownSeconds())
	scaleToZero := p.GetScaleToZero()

	if min > max {
		return apierrors.Newf(apierrors.CodeAutoscalingPolicyInvalid,
			"autoscaling policy invalid: min replicas must be ≤ max replicas")
	}
	if min == 0 && !scaleToZero {
		return apierrors.Newf(apierrors.CodeAutoscalingPolicyInvalid,
			"autoscaling policy invalid: min replicas 0 requires scale-to-zero")
	}
	if scaleToZero && min != 0 {
		return apierrors.Newf(apierrors.CodeAutoscalingPolicyInvalid,
			"autoscaling policy invalid: scale-to-zero requires min replicas 0")
	}
	if target < autoscalingMinTarget || target > autoscalingMaxTarget {
		return apierrors.Newf(apierrors.CodeAutoscalingPolicyInvalid,
			"autoscaling policy invalid: target concurrency must be between 1 and 1000")
	}
	if cooldown < autoscalingMinCooldown || cooldown > autoscalingMaxCooldown {
		return apierrors.Newf(apierrors.CodeAutoscalingPolicyInvalid,
			"autoscaling policy invalid: cooldown must be between 0 and 3600 seconds")
	}
	if max < 1 || max > autoscalingMaxReplicas {
		return apierrors.Newf(apierrors.CodeAutoscalingPolicyInvalid,
			"autoscaling policy invalid: max replicas must be between 1 and 100")
	}
	return nil
}

// policyFromProto converts a proto policy to the GORM model.
func policyFromProto(p *inferv1.AutoscalingPolicy) *AutoscalingPolicy {
	if p == nil {
		return nil
	}
	return &AutoscalingPolicy{
		Enabled:           p.GetEnabled(),
		MinReplicas:       int(p.GetMinReplicas()),
		MaxReplicas:       int(p.GetMaxReplicas()),
		TargetConcurrency: int(p.GetTargetConcurrency()),
		ScaleToZero:       p.GetScaleToZero(),
		CooldownSeconds:   int(p.GetCooldownSeconds()),
	}
}

// policyToProto converts a GORM model to the proto policy.
func policyToProto(p *AutoscalingPolicy) *inferv1.AutoscalingPolicy {
	if p == nil {
		return nil
	}
	return &inferv1.AutoscalingPolicy{
		Enabled:           p.Enabled,
		MinReplicas:       clampToInt32(p.MinReplicas),
		MaxReplicas:       clampToInt32(p.MaxReplicas),
		TargetConcurrency: clampToInt32(p.TargetConcurrency),
		ScaleToZero:       p.ScaleToZero,
		CooldownSeconds:   clampToInt32(p.CooldownSeconds),
	}
}

// autoscalingPolicyRepository lazily resolves the policy repository.
func (s *Service) autoscalingPolicyRepository() (*AutoscalingPolicyRepository, error) {
	db, err := s.gormDB()
	if err != nil {
		// Fall back to the wired repository's DB (tests, FVT).
		if s.repo != nil {
			return NewAutoscalingPolicyRepository(s.repo.db.DB(context.Background())), nil
		}
		return nil, err
	}
	return NewAutoscalingPolicyRepository(db), nil
}

// GetAutoscalingPolicy returns the global default autoscaling policy
// (feature #16, FR1.4).
func (s *Service) GetAutoscalingPolicy(ctx context.Context, _ *inferv1.GetAutoscalingPolicyRequest) (*inferv1.GetAutoscalingPolicyResponse, error) {
	repo, err := s.autoscalingPolicyRepository()
	if err != nil {
		return nil, err
	}
	policy, err := repo.GetDefault(ctx)
	if err != nil {
		return nil, err
	}
	return &inferv1.GetAutoscalingPolicyResponse{
		Response: okResponse(),
		Policy:   policyToProto(policy),
	}, nil
}

// UpdateAutoscalingPolicy saves the global default autoscaling policy.
// Synchronous (FR1.4); the policy is validated before any write (AD10).
func (s *Service) UpdateAutoscalingPolicy(ctx context.Context, req *inferv1.UpdateAutoscalingPolicyRequest) (*inferv1.UpdateAutoscalingPolicyResponse, error) {
	if err := validateAutoscalingPolicy(req.GetPolicy()); err != nil {
		return nil, err
	}
	repo, err := s.autoscalingPolicyRepository()
	if err != nil {
		return nil, err
	}
	policy := policyFromProto(req.GetPolicy())
	if policy == nil {
		policy = defaultAutoscalingPolicy()
	}
	if err := repo.UpsertDefault(ctx, policy); err != nil {
		return nil, err
	}
	// Feature #15: record the successful update best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  orgIDFromContext(ctx),
		ActorType:    "user",
		Action:       "autoscaling_policy.update",
		ResourceType: "autoscaling_policy",
		ResourceID:   "global-default",
		Result:       "success",
	})
	return &inferv1.UpdateAutoscalingPolicyResponse{
		Response: okResponse(),
		Policy:   policyToProto(policy),
	}, nil
}

// UpdateInferenceServiceAutoscaling edits a service's per-service
// autoscaling policy in place. Asynchronous via MQ (AD9): the update
// returns immediately and the service state reflects convergence. When
// disabling, the service returns to a fixed replica count equal to the
// current desired count (FR2.5).
func (s *Service) UpdateInferenceServiceAutoscaling(ctx context.Context, req *inferv1.UpdateInferenceServiceAutoscalingRequest) (*inferv1.UpdateInferenceServiceAutoscalingResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	if err := validateAutoscalingPolicy(req.GetPolicy()); err != nil {
		return nil, err
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	client, err := s.mqClientFor()
	if err != nil {
		return nil, err
	}

	row, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId())
	if err != nil {
		return nil, err
	}
	if row.State == StateTerminated {
		return nil, apierrors.Newf(apierrors.CodeInferServiceStateInvalid,
			"cannot autoscale a terminated service")
	}

	policy := policyFromProto(req.GetPolicy())
	if policy == nil {
		policy = defaultAutoscalingPolicy()
	}
	policyJSON, err := policyToJSON(policy)
	if err != nil {
		return nil, err
	}
	if err := repo.UpdateAutoscaling(ctx, orgID, req.GetServiceId(), policyJSON); err != nil {
		return nil, err
	}

	// Re-read the model and image to compose the resolved spec.
	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	modelVersion, err := modelRepo.FindVersion(ctx, row.ModelID, row.ModelVersion)
	if err != nil {
		return nil, err
	}
	img, err := image.Lookup(row.ImageID)
	if err != nil {
		return nil, err
	}

	// Resolve the effective policy for the change event: the stored
	// policy (never empty here) or the global default.
	updated := *row
	updated.Autoscaling = policyJSON
	effective, err := repo.ResolveEffectivePolicy(ctx, &updated)
	if err != nil {
		return nil, err
	}

	fixedReplicas := int32(0)
	if !policy.Enabled {
		// FR2.5: disabling returns the service to a fixed count equal to
		// the current desired count.
		fixedReplicas = clampToInt32(row.Replicas)
		updated.Replicas = row.Replicas
	}

	evt := buildChangeEvent(EventTypeUpsert, &updated, modelVersion.WeightPath, img.Reference(), img.Engine)
	evt.Autoscaling = effective
	if err := publishChange(ctx, client, evt); err != nil {
		logger.S().Errorw("infer: publish autoscaling change failed",
			"service_id", req.GetServiceId(), "err", err)
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: publish change failed")
	}

	// Feature #15: record the successful update best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "inference_service.autoscaling",
		ResourceType:   "inference_service",
		ResourceID:     req.GetServiceId(),
		Result:         "success",
	})

	return &inferv1.UpdateInferenceServiceAutoscalingResponse{
		Response:      okResponse(),
		FixedReplicas: fixedReplicas,
	}, nil
}

// orgIDFromContext reads the transitional caller organization from the
// context for audit attribution (best-effort; empty when absent).
func orgIDFromContext(ctx context.Context) string {
	org, err := resolveOrganizationID(ctx)
	if err != nil {
		return ""
	}
	return org
}