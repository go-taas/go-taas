package model

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
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

// Compile-time guard: the FVT helpers keep their signatures.
var _ = func(db *gorm.DB) *Service { return NewForFVT(db) }
