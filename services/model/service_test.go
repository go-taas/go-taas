package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/modelhub"
)

func newModelTestService(t *testing.T) *Service {
	t.Helper()
	return NewForFVT(newModelTestDB(t))
}

func TestServiceRegisterModel(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	resp, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name:        "qwen-3b",
		Version:     "v1",
		WeightPath:  "qwen/3b/v1",
		Description: "Qwen 3B",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetModelId())

	// Same model, new version: same model id.
	resp2, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name:       "qwen-3b",
		Version:    "v2",
		WeightPath: "qwen/3b/v2",
	})
	require.NoError(t, err)
	assert.Equal(t, resp.GetModelId(), resp2.GetModelId())

	// Duplicate (model, version): 10102.
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name:       "qwen-3b",
		Version:    "v1",
		WeightPath: "qwen/3b/v1",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelExists, ae.Code)
}

func TestServiceRegisterModelValidation(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	cases := []struct {
		name string
		req  *modelv1.RegisterModelRequest
	}{
		{"empty name", &modelv1.RegisterModelRequest{Name: "", Version: "v1", WeightPath: "qwen/v1"}},
		{"name too long", &modelv1.RegisterModelRequest{Name: strings.Repeat("a", 129), Version: "v1", WeightPath: "qwen/v1"}},
		{"empty version", &modelv1.RegisterModelRequest{Name: "m", Version: "", WeightPath: "qwen/v1"}},
		{"version too long", &modelv1.RegisterModelRequest{Name: "m", Version: strings.Repeat("a", 65), WeightPath: "qwen/v1"}},
		{"empty weight path", &modelv1.RegisterModelRequest{Name: "m", Version: "v1", WeightPath: ""}},
		{"weight path too long", &modelv1.RegisterModelRequest{Name: "m", Version: "v1", WeightPath: strings.Repeat("a", 513)}},
		{"absolute weight path", &modelv1.RegisterModelRequest{Name: "m", Version: "v1", WeightPath: "/abs/path"}},
		{"backslash weight path", &modelv1.RegisterModelRequest{Name: "m", Version: "v1", WeightPath: "a\\b"}},
		{"dotdot weight path", &modelv1.RegisterModelRequest{Name: "m", Version: "v1", WeightPath: "a/../b"}},
		{"description too long", &modelv1.RegisterModelRequest{Name: "m", Version: "v1", WeightPath: "qwen/v1", Description: strings.Repeat("d", 1025)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RegisterModel(ctx, tc.req)
			require.Error(t, err)
			ae, ok := apierrors.As(err)
			require.True(t, ok, "expected API error, got %v", err)
			assert.Equal(t, apierrors.CodeModelPathInvalid, ae.Code)
		})
	}
}

func TestServiceListModels(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	for _, name := range []string{"alpha", "beta", "gamma"} {
		_, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
			Name: name, Version: "v1", WeightPath: "p/v1",
		})
		require.NoError(t, err)
	}

	resp, err := svc.ListModels(ctx, &modelv1.ListModelsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 2},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 2)
	assert.Equal(t, int64(3), resp.GetPageMeta().GetTotal())
	// Default ordering: newest first.
	assert.Equal(t, "gamma", resp.GetModels()[0].GetName())

	// Second page.
	resp, err = svc.ListModels(ctx, &modelv1.ListModelsRequest{
		Page: &commonv1.PageRequest{Offset: 2, Limit: 2},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 1)
	assert.Equal(t, "alpha", resp.GetModels()[0].GetName())
}

func TestServiceGetModel(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	reg, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", WeightPath: "qwen/v1", Description: "Qwen 3B",
	})
	require.NoError(t, err)
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v2", WeightPath: "qwen/v2",
	})
	require.NoError(t, err)

	resp, err := svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	assert.Equal(t, "qwen-3b", resp.GetModel().GetName())
	// Versions ordered newest first (AC2).
	assert.Equal(t, []string{"v2", "v1"}, resp.GetVersions())
	// Latest version summary.
	assert.Equal(t, "v2", resp.GetModel().GetLatestVersion())
	assert.Equal(t, "qwen/v2", resp.GetModel().GetWeightPath())

	// Missing model.
	_, err = svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: "no-such"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)
}

