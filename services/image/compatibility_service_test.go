package image

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/model"
)

// newModelRepoForTest builds a model repository bound to the given
// database (the image module reads the shared models table directly).
func newModelRepoForTest(t *testing.T, db *gorm.DB) *model.Repository {
	t.Helper()
	return model.NewRepository(db)
}

// newCompatServiceForTest builds an image service bound to a disposable
// database with the compatibility repository, model repository, and a
// fake card-types provider wired.
func newCompatServiceForTest(t *testing.T) *Service {
	t.Helper()
	db := newCompatTestDB(t)
	seedCompatDimensions(t, db)
	svc := NewWithRepositories(NewRepository(db), NewWarmupTaskRepository(db))
	svc.compatRepo = NewCompatibilityRepository(db)
	svc.modelRepo = newModelRepoForTest(t, db)
	svc.cardTypesProvider = fakeCardTypesProvider(testCardTypes)
	svc.compatLazyDefault = StatusExperimental
	wireRegistry(db)
	return svc
}

// fakeCardTypesProvider returns a fixed card-type set.
func fakeCardTypesProvider(cardTypes []CardType) CardTypesProvider {
	return CardTypesProviderFunc(func(_ context.Context) ([]CardType, error) {
		return cardTypes, nil
	})
}

// orgContext attaches the transitional organization metadata.
func orgContext(org string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-organization-id", org))
}

func TestServiceListCompatibilityMatrix(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := context.Background()

	// Seed the matrix first.
	_, err := svc.compatRepo.SeedIfEmpty(ctx, testCardTypes, StatusExperimental)
	require.NoError(t, err)

	resp, err := svc.ListCompatibilityMatrix(ctx, &imagev1.ListCompatibilityMatrixRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(4), resp.GetPageMeta().GetTotal())
	assert.Len(t, resp.GetCells(), 4)
	// Each cell carries status, note, not_in_fleet, updated_at.
	for _, c := range resp.GetCells() {
		assert.NotEmpty(t, c.GetStatus())
		assert.NotEmpty(t, c.GetModelName())
	}

	// Filter by status.
	resp, err = svc.ListCompatibilityMatrix(ctx, &imagev1.ListCompatibilityMatrixRequest{Status: StatusSupported})
	require.NoError(t, err)
	assert.Equal(t, int64(0), resp.GetPageMeta().GetTotal())

	// Filter by model.
	resp, err = svc.ListCompatibilityMatrix(ctx, &imagev1.ListCompatibilityMatrixRequest{ModelId: "model-qwen"})
	require.NoError(t, err)
	assert.Equal(t, int64(4), resp.GetPageMeta().GetTotal())
}

func TestServiceListCompatibilityDimensions(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := context.Background()

	_, err := svc.compatRepo.SeedIfEmpty(ctx, testCardTypes, StatusExperimental)
	require.NoError(t, err)

	resp, err := svc.ListCompatibilityDimensions(ctx, &imagev1.ListCompatibilityDimensionsRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.GetModels(), 1)
	assert.Len(t, resp.GetEngines(), 2)
	assert.Len(t, resp.GetCardTypes(), 2)
	// After seed: 2 experimental (vllm/A800, sglang/M100), 2 unsupported.
	assert.Equal(t, int64(2), resp.GetStatusCounts().GetExperimental())
	assert.Equal(t, int64(2), resp.GetStatusCounts().GetUnsupported())
}

