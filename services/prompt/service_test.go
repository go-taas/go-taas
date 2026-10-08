package prompt

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	promptv1 "github.com/go-taas/go-taas/proto/taas/prompt/v1"
)

func orgCtx(orgID string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(organizationMetadataKey, orgID))
}

type fakeRoleGuard struct {
	deny bool
}

func (f *fakeRoleGuard) RequireRole(_ context.Context, _ string, _ string, _ string) error {
	if f.deny {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

type fakeSessionUserResolver struct{}

func (f *fakeSessionUserResolver) SessionUserID(_ context.Context) (string, error) {
	return "user-1", nil
}

func TestServiceCreatePrompt(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	resp, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{
		Name:      "greeting",
		Content:   "Hello ${topic}",
		Variables: []string{"${topic}"},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), resp.GetPrompt().GetVersion())
	assert.Equal(t, "greeting", resp.GetPrompt().GetName())
}

func TestServiceCreatePromptNameConflict(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	_, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "greeting", Content: "hi"})
	require.NoError(t, err)

	_, err = svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "greeting", Content: "hi again"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNameConflict, apierrors.CodeOf(err))
}

func TestServiceCreatePromptInvalid(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	// Empty content -> 12705.
	_, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "x", Content: ""})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidContent, apierrors.CodeOf(err))

	// Invalid variable -> 12706.
	_, err = svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "x", Content: "hi", Variables: []string{"bad"}})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidVariable, apierrors.CodeOf(err))
}

func TestServiceUpdateAndRollback(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	created, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "greeting", Content: "v1"})
	require.NoError(t, err)
	id := created.GetPrompt().GetPromptId()

	// Update creates version 2.
	updated, err := svc.UpdatePrompt(ctx, &promptv1.UpdatePromptRequest{PromptId: id, Content: "v2"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), updated.GetPrompt().GetVersion())

	// List versions.
	versions, err := svc.ListPromptVersions(ctx, &promptv1.ListPromptVersionsRequest{PromptId: id})
	require.NoError(t, err)
	require.Len(t, versions.GetVersions(), 2)

	// Rollback to version 1.
	rolled, err := svc.RollbackPrompt(ctx, &promptv1.RollbackPromptRequest{PromptId: id, Version: 1})
	require.NoError(t, err)
	assert.Equal(t, int32(1), rolled.GetPrompt().GetVersion())

	// Rollback to unknown version -> 12702.
	_, err = svc.RollbackPrompt(ctx, &promptv1.RollbackPromptRequest{PromptId: id, Version: 99})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptVersionNotFound, apierrors.CodeOf(err))
}

func TestServiceFolders(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	created, err := svc.CreatePromptFolder(ctx, &promptv1.CreatePromptFolderRequest{Name: "work"})
	require.NoError(t, err)
	folderID := created.GetFolder().GetFolderId()

	folders, err := svc.ListPromptFolders(ctx, &promptv1.ListPromptFoldersRequest{})
	require.NoError(t, err)
	require.Len(t, folders.GetFolders(), 1)

	// Delete empty folder.
	_, err = svc.DeletePromptFolder(ctx, &promptv1.DeletePromptFolderRequest{FolderId: folderID})
	require.NoError(t, err)

	// Non-empty folder -> 12708.
	created2, err := svc.CreatePromptFolder(ctx, &promptv1.CreatePromptFolderRequest{Name: "full"})
	require.NoError(t, err)
	folderID2 := created2.GetFolder().GetFolderId()
	_, err = svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "p", Content: "c", FolderId: folderID2})
	require.NoError(t, err)
	_, err = svc.DeletePromptFolder(ctx, &promptv1.DeletePromptFolderRequest{FolderId: folderID2})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptFolderNotEmpty, apierrors.CodeOf(err))
}

