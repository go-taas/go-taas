package infer

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// AutoscalingPolicyRepository persists the singleton global-default
// autoscaling policy (feature #16, §4.2).
type AutoscalingPolicyRepository struct {
	*database.BaseRepository[AutoscalingPolicy]
	db *database.Manager
}

// NewAutoscalingPolicyRepository constructs an AutoscalingPolicyRepository
// bound to a database Manager.
func NewAutoscalingPolicyRepository(db *gorm.DB) *AutoscalingPolicyRepository {
	mgr := database.NewManager(db)
	return &AutoscalingPolicyRepository{
		BaseRepository: database.NewBaseRepository[AutoscalingPolicy](mgr),
		db:             mgr,
	}
}

// GetDefault reads the singleton global-default policy. A miss maps to
// CodeInternal: the row is seeded at migration time (§4.3), so its
// absence is an infrastructure invariant violation.
func (r *AutoscalingPolicyRepository) GetDefault(ctx context.Context) (*AutoscalingPolicy, error) {
	var row AutoscalingPolicy
	err := r.DB(ctx).Where("id = ?", singletonPolicyID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.Newf(apierrors.CodeInternal, "infer: global autoscaling policy not seeded")
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpsertDefault saves the singleton global-default policy (id = 1).
func (r *AutoscalingPolicyRepository) UpsertDefault(ctx context.Context, p *AutoscalingPolicy) error {
	p.ID = singletonPolicyID
	p.UpdatedAt = time.Now().UTC()
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		var existing AutoscalingPolicy
		err := r.DB(ctx).Where("id = ?", singletonPolicyID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return r.Create(ctx, p)
		}
		if err != nil {
			return err
		}
		return r.DB(ctx).Model(&AutoscalingPolicy{}).
			Where("id = ?", singletonPolicyID).
			Updates(map[string]any{
				"enabled":            p.Enabled,
				"min_replicas":       p.MinReplicas,
				"max_replicas":       p.MaxReplicas,
				"target_concurrency": p.TargetConcurrency,
				"scale_to_zero":      p.ScaleToZero,
				"cooldown_seconds":   p.CooldownSeconds,
				"updated_at":         p.UpdatedAt,
			}).Error
	})
}

// SeedDefault inserts the shipped defaults when the singleton row is
// absent. It is a no-op on an existing install (§4.3).
func (r *AutoscalingPolicyRepository) SeedDefault(ctx context.Context) error {
	var count int64
	if err := r.DB(ctx).Model(&AutoscalingPolicy{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return r.Create(ctx, defaultAutoscalingPolicy())
}

// UpdateAutoscaling applies a per-service autoscaling policy, scoped by
// id AND organization_id. A miss maps to CodeInferServiceNotFound (no
// cross-org existence leak).
func (r *InferenceServiceRepository) UpdateAutoscaling(ctx context.Context, orgID, serviceID string, policy datatypes.JSON) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		res := r.DB(ctx).
			Model(&InferenceService{}).
			Where("id = ? AND organization_id = ?", serviceID, orgID).
			Updates(map[string]any{
				"autoscaling": string(policy),
				"updated_at":  time.Now().UTC(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return apierrors.New(apierrors.CodeInferServiceNotFound)
		}
		return nil
	})
}

// ResolveEffectivePolicy returns the effective autoscaling policy for a
// service: the stored policy when present, otherwise the global default
// (feature #16, §4.1). Empty {} means inherit.
func (r *InferenceServiceRepository) ResolveEffectivePolicy(ctx context.Context, svc *InferenceService) (*AutoscalingPolicy, error) {
	if len(svc.Autoscaling) > 0 && string(svc.Autoscaling) != "{}" && string(svc.Autoscaling) != "null" {
		var p AutoscalingPolicy
		if err := json.Unmarshal(svc.Autoscaling, &p); err != nil {
			return nil, apierrors.Newf(apierrors.CodeInternal, "infer: malformed stored autoscaling policy")
		}
		return &p, nil
	}
	policyRepo := NewAutoscalingPolicyRepository(r.db.DB(context.Background()))
	return policyRepo.GetDefault(ctx)
}

// policyToJSON marshals an AutoscalingPolicy to its jsonb representation.
func policyToJSON(p *AutoscalingPolicy) (datatypes.JSON, error) {
	if p == nil {
		return datatypes.JSON("{}"), nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(raw), nil
}