func TestServiceListModelVersions(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	reg, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v2", WeightPath: "qwen/v2",
	})
	require.NoError(t, err)

	resp, err := svc.ListModelVersions(ctx, &modelv1.ListModelVersionsRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	assert.Equal(t, reg.GetModelId(), resp.GetModelId())
	assert.Equal(t, "qwen-3b", resp.GetName())
	assert.Equal(t, "", resp.GetActiveVersion())
	require.Len(t, resp.GetVersions(), 2)
	// Newest first.
	assert.Equal(t, "v2", resp.GetVersions()[0].GetVersion())
	assert.Equal(t, "qwen/v2", resp.GetVersions()[0].GetWeightPath())
	assert.Equal(t, false, resp.GetVersions()[0].GetIsActive())
	assert.Equal(t, int64(0), resp.GetVersions()[0].GetDeploymentCount())
	assert.Equal(t, "v1", resp.GetVersions()[1].GetVersion())

	// Unknown model → 10101.
	_, err = svc.ListModelVersions(ctx, &modelv1.ListModelVersionsRequest{ModelId: "no-such"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)
}

func TestServiceActivateModelVersion(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	reg, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v2", WeightPath: "qwen/v2",
	})
	require.NoError(t, err)

	// Activate v1.
	resp, err := svc.ActivateModelVersion(ctx, &modelv1.ActivateModelVersionRequest{
		ModelId: reg.GetModelId(), Version: "v1",
	})
	require.NoError(t, err)
	assert.Equal(t, "v1", resp.GetActiveVersion())

	// Idempotent.
	resp, err = svc.ActivateModelVersion(ctx, &modelv1.ActivateModelVersionRequest{
		ModelId: reg.GetModelId(), Version: "v1",
	})
	require.NoError(t, err)
	assert.Equal(t, "v1", resp.GetActiveVersion())

	// Activating v2 clears v1.
	resp, err = svc.ActivateModelVersion(ctx, &modelv1.ActivateModelVersionRequest{
		ModelId: reg.GetModelId(), Version: "v2",
	})
	require.NoError(t, err)
	assert.Equal(t, "v2", resp.GetActiveVersion())

	// Unknown model → 10101.
	_, err = svc.ActivateModelVersion(ctx, &modelv1.ActivateModelVersionRequest{
		ModelId: "no-such", Version: "v1",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)

	// Unknown version → 10103.
	_, err = svc.ActivateModelVersion(ctx, &modelv1.ActivateModelVersionRequest{
		ModelId: reg.GetModelId(), Version: "nope",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelVersionNotFound, ae.Code)
}

func TestServiceDeleteModel(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	reg, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)

	// Without a guard: delete succeeds.
	_, err = svc.DeleteModel(ctx, &modelv1.DeleteModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)

	_, err = svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: reg.GetModelId()})
	require.Error(t, err)
}

func TestServiceDeleteModelGuardBlocks(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	reg, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)

	svc.SetDeleteGuard(func(_ context.Context, _ string) error {
		return apierrors.Newf(apierrors.CodeModelNotFound,
			"referenced by inference service %s", "blocked-svc")
	})

	_, err = svc.DeleteModel(ctx, &modelv1.DeleteModelRequest{ModelId: reg.GetModelId()})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)
	assert.Contains(t, ae.Error(), "blocked-svc")

	// The model is still there.
	_, err = svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
}

func TestServiceMigrate(t *testing.T) {
	svc := newModelTestService(t)
	require.NoError(t, svc.Migrate(context.Background()))
}

