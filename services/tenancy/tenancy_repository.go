package tenancy

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Repository persists organizations and projects on top of the generic
// repository base. All reads and writes join an open transaction via
// the context.
type Repository struct {
	*database.BaseRepository[Organization]
	db *database.Manager
}

// NewRepository constructs a Repository bound to a database Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[Organization](mgr),
		db:             mgr,
	}
}

// OrgFilter narrows ListOrganizations.
type OrgFilter struct {
	State string // "" = all states
}

// ProjectFilter narrows ListProjects.
type ProjectFilter struct {
	OrganizationID string // "" = all organizations
	State          string // "" = all states
}

// CreateOrganization inserts a new organization row. A unique violation
// on the primary key maps to CodeOrganizationExists (the CreateImage
// error-mapping pattern).
func (r *Repository) CreateOrganization(ctx context.Context, org *Organization) error {
	if err := r.Create(ctx, org); err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeOrganizationExists)
		}
		return err
	}
	return nil
}

// FindOrganization returns the organization with the given id.
// gorm.ErrRecordNotFound passes through; the caller maps it to
// CodeOrganizationNotFound.
func (r *Repository) FindOrganization(ctx context.Context, id string) (*Organization, error) {
	var row Organization
	err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListOrganizations returns organizations newest first, paginated,
// with the total count before pagination.
func (r *Repository) ListOrganizations(ctx context.Context, filter OrgFilter, offset, limit int) ([]*Organization, int64, error) {
	query := r.db.DB(ctx).Model(&Organization{})
	if filter.State != "" {
		query = query.Where("state = ?", filter.State)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Organization
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// UpdateOrganization replaces the display name and description and
// bumps updated_at (GORM's auto-update). The row is re-selected
// afterwards.
func (r *Repository) UpdateOrganization(ctx context.Context, id, displayName, description string) (*Organization, error) {
	err := r.db.DB(ctx).Model(&Organization{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"display_name": displayName,
			"description":  description,
		}).Error
	if err != nil {
		return nil, err
	}
	return r.FindOrganization(ctx, id)
}

// SetOrganizationState moves an organization to the target state and
// bumps updated_at. It is idempotent: a row already in the target
// state just gets its updated_at bumped. The row is re-selected
// afterwards.
func (r *Repository) SetOrganizationState(ctx context.Context, id, state string) (*Organization, error) {
	err := r.db.DB(ctx).Model(&Organization{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"state": state,
		}).Error
	if err != nil {
		return nil, err
	}
	return r.FindOrganization(ctx, id)
}

// CreateProject inserts a new project row. A unique violation on the
// primary key maps to CodeProjectExists.
func (r *Repository) CreateProject(ctx context.Context, project *Project) error {
	err := r.db.DB(ctx).Create(project).Error
	if err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeProjectExists)
		}
		return err
	}
	return nil
}

