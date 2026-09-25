package infer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"
)

// InferenceServiceRepository persists inference services on top of the
// generic repository base. All reads and writes join an open transaction
// via the context.
type InferenceServiceRepository struct {
	*database.BaseRepository[InferenceService]
	db *database.Manager
}

// NewInferenceServiceRepository constructs an InferenceServiceRepository
// bound to a database Manager.
func NewInferenceServiceRepository(db *gorm.DB) *InferenceServiceRepository {
	mgr := database.NewManager(db)
	return &InferenceServiceRepository{
		BaseRepository: database.NewBaseRepository[InferenceService](mgr),
		db:             mgr,
	}
}

// Create inserts a new inference service row. A unique violation on
// (organization_id, name) maps to CodeInferServiceExists.
func (r *InferenceServiceRepository) Create(ctx context.Context, svc *InferenceService) error {
	if err := r.BaseRepository.Create(ctx, svc); err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeInferServiceExists)
		}
		return err
	}
	return nil
}

// AcceleratorTypesByServiceIDs returns the accelerator type of the
// given inference service ids. Missing ids are simply absent from the
// result (terminated services resolve to the "default" sentinel
// downstream). Read-only helper consumed by billing's usage-line
// ingestion (feature #5, architecture Section 3.4).
func (r *InferenceServiceRepository) AcceleratorTypesByServiceIDs(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	var rows []struct {
		ID              string
		AcceleratorType string
	}
	err := r.DB(ctx).
		Model(&InferenceService{}).
		Select("id, accelerator_type").
		Where("id IN ?", ids).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.AcceleratorType
	}
	return out, nil
}

// FindByIDAndOrganization returns the service owned by orgID. A miss
// (including a service of another organization) maps to
// CodeInferServiceNotFound — no cross-org existence leak.
func (r *InferenceServiceRepository) FindByIDAndOrganization(ctx context.Context, orgID, serviceID string) (*InferenceService, error) {
	var row InferenceService
	err := r.DB(ctx).
		Where("id = ? AND organization_id = ?", serviceID, orgID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListByOrganization returns one page of the organization's services
// ordered by updated_at DESC (newest first) and the total count. The
// default view excludes terminated services (AC9).
func (r *InferenceServiceRepository) ListByOrganization(ctx context.Context, orgID string, offset, limit int, includeTerminated bool) ([]*InferenceService, int64, error) {
	conds := []any{"organization_id = ?", orgID}
	if !includeTerminated {
		conds = []any{"organization_id = ? AND state != ?", orgID, StateTerminated}
	}
	return r.Paginate(ctx, offset, limit, conds, "updated_at DESC", "id DESC")
}

// UpdateReplicas applies a spec-only replica update (state untouched).
func (r *InferenceServiceRepository) UpdateReplicas(ctx context.Context, orgID, serviceID string, replicas int) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		res := r.DB(ctx).
			Model(&InferenceService{}).
			Where("id = ? AND organization_id = ?", serviceID, orgID).
			Update("replicas", replicas)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return apierrors.New(apierrors.CodeInferServiceNotFound)
		}
		return nil
	})
}

