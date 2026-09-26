package infer

import (
	"context"

	"gorm.io/gorm"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

// ModelAutoscalingProvider implements the model module's
// AutoscalingProvider interface (feature #16, AD13): it resolves the
// read-only autoscaling summary for a model's ready inference service.
type ModelAutoscalingProvider struct {
	repo *InferenceServiceRepository
}

// NewModelAutoscalingProvider builds the model-module autoscaling
// projection provider. It is injected into the model service at wiring
// time (apps/taas-server), keeping the model module free of an infer
// dependency.
func NewModelAutoscalingProvider(db *gorm.DB) *ModelAutoscalingProvider {
	return &ModelAutoscalingProvider{repo: NewInferenceServiceRepository(db)}
}

// ModelAutoscaling implements model.AutoscalingProvider.
func (p *ModelAutoscalingProvider) ModelAutoscaling(ctx context.Context, orgID, modelID string) (*modelv1.ModelAutoscaling, error) {
	svc, err := p.repo.FindReadyByModelAndOrganization(ctx, orgID, modelID)
	if err != nil {
		return nil, err
	}
	if svc == nil {
		return nil, nil
	}
	effective, err := p.repo.ResolveEffectivePolicy(ctx, svc)
	if err != nil {
		return nil, err
	}
	autoscaled := effective != nil && effective.Enabled
	projection := &modelv1.ModelAutoscaling{
		Autoscaled:      autoscaled,
		CurrentReplicas: clampToInt32(svc.AutoscalingCurrentReplicas),
		MinReplicas:     int32(0),
		MaxReplicas:     int32(0),
		ScaleToZero:     effective != nil && effective.ScaleToZero,
		State:           userAutoscalingState(svc, autoscaled),
	}
	if effective != nil {
		projection.MinReplicas = clampToInt32(effective.MinReplicas)
		projection.MaxReplicas = clampToInt32(effective.MaxReplicas)
	}
	return projection, nil
}

// userAutoscalingState maps the service's autoscaling state to the
// masked user-realm state string (feature #16, AD13): steady | scaling |
// scaled-to-zero | warming-up | fixed.
func userAutoscalingState(svc *InferenceService, autoscaled bool) string {
	if !autoscaled {
		return "fixed"
	}
	switch svc.AutoscalingState {
	case "scaled-to-zero":
		return "scaled-to-zero"
	case "cold-starting":
		return "warming-up"
	case "scaling-up", "scaling-down":
		return "scaling"
	case "error":
		return "scaling"
	default:
		return "steady"
	}
}