func TestServiceSetCompatibilityStatus(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := context.Background()

	// AC4: set a cell's status and note.
	resp, err := svc.SetCompatibilityStatus(ctx, &imagev1.SetCompatibilityStatusRequest{
		ModelId: "model-qwen", Engine: "vllm", CardType: "A800",
		Status: StatusSupported, Note: "validated",
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSupported, resp.GetCell().GetStatus())
	assert.Equal(t, "validated", resp.GetCell().GetNote())

	// Invalid status → 10210.
	_, err = svc.SetCompatibilityStatus(ctx, &imagev1.SetCompatibilityStatusRequest{
		ModelId: "model-qwen", Engine: "vllm", CardType: "A800", Status: "bogus",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityStatusInvalid, ae.Code)

	// Unknown dimension → 10211.
	_, err = svc.SetCompatibilityStatus(ctx, &imagev1.SetCompatibilityStatusRequest{
		ModelId: "model-missing", Engine: "vllm", CardType: "A800", Status: StatusSupported,
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityDimensionInvalid, ae.Code)
}

func TestServiceBulkSetCompatibilityStatus(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := context.Background()

	resp, err := svc.BulkSetCompatibilityStatus(ctx, &imagev1.BulkSetCompatibilityStatusRequest{
		Cells: []*imagev1.CompatibilityCellRef{
			{ModelId: "model-qwen", Engine: "vllm", CardType: "A800"},
			{ModelId: "model-qwen", Engine: "vllm", CardType: "M100"},
		},
		Status: StatusSupported, Note: "bulk",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), resp.GetUpdated())
}

func TestServiceGetCompatibilityCell(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := context.Background()

	// A missing cell is materialized lazily (AC4).
	resp, err := svc.GetCompatibilityCell(ctx, &imagev1.GetCompatibilityCellRequest{
		ModelId: "model-qwen", Engine: "vllm", CardType: "A800",
	})
	require.NoError(t, err)
	assert.Equal(t, StatusExperimental, resp.GetCell().GetStatus())

	// An unknown dimension → 10211.
	_, err = svc.GetCompatibilityCell(ctx, &imagev1.GetCompatibilityCellRequest{
		ModelId: "model-missing", Engine: "vllm", CardType: "A800",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityDimensionInvalid, ae.Code)
}

func TestServiceGetModelCompatibility(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := orgContext("org-a")

	_, err := svc.compatRepo.SeedIfEmpty(ctx, testCardTypes, StatusExperimental)
	require.NoError(t, err)
	// Curate one supported.
	_, err = svc.compatRepo.SetStatus(ctx, "model-qwen", "vllm", "A800", StatusSupported, "", testCardTypes, StatusExperimental)
	require.NoError(t, err)

	// AC7: masked projection returns only supported/experimental.
	resp, err := svc.GetModelCompatibility(ctx, &imagev1.GetModelCompatibilityRequest{ModelId: "model-qwen"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.GetSupportedCount())
	assert.Equal(t, int64(1), resp.GetExperimentalCount())
	assert.Len(t, resp.GetEntries(), 2)
	for _, e := range resp.GetEntries() {
		assert.NotEqual(t, StatusUnsupported, e.GetStatus())
	}

	// Unknown model → 10101.
	_, err = svc.GetModelCompatibility(ctx, &imagev1.GetModelCompatibilityRequest{ModelId: "model-missing"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)
}

func TestServiceGetCompatibilityStatusNarrow(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := context.Background()

	// The narrow interface materializes a default row lazily.
	status, err := svc.GetCompatibilityStatus(ctx, "model-qwen", "vllm", "A800")
	require.NoError(t, err)
	assert.Equal(t, StatusExperimental, status)

	// An unknown dimension returns 10211.
	_, err = svc.GetCompatibilityStatus(ctx, "model-missing", "vllm", "A800")
	require.Error(t, err)
}

func TestServiceModelCompatibilitySummary(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := context.Background()

	_, err := svc.compatRepo.SeedIfEmpty(ctx, testCardTypes, StatusExperimental)
	require.NoError(t, err)
	// Curate vllm/A800 to supported.
	_, err = svc.compatRepo.SetStatus(ctx, "model-qwen", "vllm", "A800", StatusSupported, "", testCardTypes, StatusExperimental)
	require.NoError(t, err)

	// The summary groups card types by engine.
	summary, err := svc.ModelCompatibilitySummary(ctx, "model-qwen")
	require.NoError(t, err)
	assert.Contains(t, summary, "vllm")
	assert.Contains(t, summary, "A800")
}

func TestNewCompatibilityChecker(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := context.Background()

	checker := NewCompatibilityChecker(svc)
	require.NotNil(t, checker)
	// The checker resolves the status through the narrow interface.
	status, err := checker(ctx, "model-qwen", "vllm", "A800")
	require.NoError(t, err)
	assert.Equal(t, StatusExperimental, status)
}

func TestNewModelCompatibilitySummaryProvider(t *testing.T) {
	svc := newCompatServiceForTest(t)
	ctx := context.Background()

	provider := NewModelCompatibilitySummaryProvider(svc)
	require.NotNil(t, provider)
	// The provider resolves the masked summary through the narrow read.
	summary, err := provider.ModelCompatibilitySummary(ctx, "model-qwen")
	require.NoError(t, err)
	assert.Equal(t, "", summary, "no curated cells yet")
}
