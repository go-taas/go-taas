package vgpu

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// PoolListFilter narrows ListVGPUPools (feature #52, FR1.1).
type PoolListFilter struct {
	Name            string // name substring search, case-insensitive
	PlacementPolicy string // "" | binpack | spread | mutex
	Offset          int
	Limit           int
}

// Repository persists vGPU slicing pools on top of the generic
// repository base. All reads and writes join an open transaction via
// the context.
type Repository struct {
	*database.BaseRepository[VGPUPool]
	db *database.Manager
}

// NewRepository constructs a Repository bound to a database Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[VGPUPool](mgr),
		db:             mgr,
	}
}

// ListVGPUPools returns the pool rows matching the filter, ordered by
// name, paginated with a total count (feature #52, FR1.1).
func (r *Repository) ListVGPUPools(ctx context.Context, filter PoolListFilter) ([]*VGPUPool, int64, error) {
	query := r.db.DB(ctx).Model(&VGPUPool{})
	if filter.Name != "" {
		like := "%" + strings.ToLower(filter.Name) + "%"
		query = query.Where("LOWER(name) LIKE ?", like)
	}
	if filter.PlacementPolicy != "" {
		query = query.Where("placement_policy = ?", filter.PlacementPolicy)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*VGPUPool
	if err := query.Order("name ASC").Offset(filter.Offset).Limit(filter.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// GetVGPUPool returns one pool row. gorm.ErrRecordNotFound passes
// through; the caller maps it to CodeVGPUPoolNotFound.
func (r *Repository) GetVGPUPool(ctx context.Context, poolID string) (*VGPUPool, error) {
	var row VGPUPool
	err := r.db.DB(ctx).Where("id = ?", poolID).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateVGPUPool inserts a new pool row. A unique violation on name maps
// to CodeVGPUPoolInvalid.
func (r *Repository) CreateVGPUPool(ctx context.Context, pool *VGPUPool) error {
	if err := r.Create(ctx, pool); err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeVGPUPoolInvalid)
		}
		return err
	}
	return nil
}

// UpdateVGPUPool writes the changed pool columns (feature #52, FR1.4).
// patch carries only the columns to change.
func (r *Repository) UpdateVGPUPool(ctx context.Context, poolID string, patch map[string]any, now time.Time) error {
	patch["updated_at"] = now
	return r.db.DB(ctx).Model(&VGPUPool{}).Where("id = ?", poolID).Updates(patch).Error
}

// DeleteVGPUPool deletes a pool row.
func (r *Repository) DeleteVGPUPool(ctx context.Context, poolID string) error {
	return r.db.DB(ctx).Delete(&VGPUPool{}, "id = ?", poolID).Error
}

// CountSliceServices counts the services using the pool's slices
// (feature #52, FR1.5). Terminated services do not block deletion.
func (r *Repository) CountSliceServices(ctx context.Context, poolID string) (int64, error) {
	var count int64
	err := r.db.DB(ctx).Table("inference_services").
		Where("pool_id = ? AND state <> ?", poolID, "terminated").
		Count(&count).Error
	return count, err
}

// SliceService is one service using a slice of the pool (feature #52,
// FR3.3).
type SliceService struct {
	ServiceID   string
	Name        string
	TemplateName string
	NodeID      string
	GPUIndex    int64
	MemoryGiB   int64
	ComputePct  int64
	State       string
}

// ListSliceServices returns the services using the pool's slices
// (feature #52, FR3.3).
func (r *Repository) ListSliceServices(ctx context.Context, poolID string) ([]SliceService, error) {
	var rows []SliceService
	err := r.db.DB(ctx).Table("inference_services").
		Select("id AS service_id, name, COALESCE(slice_template_id, '') AS template_name, COALESCE(slice_node_id, '') AS node_id, COALESCE(slice_gpu_index, 0) AS gpu_index, COALESCE(slice_memory_gib, 0) AS memory_gib, COALESCE(slice_compute_pct, 0) AS compute_pct, state").
		Where("pool_id = ? AND state <> ?", poolID, "terminated").
		Order("name ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ListGrantedPools returns the pools the org may use (feature #52,
// AD10). The grant check is delegated to the grantChecker seam; when
// the seam is nil, every pool is granted (default-allow, matching the
// model-authorization default-allow rule).
func (r *Repository) ListGrantedPools(ctx context.Context, orgID string, grantChecker GrantChecker) ([]*VGPUPool, error) {
	var rows []*VGPUPool
	if err := r.db.DB(ctx).Model(&VGPUPool{}).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if grantChecker == nil {
		return rows, nil
	}
	out := make([]*VGPUPool, 0, len(rows))
	for _, p := range rows {
		granted, err := grantChecker.IsPoolGranted(ctx, orgID, p.ID)
		if err != nil {
			return nil, err
		}
		if granted {
			out = append(out, p)
		}
	}
	return out, nil
}

// isUniqueViolation reports whether err is a unique-constraint
// violation across the supported dialects (PostgreSQL 23505, SQLite
// generic message).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}

// templatesFromJSON decodes the stored templates JSON into a slice.
func templatesFromJSON(raw []byte) ([]SliceTemplate, error) {
	if len(raw) == 0 {
		return []SliceTemplate{}, nil
	}
	var out []SliceTemplate
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}