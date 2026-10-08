package prompt

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Repository persists prompts, versions, folders, and templates.
type Repository struct {
	db *database.Manager
}

// NewRepository constructs a Repository bound to a GORM database.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: database.NewManager(db)}
}

// InsertPrompt inserts a prompt and returns the generated id (AC1).
func (r *Repository) InsertPrompt(ctx context.Context, p *Prompt) (string, error) {
	if p.PromptID == "" {
		p.PromptID = uuid.NewString()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	p.UpdatedAt = p.CreatedAt
	if err := r.db.DB(ctx).Create(p).Error; err != nil {
		return "", apierrors.Wrap(apierrors.CodeInternal, err, "prompt: insert failed")
	}
	return p.PromptID, nil
}

// FindPromptByID returns a prompt scoped to the org. A miss maps to
// 12701 (AC1).
func (r *Repository) FindPromptByID(ctx context.Context, orgID, promptID string) (*Prompt, error) {
	if _, err := uuid.Parse(promptID); err != nil {
		return nil, apierrors.New(apierrors.CodePromptNotFound)
	}
	var row Prompt
	err := r.db.DB(ctx).First(&row, "prompt_id = ? AND organization_id = ?", promptID, orgID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodePromptNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindPromptAnyOrg returns a prompt by id regardless of org (admin view).
func (r *Repository) FindPromptAnyOrg(ctx context.Context, promptID string) (*Prompt, error) {
	if _, err := uuid.Parse(promptID); err != nil {
		return nil, apierrors.New(apierrors.CodePromptNotFound)
	}
	var row Prompt
	err := r.db.DB(ctx).First(&row, "prompt_id = ?", promptID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodePromptNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListPrompts returns one page of prompts for the org, newest first
// (AC5).
func (r *Repository) ListPrompts(ctx context.Context, filter PromptFilter) ([]*Prompt, int64, error) {
	query := r.db.DB(ctx).Model(&Prompt{}).Where("organization_id = ?", filter.OrganizationID)
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		query = query.Where("name LIKE ?", like)
	}
	if filter.FolderID != "" {
		query = query.Where("folder_id = ?", filter.FolderID)
	}
	if filter.ModelID != "" {
		query = query.Where("model_id = ?", filter.ModelID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Prompt
	if err := query.
		Order("created_at DESC").Order("prompt_id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListPromptsAnyOrg returns one page of prompts across all orgs (admin,
// masked).
func (r *Repository) ListPromptsAnyOrg(ctx context.Context, filter PromptFilter) ([]*Prompt, int64, error) {
	query := r.db.DB(ctx).Model(&Prompt{})
	if filter.OrganizationID != "" {
		query = query.Where("organization_id = ?", filter.OrganizationID)
	}
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		query = query.Where("name LIKE ?", like)
	}
	if filter.ModelID != "" {
		query = query.Where("model_id = ?", filter.ModelID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Prompt
	if err := query.
		Order("created_at DESC").Order("prompt_id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// UpdatePrompt persists the mutable fields of a prompt.
func (r *Repository) UpdatePrompt(ctx context.Context, p *Prompt) error {
	p.UpdatedAt = time.Now().UTC()
	return r.db.DB(ctx).Model(&Prompt{}).
		Where("prompt_id = ? AND organization_id = ?", p.PromptID, p.OrganizationID).
		Updates(map[string]any{
			"name":           p.Name,
			"model_id":       p.ModelID,
			"folder_id":      p.FolderID,
			"active_version": p.ActiveVersion,
			"updated_at":     p.UpdatedAt,
		}).Error
}

// DeletePrompt deletes a prompt and all its versions (cascade, AC2).
func (r *Repository) DeletePrompt(ctx context.Context, orgID, promptID string) error {
	if _, err := uuid.Parse(promptID); err != nil {
		return apierrors.New(apierrors.CodePromptNotFound)
	}
	res := r.db.DB(ctx).Where("prompt_id = ? AND organization_id = ?", promptID, orgID).Delete(&Prompt{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apierrors.New(apierrors.CodePromptNotFound)
	}
	return r.db.DB(ctx).Where("prompt_id = ?", promptID).Delete(&PromptVersion{}).Error
}

// InsertVersion inserts an immutable version row (AC2).
func (r *Repository) InsertVersion(ctx context.Context, v *PromptVersion) error {
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	return r.db.DB(ctx).Create(v).Error
}

// ListVersions returns a prompt's version history, newest first (AC2).
func (r *Repository) ListVersions(ctx context.Context, promptID string, offset, limit int) ([]*PromptVersion, int64, error) {
	var total int64
	if err := r.db.DB(ctx).Model(&PromptVersion{}).Where("prompt_id = ?", promptID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*PromptVersion
	if err := r.db.DB(ctx).Where("prompt_id = ?", promptID).
		Order("version DESC").
		Offset(offset).Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FindVersion returns a specific version. A miss maps to 12702 (AC2).
func (r *Repository) FindVersion(ctx context.Context, promptID string, version int) (*PromptVersion, error) {
	var row PromptVersion
	err := r.db.DB(ctx).First(&row, "prompt_id = ? AND version = ?", promptID, version).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodePromptVersionNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// NextVersion returns the next version number for a prompt.
func (r *Repository) NextVersion(ctx context.Context, promptID string) (int, error) {
	var max int
	if err := r.db.DB(ctx).Model(&PromptVersion{}).
		Where("prompt_id = ?", promptID).
		Select("COALESCE(MAX(version), 0)").
		Scan(&max).Error; err != nil {
		return 0, err
	}
	return max + 1, nil
}

// SetActiveVersion sets a prompt's active version.
func (r *Repository) SetActiveVersion(ctx context.Context, promptID string, version int) error {
	return r.db.DB(ctx).Model(&Prompt{}).Where("prompt_id = ?", promptID).
		Updates(map[string]any{"active_version": version, "updated_at": time.Now().UTC()}).Error
}

// InsertFolder inserts a folder and returns the generated id (AC3).
func (r *Repository) InsertFolder(ctx context.Context, f *PromptFolder) (string, error) {
	if f.FolderID == "" {
		f.FolderID = uuid.NewString()
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	if err := r.db.DB(ctx).Create(f).Error; err != nil {
		return "", apierrors.Wrap(apierrors.CodeInternal, err, "prompt: folder insert failed")
	}
	return f.FolderID, nil
}

// ListFolders returns the org's folders with prompt counts (AC3).
func (r *Repository) ListFolders(ctx context.Context, orgID string) ([]*PromptFolder, int64, error) {
	var rows []*PromptFolder
	if err := r.db.DB(ctx).Where("organization_id = ?", orgID).
		Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	var counts []struct {
		FolderID string
		Count    int64
	}
	if err := r.db.DB(ctx).Model(&Prompt{}).
		Select("folder_id, COUNT(*) as count").
		Where("organization_id = ? AND folder_id IS NOT NULL", orgID).
		Group("folder_id").Scan(&counts).Error; err != nil {
		return nil, 0, err
	}
	countMap := map[string]int64{}
	for _, c := range counts {
		countMap[c.FolderID] = c.Count
	}
	for _, f := range rows {
		f.PromptCount = countMap[f.FolderID]
	}
	return rows, int64(len(rows)), nil
}

// FindFolderByID returns a folder scoped to the org. A miss maps to
// 12704.
func (r *Repository) FindFolderByID(ctx context.Context, orgID, folderID string) (*PromptFolder, error) {
	if _, err := uuid.Parse(folderID); err != nil {
		return nil, apierrors.New(apierrors.CodePromptFolderNotFound)
	}
	var row PromptFolder
	err := r.db.DB(ctx).First(&row, "folder_id = ? AND organization_id = ?", folderID, orgID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodePromptFolderNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// DeleteFolder deletes an empty folder (12708 if non-empty, AC3).
func (r *Repository) DeleteFolder(ctx context.Context, orgID, folderID string) error {
	if _, err := uuid.Parse(folderID); err != nil {
		return apierrors.New(apierrors.CodePromptFolderNotFound)
	}
	var count int64
	if err := r.db.DB(ctx).Model(&Prompt{}).
		Where("organization_id = ? AND folder_id = ?", orgID, folderID).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return apierrors.New(apierrors.CodePromptFolderNotEmpty)
	}
	res := r.db.DB(ctx).Where("folder_id = ? AND organization_id = ?", folderID, orgID).Delete(&PromptFolder{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apierrors.New(apierrors.CodePromptFolderNotFound)
	}
	return nil
}

// IncrementUsage increments a prompt's usage counters (AC4).
func (r *Repository) IncrementUsage(ctx context.Context, promptID string) error {
	return r.db.DB(ctx).Model(&Prompt{}).Where("prompt_id = ?", promptID).
		Updates(map[string]any{
			"times_used":   gorm.Expr("times_used + 1"),
			"last_used_at": time.Now().UTC(),
		}).Error
}

// InsertTemplate inserts a shared template and returns the generated id
// (AC6).
func (r *Repository) InsertTemplate(ctx context.Context, t *PromptTemplate) (string, error) {
	if t.TemplateID == "" {
		t.TemplateID = uuid.NewString()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	t.UpdatedAt = t.CreatedAt
	if err := r.db.DB(ctx).Create(t).Error; err != nil {
		return "", apierrors.Wrap(apierrors.CodeInternal, err, "prompt: template insert failed")
	}
	return t.TemplateID, nil
}

// ListTemplates returns one page of shared templates (AC6).
func (r *Repository) ListTemplates(ctx context.Context, filter TemplateFilter) ([]*PromptTemplate, int64, error) {
	query := r.db.DB(ctx).Model(&PromptTemplate{})
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		query = query.Where("(name LIKE ? OR content LIKE ?)", like, like)
	}
	if filter.ModelID != "" {
		query = query.Where("model_id = ?", filter.ModelID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*PromptTemplate
	if err := query.
		Order("created_at DESC").Order("template_id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FindTemplateByID returns a shared template. A miss maps to 12707.
func (r *Repository) FindTemplateByID(ctx context.Context, templateID string) (*PromptTemplate, error) {
	if _, err := uuid.Parse(templateID); err != nil {
		return nil, apierrors.New(apierrors.CodePromptTemplateNotFound)
	}
	var row PromptTemplate
	err := r.db.DB(ctx).First(&row, "template_id = ?", templateID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodePromptTemplateNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpdateTemplate persists a shared template.
func (r *Repository) UpdateTemplate(ctx context.Context, t *PromptTemplate) error {
	t.UpdatedAt = time.Now().UTC()
	return r.db.DB(ctx).Model(&PromptTemplate{}).
		Where("template_id = ?", t.TemplateID).
		Updates(map[string]any{
			"name":       t.Name,
			"content":    t.Content,
			"model_id":   t.ModelID,
			"variables":  t.Variables,
			"updated_at": t.UpdatedAt,
		}).Error
}

// DeleteTemplate deletes a shared template.
func (r *Repository) DeleteTemplate(ctx context.Context, templateID string) error {
	if _, err := uuid.Parse(templateID); err != nil {
		return apierrors.New(apierrors.CodePromptTemplateNotFound)
	}
	res := r.db.DB(ctx).Where("template_id = ?", templateID).Delete(&PromptTemplate{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apierrors.New(apierrors.CodePromptTemplateNotFound)
	}
	return nil
}

// IncrementTemplateUsage increments a template's copy counter (AC6).
func (r *Repository) IncrementTemplateUsage(ctx context.Context, templateID string) error {
	return r.db.DB(ctx).Model(&PromptTemplate{}).Where("template_id = ?", templateID).
		Updates(map[string]any{"times_used": gorm.Expr("times_used + 1")}).Error
}

// AdminUsage returns the cross-tenant usage analytics (AC6).
func (r *Repository) AdminUsage(ctx context.Context, orgFilter string) (totalPrompts, totalUses int64, mostUsed []*Prompt, byOrg []*OrgUsage, err error) {
	base := r.db.DB(ctx).Model(&Prompt{})
	if orgFilter != "" {
		base = base.Where("organization_id = ?", orgFilter)
	}
	if err = base.Count(&totalPrompts).Error; err != nil {
		return
	}
	sumQuery := r.db.DB(ctx).Model(&Prompt{})
	if orgFilter != "" {
		sumQuery = sumQuery.Where("organization_id = ?", orgFilter)
	}
	if err = sumQuery.Select("COALESCE(SUM(times_used), 0)").Scan(&totalUses).Error; err != nil {
		return
	}
	mostQuery := r.db.DB(ctx).Model(&Prompt{})
	if orgFilter != "" {
		mostQuery = mostQuery.Where("organization_id = ?", orgFilter)
	}
	if err = mostQuery.Order("times_used DESC").Limit(10).Find(&mostUsed).Error; err != nil {
		return
	}
	orgQuery := r.db.DB(ctx).Model(&Prompt{})
	if orgFilter != "" {
		orgQuery = orgQuery.Where("organization_id = ?", orgFilter)
	}
	if err = orgQuery.Select("organization_id, COUNT(*) as prompt_count, COALESCE(SUM(times_used), 0) as total_uses").
		Group("organization_id").Order("total_uses DESC").Scan(&byOrg).Error; err != nil {
		return
	}
	return
}

// OrgUsage is one organization's aggregate usage.
type OrgUsage struct {
	OrganizationID string `gorm:"column:organization_id"`
	PromptCount    int64  `gorm:"column:prompt_count"`
	TotalUses      int64  `gorm:"column:total_uses"`
}