// MarkTerminated sets state=terminated. It is idempotent: an
// already-terminated service is a no-op success (AC9).
func (r *InferenceServiceRepository) MarkTerminated(ctx context.Context, orgID, serviceID string) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		res := r.DB(ctx).
			Model(&InferenceService{}).
			Where("id = ? AND organization_id = ?", serviceID, orgID).
			Updates(map[string]any{
				"state":      StateTerminated,
				"updated_at": time.Now().UTC(),
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

// ApplyStatus applies a controller status report: state, endpoints,
// failure_reason and the autoscaling status block. Last-write-wins is
// acceptable (the controller is the only publisher). failure_reason is
// cleared on any non-failed state.
func (r *InferenceServiceRepository) ApplyStatus(ctx context.Context, serviceID, state string, endpoints []string, failureReason *string, autoscaling *autoscalingStatusReport) error {
	if endpoints == nil {
		endpoints = []string{}
	}
	// Map-based Updates bypasses the field serializer, so the endpoints
	// slice is encoded to JSON explicitly.
	endpointsJSON, err := json.Marshal(endpoints)
	if err != nil {
		return err
	}
	fields := map[string]any{
		"state":      state,
		"endpoints":  string(endpointsJSON),
		"updated_at": time.Now().UTC(),
	}
	if state == StateFailed && failureReason != nil {
		fields["failure_reason"] = *failureReason
	} else if state != StateFailed {
		fields["failure_reason"] = nil
	}
	if autoscaling != nil {
		fields["autoscaling_state"] = autoscaling.State
		fields["autoscaling_current_replicas"] = autoscaling.CurrentReplicas
		fields["autoscaling_desired_replicas"] = autoscaling.DesiredReplicas
		fields["autoscaling_current_concurrency"] = autoscaling.CurrentConcurrency
		fields["autoscaling_target_concurrency"] = autoscaling.TargetConcurrency
		fields["autoscaling_error_reason"] = autoscaling.ErrorReason
		if ts, err := time.Parse(time.RFC3339, autoscaling.LastScalingEventAt); err == nil {
			fields["autoscaling_last_scaling_event_at"] = ts
		}
	}
	res := r.DB(ctx).
		Model(&InferenceService{}).
		Where("id = ?", serviceID).
		Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Unknown service id: a deleted service's late report. The
		// caller decides whether to log-and-skip; the repository
		// surfaces it as not-found.
		return apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	return nil
}

// ApplyConcurrency updates the service's current-concurrency projection
// from a gateway concurrency report (feature #16, §6.3). Unknown service
// ids map to CodeInferServiceNotFound.
func (r *InferenceServiceRepository) ApplyConcurrency(ctx context.Context, serviceID string, inFlight int) error {
	res := r.DB(ctx).
		Model(&InferenceService{}).
		Where("id = ?", serviceID).
		Update("autoscaling_current_concurrency", inFlight)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	return nil
}

// FindReadyByModelAndOrganization returns the newest running inference
// service for a model in an organization, or nil when none exists
// (feature #16, AD13). Used by the model module's autoscaling projection.
func (r *InferenceServiceRepository) FindReadyByModelAndOrganization(ctx context.Context, orgID, modelID string) (*InferenceService, error) {
	var row InferenceService
	err := r.DB(ctx).
		Where("organization_id = ? AND model_id = ? AND state = ?", orgID, modelID, StateRunning).
		Order("updated_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CountByModelID counts the inference services referencing the model.
// With excludeTerminated, terminated services do not block a model
// delete (AC3).
func (r *InferenceServiceRepository) CountByModelID(ctx context.Context, modelID string, excludeTerminated bool) (int64, error) {
	conds := []any{"model_id = ?", modelID}
	if excludeTerminated {
		conds = []any{"model_id = ? AND state != ?", modelID, StateTerminated}
	}
	return r.Count(ctx, conds...)
}

// FindBlockingServiceByModel returns the first non-terminated service
// referencing the model, for the delete-model error detail (AC3). It
// returns nil when none exists.
func (r *InferenceServiceRepository) FindBlockingServiceByModel(ctx context.Context, modelID string) (*InferenceService, error) {
	var row InferenceService
	err := r.DB(ctx).
		Where("model_id = ? AND state != ?", modelID, StateTerminated).
		Order("updated_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// NewServiceID returns a fresh UUID v4 for a new inference service.
func NewServiceID() string { return uuid.NewString() }

// NewDeleteModelGuard builds the model-module delete guard (AC3): it
// blocks deleting a model while a non-terminated inference service
// references it, returning 10101 with a detail naming the blocking
// service. The guard is injected into the model service at wiring time
// (apps/taas-server), keeping the model module free of an infer
// dependency.
func NewDeleteModelGuard(db *gorm.DB) func(ctx context.Context, modelID string) error {
	repo := NewInferenceServiceRepository(db)
	return func(ctx context.Context, modelID string) error {
		count, err := repo.CountByModelID(ctx, modelID, true)
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		blocking, err := repo.FindBlockingServiceByModel(ctx, modelID)
		if err != nil {
			return err
		}
		detail := "referenced by inference service"
		if blocking != nil {
			detail = fmt.Sprintf("referenced by inference service %s", blocking.Name)
		}
		return apierrors.Newf(apierrors.CodeModelNotFound, "%s", detail)
	}
}

// CountByImageID counts the inference services referencing the image.
// With excludeTerminated, terminated services do not block an image
// delete (feature #3 D7).
func (r *InferenceServiceRepository) CountByImageID(ctx context.Context, imageID string, excludeTerminated bool) (int64, error) {
	conds := []any{"image_id = ?", imageID}
	if excludeTerminated {
		conds = []any{"image_id = ? AND state != ?", imageID, StateTerminated}
	}
	return r.Count(ctx, conds...)
}

// ListActiveByImageID returns the non-terminated inference services
// referencing the image, newest update first (feature #3 GetImage
// in_use_services and the list in_use_count).
func (r *InferenceServiceRepository) ListActiveByImageID(ctx context.Context, imageID string) ([]*InferenceService, error) {
	var rows []*InferenceService
	err := r.DB(ctx).
		Where("image_id = ? AND state != ?", imageID, StateTerminated).
		Order("updated_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// NewDeleteImageGuard builds the image-module delete guard (feature #3
// D7): it blocks deleting an image while a non-terminated inference
// service references it, returning 10206 with a detail naming the
// blocking service. The guard is injected into the image service at
// wiring time (apps/taas-server), keeping the image module free of an
// infer dependency.
func NewDeleteImageGuard(db *gorm.DB) func(ctx context.Context, imageID string) error {
	repo := NewInferenceServiceRepository(db)
	return func(ctx context.Context, imageID string) error {
		count, err := repo.CountByImageID(ctx, imageID, true)
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		blocking, err := repo.FindBlockingServiceByImage(ctx, imageID)
		if err != nil {
			return err
		}
		detail := "referenced by inference service"
		if blocking != nil {
			detail = fmt.Sprintf("referenced by inference service %s", blocking.Name)
		}
		return apierrors.Newf(apierrors.CodeImageInUse, "%s", detail)
	}
}

// FindBlockingServiceByImage returns the first non-terminated service
// referencing the image, for the delete-image error detail (feature #3
// D7). It returns nil when none exists.
func (r *InferenceServiceRepository) FindBlockingServiceByImage(ctx context.Context, imageID string) (*InferenceService, error) {
	var row InferenceService
	err := r.DB(ctx).
		Where("image_id = ? AND state != ?", imageID, StateTerminated).
		Order("updated_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// NewImageInUseProvider builds the image-module in-use provider (feature
// #3 GetImage in_use_services and the list in_use_count): it lists the
// non-terminated inference services referencing the image. The provider
// is injected into the image service at wiring time (apps/taas-server),
// keeping the image module free of an infer dependency.
func NewImageInUseProvider(db *gorm.DB) func(ctx context.Context, imageID string) ([]*imagev1.InUseService, error) {
	repo := NewInferenceServiceRepository(db)
	return func(ctx context.Context, imageID string) ([]*imagev1.InUseService, error) {
		rows, err := repo.ListActiveByImageID(ctx, imageID)
		if err != nil {
			return nil, err
		}
		out := make([]*imagev1.InUseService, 0, len(rows))
		for _, row := range rows {
			out = append(out, &imagev1.InUseService{
				ServiceId: row.ID,
				Name:      row.Name,
				State:     row.State,
			})
		}
		return out, nil
	}
}

// isUniqueViolation reports whether err is a unique-constraint violation
// across the supported dialects (PostgreSQL 23505, SQLite generic message).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}