// fakeDownloader records the download calls and writes a marker file so
// the test can assert the destination directory.
type fakeDownloader struct {
	calls []string
}

func (f *fakeDownloader) Download(_ context.Context, source modelhub.Source, modelID, destDir string) error {
	f.calls = append(f.calls, string(source)+"|"+modelID+"|"+destDir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "config.json"), []byte("{}"), 0o644)
}

func TestServiceRegisterModelWithDownload(t *testing.T) {
	svc := newModelTestService(t)
	weightsDir := t.TempDir()
	dl := &fakeDownloader{}
	svc.SetWeightsDir(weightsDir)
	svc.SetModelDownloader(dl)
	ctx := context.Background()

	resp, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name:          "qwen-3b",
		Version:       "v1",
		Source:        "modelscope",
		SourceModelId: "Qwen/Qwen2.5-0.5B",
		Description:   "Qwen 3B",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetModelId())

	// The downloader was invoked with the parsed source and the derived
	// destination directory.
	require.Len(t, dl.calls, 1)
	assert.Equal(t, "modelscope|Qwen/Qwen2.5-0.5B|"+filepath.Join(weightsDir, "Qwen__Qwen2.5-0.5B"), dl.calls[0])

	// The registered weight path is the model's directory relative to the
	// weights root.
	got, err := svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: resp.GetModelId()})
	require.NoError(t, err)
	assert.Equal(t, "Qwen__Qwen2.5-0.5B/", got.GetModel().GetWeightPath())
}

func TestServiceRegisterModelDownloadValidation(t *testing.T) {
	svc := newModelTestService(t)
	svc.SetWeightsDir(t.TempDir())
	svc.SetModelDownloader(&fakeDownloader{})
	ctx := context.Background()

	// Unsupported source fails fast.
	_, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "m", Version: "v1", Source: "unknown", SourceModelId: "org/model",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelPathInvalid, ae.Code)

	// Missing source_model_id fails.
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "m", Version: "v1", Source: "modelscope",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelPathInvalid, ae.Code)
}

func TestServiceRegisterModelDownloadNotConfigured(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	// No downloader wired: source-based registration fails closed.
	_, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "m", Version: "v1", Source: "modelscope", SourceModelId: "org/model",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelPathInvalid, ae.Code)

	// Downloader wired but no weights dir: still fails closed.
	svc.SetModelDownloader(&fakeDownloader{})
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "m", Version: "v1", Source: "modelscope", SourceModelId: "org/model",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelPathInvalid, ae.Code)
}

func TestServiceRegisterModelDownloadFailure(t *testing.T) {
	svc := newModelTestService(t)
	svc.SetWeightsDir(t.TempDir())
	svc.SetModelDownloader(failingDownloader{})
	ctx := context.Background()

	_, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "m", Version: "v1", Source: "modelscope", SourceModelId: "org/model",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelPathInvalid, ae.Code)
	assert.Contains(t, ae.Error(), "download")
}

// failingDownloader always fails the download.
type failingDownloader struct{}

func (failingDownloader) Download(context.Context, modelhub.Source, string, string) error {
	return errors.New("hub unreachable")
}

// Compile-time guard: the FVT helpers keep their signatures.
var _ = func(db *gorm.DB) *Service { return NewForFVT(db) }

func TestServiceRegisterModelHubModelID(t *testing.T) {
	svc := newModelTestService(t)
	svc.SetWeightsDir(t.TempDir())
	svc.SetModelDownloader(&fakeDownloader{})
	ctx := context.Background()

	// The hub id defaults to the hub's model id when the weights come
	// from a hub, and is projected in the catalog.
	resp, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "minimax", Version: "v1", Source: "modelscope",
		SourceModelId: "MiniMax/MiniMax-M3.1-Flash",
	})
	require.NoError(t, err)
	got, err := svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: resp.GetModelId()})
	require.NoError(t, err)
	assert.Equal(t, "MiniMax/MiniMax-M3.1-Flash", got.GetModel().GetHubModelId())

	// A hub id is globally unique: another model cannot claim it.
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "other-name", Version: "v1", HubModelId: "MiniMax/MiniMax-M3.1-Flash",
		WeightPath: "other/v1",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelExists, ae.Code)

	// The owning model can append a version under the same hub id.
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "minimax", Version: "v2", HubModelId: "MiniMax/MiniMax-M3.1-Flash",
		WeightPath: "minimax/v2",
	})
	require.NoError(t, err)
}