func TestServiceUsage(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	created, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "greeting", Content: "hi"})
	require.NoError(t, err)
	id := created.GetPrompt().GetPromptId()

	rec, err := svc.RecordPromptUsage(ctx, &promptv1.RecordPromptUsageRequest{PromptId: id})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rec.GetTimesUsed())

	got, err := svc.GetPromptUsage(ctx, &promptv1.GetPromptUsageRequest{PromptId: id})
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.GetTimesUsed())
}

func TestServiceTemplatesAndCopy(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.roleGuard = &fakeRoleGuard{}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	adminCtx := orgCtx("org-a")
	userCtx := orgCtx("org-b")

	// Admin creates a template.
	created, err := svc.AdminCreateTemplate(adminCtx, &promptv1.AdminCreateTemplateRequest{
		Name:    "tpl",
		Content: "template content",
	})
	require.NoError(t, err)
	tplID := created.GetTemplate().GetTemplateId()

	// User lists templates.
	templates, err := svc.ListPromptTemplates(userCtx, &promptv1.ListPromptTemplatesRequest{})
	require.NoError(t, err)
	require.Len(t, templates.GetTemplates(), 1)

	// User copies the template.
	copied, err := svc.CopyPromptTemplate(userCtx, &promptv1.CopyPromptTemplateRequest{TemplateId: tplID})
	require.NoError(t, err)
	assert.Equal(t, int32(1), copied.GetPrompt().GetVersion())
	assert.Equal(t, "template content", copied.GetPrompt().GetContent())

	// Admin updates and deletes.
	_, err = svc.AdminUpdateTemplate(adminCtx, &promptv1.AdminUpdateTemplateRequest{
		TemplateId: tplID, Name: "tpl2", Content: "c2",
	})
	require.NoError(t, err)
	_, err = svc.AdminDeleteTemplate(adminCtx, &promptv1.AdminDeleteTemplateRequest{TemplateId: tplID})
	require.NoError(t, err)

	// Unknown template -> 12707.
	_, err = svc.CopyPromptTemplate(userCtx, &promptv1.CopyPromptTemplateRequest{TemplateId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptTemplateNotFound, apierrors.CodeOf(err))
}

func TestServiceAdminRoleGuard(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.roleGuard = &fakeRoleGuard{deny: true}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	ctx := orgCtx("org-a")

	_, err := svc.AdminListPrompts(ctx, &promptv1.AdminListPromptsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestServiceAdminListAndUsage(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.roleGuard = &fakeRoleGuard{}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	ctx := orgCtx("org-a")

	_, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "greeting", Content: "hi"})
	require.NoError(t, err)

	list, err := svc.AdminListPrompts(ctx, &promptv1.AdminListPromptsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetPrompts(), 1)
	// Masked: no content.
	assert.Equal(t, "", list.GetPrompts()[0].GetContent())

	usage, err := svc.AdminPromptUsage(ctx, &promptv1.AdminPromptUsageRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), usage.GetTotalPrompts())
}

func TestServiceDeletePrompt(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	created, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "greeting", Content: "hi"})
	require.NoError(t, err)
	id := created.GetPrompt().GetPromptId()

	_, err = svc.DeletePrompt(ctx, &promptv1.DeletePromptRequest{PromptId: id})
	require.NoError(t, err)

	_, err = svc.GetPrompt(ctx, &promptv1.GetPromptRequest{PromptId: id})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))
}

