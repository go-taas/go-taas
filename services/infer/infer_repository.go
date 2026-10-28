package infer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
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

// CountActiveServicesByOrg counts the org's non-terminated inference
// services (feature #51, FR3.1). It is the authoritative enforcement
// count for the service-count quota check.
func (r *InferenceServiceRepository) CountActiveServicesByOrg(ctx context.Context, orgID string) (int64, error) {
	var count int64
	err := r.db.DB(ctx).Model(&InferenceService{}).
		Where("organization_id = ? AND state != ?", orgID, StateTerminated).
		Count(&count).Error
	return count, err
}

// BackfillClusterPlacement assigns cluster_id to services that predate
// multi-cluster placement (feature #40, AD8): a service with an empty
// cluster_id is placed on the given default cluster. It returns the
// number of services updated. It is a one-time startup migration so
// existing single-cluster deployments keep their placement view.
func (r *InferenceServiceRepository) BackfillClusterPlacement(ctx context.Context, clusterID string) (int64, error) {
	if clusterID == "" {
		return 0, nil
	}
	res := r.db.DB(ctx).Model(&InferenceService{}).
		Where("cluster_id = ?", "").
		Update("cluster_id", clusterID)
	return res.RowsAffected, res.Error
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
// CodeInferServiceNotFound — no cross-org existence leak. A malformed
// (non-UUID) id maps there too, because it can never match a stored row
// and a raw comparison would surface a postgres uuid cast error.
func (r *InferenceServiceRepository) FindByIDAndOrganization(ctx context.Context, orgID, serviceID string) (*InferenceService, error) {
	if _, err := uuid.Parse(serviceID); err != nil {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
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

// FindByID returns one service by id across organizations (platform
// scope, feature #20 AD1: load testing is an operator activity that
// spans orgs). A miss maps to CodeInferServiceNotFound; a malformed
// (non-UUID) id maps there too, because it can never match a stored row
// and a raw comparison would surface a uuid cast error.
func (r *InferenceServiceRepository) FindByID(ctx context.Context, serviceID string) (*InferenceService, error) {
	if _, err := uuid.Parse(serviceID); err != nil {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	var row InferenceService
	err := r.DB(ctx).Where("id = ?", serviceID).First(&row).Error
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

// ListAll returns one page of every organization's services ordered by
// updated_at DESC (newest first) and the total count: the platform-admin
// view of the operator surface, which is not organization-isolated. An
// empty orgFilter lists every organization.
func (r *InferenceServiceRepository) ListAll(ctx context.Context, orgFilter string, offset, limit int, includeTerminated bool) ([]*InferenceService, int64, error) {
	var conds []any
	switch {
	case orgFilter != "" && !includeTerminated:
		conds = []any{"organization_id = ? AND state != ?", orgFilter, StateTerminated}
	case orgFilter != "":
		conds = []any{"organization_id = ?", orgFilter}
	case !includeTerminated:
		conds = []any{"state != ?", StateTerminated}
	default:
		conds = nil
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

// UpdateTopology applies an in-place serving-layout change and returns the
// service to pending so the controller rolls the new storm out. The
// service_id and endpoints are untouched; replicas is only written when
// non-zero. A miss maps to CodeInferServiceNotFound.
func (r *InferenceServiceRepository) UpdateTopology(
	ctx context.Context, orgID, serviceID string, spec TopologySpec, replicas int,
	roleArgs datatypes.JSON, scheduling *PodScheduling,
) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		updates := map[string]any{
			"topology":               spec.Topology,
			"tensor_parallel_size":   spec.TensorParallelSize,
			"pipeline_parallel_size": spec.PipelineParallelSize,
			"prefill_replicas":       spec.PrefillReplicas,
			"decode_replicas":        spec.DecodeReplicas,
			"state":                  StatePending,
		}
		if replicas > 0 {
			updates["replicas"] = replicas
		}
		// An empty override list keeps the stored per-role arguments: a
		// layout change alone must not silently discard them.
		if len(roleArgs) > 0 {
			updates["role_engine_args"] = roleArgs
		}
		// A nil shape keeps the stored one: a layout change that says
		// nothing about resources must not resize the pods.
		if scheduling != nil {
			updates["gpu_per_pod"] = scheduling.GPUPerPod
			updates["cpu_milli"] = scheduling.CPUMilli
			updates["memory_mib"] = scheduling.MemoryMiB
			updates["priority"] = scheduling.Priority
		}
		res := r.DB(ctx).
			Model(&InferenceService{}).
			Where("id = ? AND organization_id = ?", serviceID, orgID).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return apierrors.New(apierrors.CodeInferServiceNotFound)
		}
		return nil
	})
}

// UpdateModelVersion applies an in-place version change: it sets the new
// model_version and transitions the service to deploying (feature #32,
// AD6). The service_id and endpoints are untouched. A miss maps to
// CodeInferServiceNotFound.
func (r *InferenceServiceRepository) UpdateModelVersion(ctx context.Context, orgID, serviceID, modelVersion string) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		res := r.DB(ctx).
			Model(&InferenceService{}).
			Where("id = ? AND organization_id = ?", serviceID, orgID).
			Updates(map[string]any{
				"model_version": modelVersion,
				"state":         StateDeploying,
				"updated_at":    time.Now().UTC(),
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
func (r *InferenceServiceRepository) ApplyStatus(ctx context.Context, serviceID, state string, endpoints []string, failureReason *string, autoscaling *autoscalingStatusReport, assignedSlice *assignedSliceReport) error {
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
	// Feature #52 (AD14): the assigned-slice report. When present, the
	// physical GPU the service's slices landed on is stored on the row.
	if assignedSlice != nil {
		fields["slice_node_id"] = assignedSlice.NodeID
		fields["slice_gpu_index"] = assignedSlice.GPUIndex
		fields["slice_memory_gib"] = assignedSlice.MemoryGiB
		fields["slice_compute_pct"] = assignedSlice.ComputePct
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

// FindRunningServiceByModel returns the newest running inference service
// serving a model, across organizations.
//
// It is platform-wide on purpose: services are platform assets, and a
// voucher produced by the inference gateway is attributed to whichever
// service is actually serving the model, not to a tenant's copy of it.
func (r *InferenceServiceRepository) FindRunningServiceByModel(ctx context.Context, modelID string) (*InferenceService, error) {
	var row InferenceService
	err := r.DB(ctx).
		Where("model_id = ? AND state = ?", modelID, StateRunning).
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

// ListAccess returns a service's access-policy grants, ordered so the
// console renders a stable list.
func (r *InferenceServiceRepository) ListAccess(ctx context.Context, serviceID string) ([]*InferenceServiceAccess, error) {
	var rows []*InferenceServiceAccess
	if err := r.DB(ctx).
		Where("service_id = ?", serviceID).
		Order("subject_type ASC, subject_id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ReplaceAccess swaps a service's whole access policy in one transaction.
// An empty list means "everyone", so the delete is the whole write: a
// half-applied policy would silently widen or narrow access.
func (r *InferenceServiceRepository) ReplaceAccess(ctx context.Context, serviceID, actor string, subjects []*InferenceServiceAccess) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		if err := r.DB(ctx).Where("service_id = ?", serviceID).
			Delete(&InferenceServiceAccess{}).Error; err != nil {
			return err
		}
		if len(subjects) == 0 {
			return nil
		}
		now := time.Now().UTC()
		rows := make([]*InferenceServiceAccess, 0, len(subjects))
		for _, subject := range subjects {
			rows = append(rows, &InferenceServiceAccess{
				ID:          uuid.NewString(),
				ServiceID:   serviceID,
				SubjectType: subject.SubjectType,
				SubjectID:   subject.SubjectID,
				CreatedBy:   actor,
				CreatedAt:   now,
			})
		}
		return r.DB(ctx).Create(&rows).Error
	})
}

// RestrictedServiceIDs returns the subset of service ids that carry at
// least one access-policy grant. A service with no rows is unrestricted,
// which is the default.
func (r *InferenceServiceRepository) RestrictedServiceIDs(ctx context.Context, serviceIDs []string) ([]string, error) {
	if len(serviceIDs) == 0 {
		return nil, nil
	}
	var ids []string
	if err := r.DB(ctx).Model(&InferenceServiceAccess{}).
		Where("service_id IN ?", serviceIDs).
		Distinct("service_id").
		Pluck("service_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// NameTaken reports whether any service already uses this name. Service
// names are platform-unique because a service is a platform asset: two
// rows with the same name would render the same Kubernetes Deployment and
// fight over it.
func (r *InferenceServiceRepository) NameTaken(ctx context.Context, name string) (bool, error) {
	var count int64
	if err := r.DB(ctx).Model(&InferenceService{}).
		Where("name = ?", name).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