func TestServiceRegisterModelHubModelIDValidation(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	// A hub id must be "<vendor>/<model>".
	for _, bad := range []string{"qwen", "/qwen", "qwen/", "vendor na me/model", "vendor/model name"} {
		_, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
			Name: "m-" + bad, Version: "v1", HubModelId: bad, WeightPath: "m/v1",
		})
		require.Error(t, err, bad)
		ae, ok := apierrors.As(err)
		require.True(t, ok)
		assert.Equal(t, apierrors.CodeModelPathInvalid, ae.Code, bad)
	}

	// A model keeps its hub id: rebinding it to another id conflicts.
	_, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", HubModelId: "Qwen/Qwen3-3B", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v2", HubModelId: "Qwen/Qwen3-8B", WeightPath: "qwen/v2",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelExists, ae.Code)
}

func TestServiceListModelsProjectsHubModelID(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	_, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", HubModelId: "Qwen/Qwen3-3B", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)

	list, err := svc.ListModels(ctx, &modelv1.ListModelsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetModels(), 1)
	assert.Equal(t, "Qwen/Qwen3-3B", list.GetModels()[0].GetHubModelId())
}

// TestServiceDeleteModelRemovesWeights verifies deleting a model also
// frees the storage its weights occupied, and never deletes anything
// outside the weights root.
func TestServiceDeleteModelRemovesWeights(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()
	root := t.TempDir()
	svc.SetWeightsDir(root)

	// Two versions share the model's weight directory; a sibling directory
	// belongs to another model and must survive.
	dir := filepath.Join(root, "Qwen", "Qwen2.5-0.5B-Instruct")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.safetensors"), []byte("w"), 0o600))
	other := filepath.Join(root, "Qwen", "Other")
	require.NoError(t, os.MkdirAll(other, 0o750))

	reg, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", WeightPath: "Qwen/Qwen2.5-0.5B-Instruct/",
	})
	require.NoError(t, err)
	_, err = svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v2", WeightPath: "Qwen/Qwen2.5-0.5B-Instruct/",
	})
	require.NoError(t, err)

	_, err = svc.DeleteModel(ctx, &modelv1.DeleteModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)

	_, statErr := os.Stat(dir)
	assert.True(t, os.IsNotExist(statErr), "the model's weight directory is gone")
	_, statErr = os.Stat(other)
	assert.NoError(t, statErr, "an unrelated weights directory survives")
}

// TestServiceDeleteModelRefusesEscape verifies a weight path outside the
// weights root is never followed, even when the row carries one.
func TestServiceDeleteModelRefusesEscape(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()
	root := t.TempDir()
	svc.SetWeightsDir(root)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("x"), 0o600))

	repo, err := svc.repository()
	require.NoError(t, err)
	mdl := &Model{ID: uuid.NewString(), Name: "escape"}
	require.NoError(t, repo.DB(ctx).Create(mdl).Error)
	require.NoError(t, repo.versions.DB(ctx).Create(&Version{
		ID: uuid.NewString(), ModelID: mdl.ID, Version: "v1",
		WeightPath: "../" + filepath.Base(outside) + "/",
	}).Error)

	_, err = svc.DeleteModel(ctx, &modelv1.DeleteModelRequest{ModelId: mdl.ID})
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(outside, "keep.txt"))
	assert.NoError(t, statErr, "a path outside the weights root is never touched")
}
