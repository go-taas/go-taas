package prompt

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Prompt{}, &PromptVersion{}, &PromptFolder{}, &PromptTemplate{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func seedPrompt(t *testing.T, db *gorm.DB, orgID, name string) *Prompt {
	t.Helper()
	repo := NewRepository(db)
	p := &Prompt{
		OrganizationID: orgID,
		Name:           name,
		ActiveVersion:  1,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	id, err := repo.InsertPrompt(context.Background(), p)
	require.NoError(t, err)
	p.PromptID = id
	require.NoError(t, repo.InsertVersion(context.Background(), &PromptVersion{
		PromptID:  id,
		Version:   1,
		Content:   "hello",
		Variables: `["topic"]`,
		CreatedAt: time.Now().UTC(),
		CreatedBy: orgID,
	}))
	return p
}

func TestInsertAndFindPrompt(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	p := seedPrompt(t, db, "org-a", "greeting")
	got, err := repo.FindPromptByID(ctx, "org-a", p.PromptID)
	require.NoError(t, err)
	assert.Equal(t, "greeting", got.Name)

	// Unknown -> 12701.
	_, err = repo.FindPromptByID(ctx, "org-a", "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))

	// Wrong org -> 12701.
	_, err = repo.FindPromptByID(ctx, "org-b", p.PromptID)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))
}

func TestListPromptsFilters(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	seedPrompt(t, db, "org-a", "greeting")
	seedPrompt(t, db, "org-a", "summary")
	seedPrompt(t, db, "org-b", "other")

	rows, total, err := repo.ListPrompts(ctx, PromptFilter{OrganizationID: "org-a", Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)

	// Search filter.
	rows, total, err = repo.ListPrompts(ctx, PromptFilter{OrganizationID: "org-a", Search: "greet", Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, "greeting", rows[0].Name)
}

func TestVersions(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	p := seedPrompt(t, db, "org-a", "greeting")
	next, err := repo.NextVersion(ctx, p.PromptID)
	require.NoError(t, err)
	assert.Equal(t, 2, next)

	require.NoError(t, repo.InsertVersion(ctx, &PromptVersion{
		PromptID:  p.PromptID,
		Version:   2,
		Content:   "hello v2",
		Variables: "[]",
		CreatedAt: time.Now().UTC(),
		CreatedBy: "org-a",
	}))

	rows, total, err := repo.ListVersions(ctx, p.PromptID, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)
	assert.Equal(t, 2, rows[0].Version) // newest first

	v, err := repo.FindVersion(ctx, p.PromptID, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, v.Version)

	// Unknown version -> 12702.
	_, err = repo.FindVersion(ctx, p.PromptID, 99)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptVersionNotFound, apierrors.CodeOf(err))

	require.NoError(t, repo.SetActiveVersion(ctx, p.PromptID, 2))
	got, err := repo.FindPromptByID(ctx, "org-a", p.PromptID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.ActiveVersion)
}

func TestFolders(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	f := &PromptFolder{OrganizationID: "org-a", Name: "work"}
	folderID, err := repo.InsertFolder(ctx, f)
	require.NoError(t, err)
	f.FolderID = folderID

	rows, total, err := repo.ListFolders(ctx, "org-a")
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, rows, 1)

	// Delete empty folder.
	require.NoError(t, repo.DeleteFolder(ctx, "org-a", folderID))

	// Non-empty folder -> 12708.
	f2 := &PromptFolder{OrganizationID: "org-a", Name: "full"}
	f2ID, err := repo.InsertFolder(ctx, f2)
	require.NoError(t, err)
	p := seedPrompt(t, db, "org-a", "in-folder")
	require.NoError(t, db.Model(&Prompt{}).Where("prompt_id = ?", p.PromptID).Update("folder_id", f2ID).Error)
	err = repo.DeleteFolder(ctx, "org-a", f2ID)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptFolderNotEmpty, apierrors.CodeOf(err))
}