func TestServiceCreatePromptErrorPaths(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	// Empty name -> invalid.
	_, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "", Content: "hi"})
	require.Error(t, err)

	// Unknown folder -> 12704.
	_, err = svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{
		Name: "p", Content: "hi", FolderId: "00000000-0000-0000-0000-000000000000",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptFolderNotFound, apierrors.CodeOf(err))

	// Missing org -> unauthorized.
	_, err = svc.CreatePrompt(context.Background(), &promptv1.CreatePromptRequest{Name: "p", Content: "hi"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}

func TestServiceUpdatePromptErrorPaths(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	created, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "greeting", Content: "v1"})
	require.NoError(t, err)
	id := created.GetPrompt().GetPromptId()

	// Invalid content -> 12705.
	_, err = svc.UpdatePrompt(ctx, &promptv1.UpdatePromptRequest{PromptId: id, Content: ""})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidContent, apierrors.CodeOf(err))

	// Invalid variable -> 12706.
	_, err = svc.UpdatePrompt(ctx, &promptv1.UpdatePromptRequest{PromptId: id, Content: "hi", Variables: []string{"bad"}})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidVariable, apierrors.CodeOf(err))

	// Unknown prompt -> 12701.
	_, err = svc.UpdatePrompt(ctx, &promptv1.UpdatePromptRequest{PromptId: "00000000-0000-0000-0000-000000000000", Content: "hi"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))
}

func TestServiceGetAndListErrorPaths(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	// Get unknown prompt -> 12701.
	_, err := svc.GetPrompt(ctx, &promptv1.GetPromptRequest{PromptId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))

	// List versions of unknown prompt -> 12701.
	_, err = svc.ListPromptVersions(ctx, &promptv1.ListPromptVersionsRequest{PromptId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))

	// Rollback unknown prompt -> 12701.
	_, err = svc.RollbackPrompt(ctx, &promptv1.RollbackPromptRequest{PromptId: "00000000-0000-0000-0000-000000000000", Version: 1})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))

	// Delete unknown prompt -> 12701.
	_, err = svc.DeletePrompt(ctx, &promptv1.DeletePromptRequest{PromptId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))
}

