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

// forceCascadeSQL is the ordered SQL run by ForceDeleteOrganization. Each
// statement either targets a table keyed by organization_id or removes a
// row that depends on a project, service, evaluation or API key the
// organization owns. Child rows are removed before their parents.
//
// audit_events is deliberately absent: it is the immutable record of what
// happened on the platform, not an organization-owned resource, and it is
// retained after the tenant is gone.
var forceCascadeSQL = []string{
	// Rows keyed by an organization-owned project.
	`DELETE FROM project_members WHERE project_id IN (SELECT id FROM projects WHERE organization_id = ?)`,
	// Rows keyed by an organization-owned inference service or API key.
	`DELETE FROM deployment_events WHERE service_id IN (SELECT id FROM inference_services WHERE organization_id = ?)`,
	`DELETE FROM inference_service_access WHERE service_id IN (SELECT id FROM inference_services WHERE organization_id = ?)`,
	`DELETE FROM load_tests WHERE service_id IN (SELECT id FROM inference_services WHERE organization_id = ?)`,
	// service_resource_metrics stores the service id as text, so cast the
	// uuid id when matching.
	`DELETE FROM service_resource_metrics WHERE service_id IN (SELECT CAST(id AS TEXT) FROM inference_services WHERE organization_id = ?)`,
	// Rows keyed by an organization-owned evaluation run.
	// evaluation_runs is keyed by run_id, not id.
	`DELETE FROM evaluation_run_cases WHERE run_id IN (SELECT run_id FROM evaluation_runs WHERE organization_id = ?)`,
	// Parent rows the organization owns directly.
	`DELETE FROM projects WHERE organization_id = ?`,
	`DELETE FROM inference_services WHERE organization_id = ?`,
	`DELETE FROM api_keys WHERE organization_id = ?`,
	`DELETE FROM evaluations WHERE organization_id = ?`,
	`DELETE FROM evaluation_runs WHERE organization_id = ?`,
	`DELETE FROM evaluation_cases WHERE organization_id = ?`,
	`DELETE FROM prompts WHERE organization_id = ?`,
	`DELETE FROM prompt_folders WHERE organization_id = ?`,
	`DELETE FROM batch_jobs WHERE organization_id = ?`,
	`DELETE FROM webhook_deliveries WHERE organization_id = ?`,
	`DELETE FROM webhooks WHERE organization_id = ?`,
	`DELETE FROM notifications WHERE organization_id = ?`,
	`DELETE FROM notification_preferences WHERE organization_id = ?`,
	`DELETE FROM notification_thresholds WHERE organization_id = ?`,
	`DELETE FROM optimization_recommendations WHERE organization_id = ?`,
	`DELETE FROM model_authorizations WHERE organization_id = ?`,
	`DELETE FROM routing_decisions WHERE organization_id = ?`,
	`DELETE FROM routing_group_grants WHERE organization_id = ?`,
	`DELETE FROM request_logs WHERE organization_id = ?`,
	`DELETE FROM traces WHERE organization_id = ?`,
	`DELETE FROM data_exports WHERE organization_id = ?`,
	`DELETE FROM billing_reports WHERE organization_id = ?`,
	`DELETE FROM billing_report_schedules WHERE organization_id = ?`,
	`DELETE FROM invoices WHERE organization_id = ?`,
	`DELETE FROM vouchers WHERE organization_id = ?`,
	`DELETE FROM charge_records WHERE organization_id = ?`,
	`DELETE FROM usage_lines WHERE organization_id = ?`,
	`DELETE FROM usage_records WHERE organization_id = ?`,
	`DELETE FROM accounts WHERE organization_id = ?`,
	// Tenancy rows last.
	`DELETE FROM invitations WHERE organization_id = ?`,
	`DELETE FROM org_members WHERE organization_id = ?`,
	`DELETE FROM join_requests WHERE target_type = 'organization' AND target_id = ?`,
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
		return deleteOrganizationRow(tx, id)
	})
}

// ForceDeleteOrganization removes an organization and every row it owns,
// including API keys, inference services and their dependent records. It
// is the operator escape hatch for a tenant that cannot be emptied
// incrementally. One transaction keeps a failure from leaving a partial
// delete behind. Tenancy tables that do not exist yet (an install that
// never enabled that module) are skipped rather than failing the delete.
func (r *Repository) ForceDeleteOrganization(ctx context.Context, id string) error {
	return r.db.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, stmt := range forceCascadeSQL {
			if err := tx.Exec(stmt, id).Error; err != nil {
				if isUndefinedTable(err) {
					continue
				}
				return err
			}
		}
		return deleteOrganizationRow(tx, id)
	})
}

// deleteOrganizationRow removes the organization itself and maps a miss
// to gorm.ErrRecordNotFound so the service can report 10005.
func deleteOrganizationRow(tx *gorm.DB, id string) error {
	res := tx.Where("id = ?", id).Delete(&Organization{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// isUndefinedTable reports whether err is the "relation does not exist"
// error, so a cascade over tables owned by modules that are not deployed
// can skip them.
func isUndefinedTable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "UndefinedTable")
}