func TestUsageAndTemplates(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	p := seedPrompt(t, db, "org-a", "greeting")
	require.NoError(t, repo.IncrementUsage(ctx, p.PromptID))
	got, err := repo.FindPromptByID(ctx, "org-a", p.PromptID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.TimesUsed)

	// Templates.
	tpl := &PromptTemplate{Name: "tpl", Content: "content", Variables: "[]"}
	tplID, err := repo.InsertTemplate(ctx, tpl)
	require.NoError(t, err)
	tpl.TemplateID = tplID

	rows, total, err := repo.ListTemplates(ctx, TemplateFilter{Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, rows, 1)

	found, err := repo.FindTemplateByID(ctx, tplID)
	require.NoError(t, err)
	assert.Equal(t, "tpl", found.Name)

	// Unknown template -> 12707.
	_, err = repo.FindTemplateByID(ctx, "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptTemplateNotFound, apierrors.CodeOf(err))

	require.NoError(t, repo.IncrementTemplateUsage(ctx, tplID))
	require.NoError(t, repo.UpdateTemplate(ctx, &PromptTemplate{TemplateID: tplID, Name: "tpl2", Content: "c2", Variables: "[]"}))
	require.NoError(t, repo.DeleteTemplate(ctx, tplID))
	_, err = repo.FindTemplateByID(ctx, tplID)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptTemplateNotFound, apierrors.CodeOf(err))
}

func TestAdminUsage(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	seedPrompt(t, db, "org-a", "greeting")
	seedPrompt(t, db, "org-b", "other")

	totalPrompts, totalUses, mostUsed, byOrg, err := repo.AdminUsage(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, int64(2), totalPrompts)
	assert.Equal(t, int64(0), totalUses)
	assert.Len(t, mostUsed, 2)
	assert.Len(t, byOrg, 2)

	// Org-filtered usage.
	totalPrompts, _, _, byOrg, err = repo.AdminUsage(ctx, "org-a")
	require.NoError(t, err)
	assert.Equal(t, int64(1), totalPrompts)
	assert.Len(t, byOrg, 1)
	assert.Equal(t, "org-a", byOrg[0].OrganizationID)
}

func TestFindPromptAnyOrg(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	p := seedPrompt(t, db, "org-a", "greeting")
	got, err := repo.FindPromptAnyOrg(ctx, p.PromptID)
	require.NoError(t, err)
	assert.Equal(t, "greeting", got.Name)

	// Unknown -> 12701.
	_, err = repo.FindPromptAnyOrg(ctx, "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))
}

func TestUpdatePrompt(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	p := seedPrompt(t, db, "org-a", "greeting")
	require.NoError(t, repo.UpdatePrompt(ctx, &Prompt{
		PromptID:       p.PromptID,
		OrganizationID: "org-a",
		Name:           "renamed",
		ActiveVersion:  2,
	}))
	got, err := repo.FindPromptByID(ctx, "org-a", p.PromptID)
	require.NoError(t, err)
	assert.Equal(t, "renamed", got.Name)
	assert.Equal(t, 2, got.ActiveVersion)
}

func TestListPromptsAnyOrg(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	seedPrompt(t, db, "org-a", "greeting")
	seedPrompt(t, db, "org-b", "summary")

	// All orgs.
	rows, total, err := repo.ListPromptsAnyOrg(ctx, PromptFilter{Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)

	// Org filter.
	rows, total, err = repo.ListPromptsAnyOrg(ctx, PromptFilter{OrganizationID: "org-a", Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, "greeting", rows[0].Name)

	// Search filter.
	rows, total, err = repo.ListPromptsAnyOrg(ctx, PromptFilter{Search: "summ", Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, "summary", rows[0].Name)
}

func TestListTemplatesFilters(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	tpl := &PromptTemplate{Name: "tpl", Content: "content", Variables: "[]"}
	tplID, err := repo.InsertTemplate(ctx, tpl)
	require.NoError(t, err)
	tpl.TemplateID = tplID

	// Search filter.
	rows, total, err := repo.ListTemplates(ctx, TemplateFilter{Search: "tpl", Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, rows, 1)

	// Model filter.
	rows, total, err = repo.ListTemplates(ctx, TemplateFilter{ModelID: "nonexistent", Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Len(t, rows, 0)
}