func TestServiceFolderErrorPaths(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	// Empty folder name -> invalid.
	_, err := svc.CreatePromptFolder(ctx, &promptv1.CreatePromptFolderRequest{Name: ""})
	require.Error(t, err)

	// Delete unknown folder -> 12704.
	_, err = svc.DeletePromptFolder(ctx, &promptv1.DeletePromptFolderRequest{FolderId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptFolderNotFound, apierrors.CodeOf(err))
}

func TestServiceUsageErrorPaths(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	// Get usage of unknown prompt -> 12701.
	_, err := svc.GetPromptUsage(ctx, &promptv1.GetPromptUsageRequest{PromptId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))

	// Record usage of unknown prompt -> 12701.
	_, err = svc.RecordPromptUsage(ctx, &promptv1.RecordPromptUsageRequest{PromptId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptNotFound, apierrors.CodeOf(err))
}

func TestServiceAdminTemplates(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.roleGuard = &fakeRoleGuard{}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	ctx := orgCtx("org-a")

	// AdminListTemplates on empty library.
	list, err := svc.AdminListTemplates(ctx, &promptv1.AdminListTemplatesRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetTemplates(), 0)

	// AdminCreateTemplate invalid name -> error.
	_, err = svc.AdminCreateTemplate(ctx, &promptv1.AdminCreateTemplateRequest{Name: "", Content: "c"})
	require.Error(t, err)

	// AdminCreateTemplate invalid content -> 12705.
	_, err = svc.AdminCreateTemplate(ctx, &promptv1.AdminCreateTemplateRequest{Name: "t", Content: ""})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidContent, apierrors.CodeOf(err))

	// AdminCreateTemplate invalid variable -> 12706.
	_, err = svc.AdminCreateTemplate(ctx, &promptv1.AdminCreateTemplateRequest{Name: "t", Content: "c", Variables: []string{"bad"}})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidVariable, apierrors.CodeOf(err))

	// Create a valid template.
	created, err := svc.AdminCreateTemplate(ctx, &promptv1.AdminCreateTemplateRequest{Name: "tpl", Content: "content"})
	require.NoError(t, err)
	tplID := created.GetTemplate().GetTemplateId()

	// AdminListTemplates now returns it.
	list, err = svc.AdminListTemplates(ctx, &promptv1.AdminListTemplatesRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetTemplates(), 1)

	// AdminUpdateTemplate invalid name -> error.
	_, err = svc.AdminUpdateTemplate(ctx, &promptv1.AdminUpdateTemplateRequest{TemplateId: tplID, Name: "", Content: "c"})
	require.Error(t, err)

	// AdminUpdateTemplate unknown template -> 12707.
	_, err = svc.AdminUpdateTemplate(ctx, &promptv1.AdminUpdateTemplateRequest{
		TemplateId: "00000000-0000-0000-0000-000000000000", Name: "x", Content: "c",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptTemplateNotFound, apierrors.CodeOf(err))

	// AdminDeleteTemplate unknown template -> 12707.
	_, err = svc.AdminDeleteTemplate(ctx, &promptv1.AdminDeleteTemplateRequest{TemplateId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptTemplateNotFound, apierrors.CodeOf(err))

	// AdminDeleteTemplate success.
	_, err = svc.AdminDeleteTemplate(ctx, &promptv1.AdminDeleteTemplateRequest{TemplateId: tplID})
	require.NoError(t, err)
}

func TestServiceAdminRoleGuardDeny(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.roleGuard = &fakeRoleGuard{deny: true}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	ctx := orgCtx("org-a")

	// AdminPromptUsage denied -> 10036.
	_, err := svc.AdminPromptUsage(ctx, &promptv1.AdminPromptUsageRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// AdminListTemplates denied -> 10036.
	_, err = svc.AdminListTemplates(ctx, &promptv1.AdminListTemplatesRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// AdminCreateTemplate denied -> 10036.
	_, err = svc.AdminCreateTemplate(ctx, &promptv1.AdminCreateTemplateRequest{Name: "t", Content: "c"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// AdminUpdateTemplate denied -> 10036.
	_, err = svc.AdminUpdateTemplate(ctx, &promptv1.AdminUpdateTemplateRequest{TemplateId: "x", Name: "t", Content: "c"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// AdminDeleteTemplate denied -> 10036.
	_, err = svc.AdminDeleteTemplate(ctx, &promptv1.AdminDeleteTemplateRequest{TemplateId: "x"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestServiceCopyTemplateInvalidName(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.roleGuard = &fakeRoleGuard{}
	svc.sessionUserResolver = &fakeSessionUserResolver{}
	adminCtx := orgCtx("org-a")
	userCtx := orgCtx("org-b")

	created, err := svc.AdminCreateTemplate(adminCtx, &promptv1.AdminCreateTemplateRequest{Name: "tpl", Content: "c"})
	require.NoError(t, err)
	tplID := created.GetTemplate().GetTemplateId()

	// Copy with an over-long name -> error.
	longName := strings.Repeat("x", 200)
	_, err = svc.CopyPromptTemplate(userCtx, &promptv1.CopyPromptTemplateRequest{TemplateId: tplID, Name: longName})
	require.Error(t, err)
}

func TestServiceListPromptsFilters(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	ctx := orgCtx("org-a")

	_, err := svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "alpha", Content: "hi"})
	require.NoError(t, err)
	_, err = svc.CreatePrompt(ctx, &promptv1.CreatePromptRequest{Name: "beta", Content: "yo"})
	require.NoError(t, err)

	// Search filter.
	list, err := svc.ListPrompts(ctx, &promptv1.ListPromptsRequest{Search: "alpha"})
	require.NoError(t, err)
	require.Len(t, list.GetPrompts(), 1)
	assert.Equal(t, "alpha", list.GetPrompts()[0].GetName())

	// Pagination.
	list, err = svc.ListPrompts(ctx, &promptv1.ListPromptsRequest{Page: &commonv1.PageRequest{Offset: 0, Limit: 1}})
	require.NoError(t, err)
	require.Len(t, list.GetPrompts(), 1)
	assert.Equal(t, int64(2), list.GetPageMeta().GetTotal())
}