// FindProject returns the project with the given id.
// gorm.ErrRecordNotFound passes through; the caller maps it to
// CodeProjectNotFound.
func (r *Repository) FindProject(ctx context.Context, id string) (*Project, error) {
	var row Project
	err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListProjects returns projects newest first, paginated, with the
// total count before pagination.
func (r *Repository) ListProjects(ctx context.Context, filter ProjectFilter, offset, limit int) ([]*Project, int64, error) {
	query := r.db.DB(ctx).Model(&Project{})
	if filter.OrganizationID != "" {
		query = query.Where("organization_id = ?", filter.OrganizationID)
	}
	if filter.State != "" {
		query = query.Where("state = ?", filter.State)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Project
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// UpdateProjectQuotas persists a project's quota columns.
func (r *Repository) UpdateProjectQuotas(ctx context.Context, p *Project) error {
	return r.db.DB(ctx).Model(&Project{}).Where("id = ?", p.ID).
		Updates(map[string]any{
			"max_concurrent_requests":      p.MaxConcurrentRequests,
			"max_inference_services":       p.MaxInferenceServices,
			"max_batch_jobs_per_day":       p.MaxBatchJobsPerDay,
			"max_output_tokens_per_request": p.MaxOutputTokensPerRequest,
			"quota_updated_by":             p.QuotaUpdatedBy,
			"quota_updated_at":             p.QuotaUpdatedAt,
		}).Error
}

// UpdateProject replaces the display name and description and bumps
// updated_at (GORM's auto-update). The row is re-selected afterwards.
func (r *Repository) UpdateProject(ctx context.Context, id, displayName, description string) (*Project, error) {
	err := r.db.DB(ctx).Model(&Project{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"display_name": displayName,
			"description":  description,
		}).Error
	if err != nil {
		return nil, err
	}
	return r.FindProject(ctx, id)
}

// SetProjectState moves a project to the target state and bumps
// updated_at. It is idempotent. The row is re-selected afterwards.
func (r *Repository) SetProjectState(ctx context.Context, id, state string) (*Project, error) {
	err := r.db.DB(ctx).Model(&Project{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"state": state,
		}).Error
	if err != nil {
		return nil, err
	}
	return r.FindProject(ctx, id)
}

// CountOrganizations returns the total number of organization rows. It
// gates the first-boot seed (the image CountAll pattern).
func (r *Repository) CountOrganizations(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.DB(ctx).Model(&Organization{}).Count(&count).Error
	return count, err
}

// SeedDefaultOrganization inserts the default organization when the
// table is empty. Insert-only: an existing row is left untouched (the
// seed never re-runs; deletions stick, edits win).
func (r *Repository) SeedDefaultOrganization(ctx context.Context, id, displayName string) error {
	org := &Organization{
		ID:          id,
		DisplayName: displayName,
		Description: "",
		State:       StateActive,
	}
	err := r.db.DB(ctx).Create(org).Error
	if err != nil {
		if isUniqueViolation(err) {
			// A concurrent seeder won the race; the row exists, which
			// is all the seed needs to guarantee.
			return nil
		}
		return err
	}
	return nil
}

// APIKeyCountByOrganization counts the organization's non-revoked API
// keys (D11). Plain table-name SQL over the auth module's existing
// table — read-only, no migration, no GORM model of a foreign table
// (the AcceleratorTypesByServiceIDs pattern).
func (r *Repository) APIKeyCountByOrganization(ctx context.Context, orgID string) (int64, error) {
	var count int64
	err := r.db.DB(ctx).
		Table("api_keys").
		Where("organization_id = ? AND revoked = ?", orgID, false).
		Count(&count).Error
	return count, err
}

// InferenceServiceCountByOrganization counts the organization's
// non-terminated inference services (D11).
func (r *Repository) InferenceServiceCountByOrganization(ctx context.Context, orgID string) (int64, error) {
	var count int64
	err := r.db.DB(ctx).
		Table("inference_services").
		Where("organization_id = ? AND state != ?", orgID, "terminated").
		Count(&count).Error
	return count, err
}

// ProjectCountByOrganization counts the organization's projects of any
// state (D11).
func (r *Repository) ProjectCountByOrganization(ctx context.Context, orgID string) (int64, error) {
	var count int64
	err := r.db.DB(ctx).
		Model(&Project{}).
		Where("organization_id = ?", orgID).
		Count(&count).Error
	return count, err
}

// isUniqueViolation reports whether err is a unique-constraint
// violation across the supported dialects (PostgreSQL 23505, SQLite
// generic message).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}

// orgScopedDelete is one table/condition pair removed when an
// organization is deleted. Keeping them as data (not a chain of ifs)
// makes the cascade easy to extend and to read.
type orgScopedDelete struct {
	model any
	where string
	args  []any
}

// DeleteOrganization removes an organization and every tenancy-side row
// that belongs to it: its projects and their rosters, its member roster,
// its invitations and its join requests. Platform assets (API keys,
// inference services) are keyed by the organization id and are not
// touched here; the service refuses a delete while any exist. The work
// runs in one transaction so a partial delete cannot leave orphans.
func (r *Repository) DeleteOrganization(ctx context.Context, id string) error {
	return r.db.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var projectIDs []string
		if err := tx.Model(&Project{}).Where("organization_id = ?", id).
			Pluck("id", &projectIDs).Error; err != nil {
			return err
		}

		// Project-scoped rows first (only when there are projects), then
		// the projects themselves, then the organization-scoped rows.
		steps := make([]orgScopedDelete, 0, 6)
		if len(projectIDs) > 0 {
			steps = append(steps,
				orgScopedDelete{&ProjectMember{}, "project_id IN ?", []any{projectIDs}},
				orgScopedDelete{&JoinRequest{}, "target_type = ? AND target_id IN ?",
					[]any{JoinTargetProject, projectIDs}},
			)
		}
		steps = append(steps,
			orgScopedDelete{&Project{}, "organization_id = ?", []any{id}},
			orgScopedDelete{&OrgMember{}, "organization_id = ?", []any{id}},
			orgScopedDelete{&Invitation{}, "organization_id = ?", []any{id}},
			orgScopedDelete{&JoinRequest{}, "target_type = ? AND target_id = ?",
				[]any{JoinTargetOrganization, id}},
		)
		for _, step := range steps {
			if err := tx.Where(step.where, step.args...).Delete(step.model).Error; err != nil {
				return err
			}
		}

		res := tx.Where("id = ?", id).Delete(&Organization{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}
