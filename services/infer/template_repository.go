// Engine parameter template persistence and matching.

package infer

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// EngineParamTemplateRepository persists engine-parameter templates.
type EngineParamTemplateRepository struct {
	db *database.Manager
}

// NewEngineParamTemplateRepository constructs a repository over a GORM handle.
func NewEngineParamTemplateRepository(db *gorm.DB) *EngineParamTemplateRepository {
	return &EngineParamTemplateRepository{db: database.NewManager(db)}
}

// Create inserts a template. A duplicate name maps to 10321.
func (r *EngineParamTemplateRepository) Create(ctx context.Context, row *EngineParamTemplate) error {
	err := r.db.DB(ctx).Create(row).Error
	if err != nil {
		if isUniqueViolation(err) {
			return apierrors.Newf(apierrors.CodeEngineTemplateExists,
				"infer: a parameter template named %q already exists", row.Name)
		}
		return err
	}
	return nil
}

// Update replaces a template's mutable content. A miss maps to 10320 and a
// name taken by another template to 10321.
func (r *EngineParamTemplateRepository) Update(ctx context.Context, row *EngineParamTemplate) error {
	res := r.db.DB(ctx).Model(&EngineParamTemplate{}).
		Where("id = ?", row.ID).
		Updates(map[string]any{
			"name":             row.Name,
			"description":      row.Description,
			"engine":           row.Engine,
			"model_id":         row.ModelID,
			"topology":         row.Topology,
			"accelerator":      row.Accelerator,
			"accelerator_type": row.AcceleratorType,
			"engine_args":      row.EngineArgs,
			"extra_args":       row.ExtraArgs,
			"role_engine_args": row.RoleEngineArgs,
			"updated_by":       row.UpdatedBy,
			"updated_at":       row.UpdatedAt,
		})
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return apierrors.Newf(apierrors.CodeEngineTemplateExists,
				"infer: a parameter template named %q already exists", row.Name)
		}
		return res.Error
	}
	if res.RowsAffected == 0 {
		// RowsAffected is 0 for a no-op update as well; confirm the row exists.
		if _, err := r.FindByID(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes a template, mapping a miss to 10320.
func (r *EngineParamTemplateRepository) Delete(ctx context.Context, id string) error {
	res := r.db.DB(ctx).Where("id = ?", id).Delete(&EngineParamTemplate{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apierrors.New(apierrors.CodeEngineTemplateNotFound)
	}
	return nil
}

// FindByID returns one template, mapping a miss to 10320.
func (r *EngineParamTemplateRepository) FindByID(ctx context.Context, id string) (*EngineParamTemplate, error) {
	var row EngineParamTemplate
	err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeEngineTemplateNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindByName returns the template with the given name, or nil.
func (r *EngineParamTemplateRepository) FindByName(ctx context.Context, name string) (*EngineParamTemplate, error) {
	var row EngineParamTemplate
	err := r.db.DB(ctx).Where("name = ?", name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// TemplateFilter narrows a template list.
type TemplateFilter struct {
	// Engine, ModelID, Topology, Accelerator and AcceleratorType are the
	// match context: a non-empty field keeps only templates whose field is
	// empty (matches any) or equal.
	Engine          string
	ModelID         string
	Topology        string
	Accelerator     string
	AcceleratorType string
	// Search narrows by name/description substring.
	Search string
}

// List returns the templates, newest first.
func (r *EngineParamTemplateRepository) List(ctx context.Context, f TemplateFilter, offset, limit int) ([]*EngineParamTemplate, int64, error) {
	query := r.db.DB(ctx).Model(&EngineParamTemplate{})
	// The engine is a requirement, not a preference: vLLM flags mean nothing
	// to SGLang, so a template for another engine is not a candidate.
	if f.Engine != "" {
		query = query.Where("engine = ?", f.Engine)
	}
	for _, pair := range []struct{ column, value string }{
		{"model_id", f.ModelID},
		{"topology", f.Topology},
		{"accelerator", f.Accelerator},
		{"accelerator_type", f.AcceleratorType},
	} {
		if pair.value == "" {
			continue
		}
		query = query.Where(pair.column+" = ? OR "+pair.column+" = ''", pair.value)
	}
	if search := strings.TrimSpace(f.Search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*EngineParamTemplate
	if err := query.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListAll returns every template, for export.
func (r *EngineParamTemplateRepository) ListAll(ctx context.Context) ([]*EngineParamTemplate, error) {
	var rows []*EngineParamTemplate
	if err := r.db.DB(ctx).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
