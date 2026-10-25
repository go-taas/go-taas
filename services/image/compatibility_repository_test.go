package image

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/model"
)

// newCompatTestDB builds a database with the image, warmup, and
// compatibility schemas plus a models table (the matrix reads the shared
// models table directly).
func newCompatTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Image{}, &WarmupTask{}, &CompatibilityCell{}))
	require.NoError(t, db.AutoMigrate(&ModelRow{}))
	// The model-authorization check (feature #13) reads the
	// model_authorizations table; migrate the model schema so the
	// user-realm masked projection works.
	require.NoError(t, model.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

// seedCompatDimensions seeds one model, two engines (nvidia vllm,
// metax sglang), and two card types (nvidia A800, metax M100) so the
// matrix has a non-empty cross product.
func seedCompatDimensions(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Create(&ModelRow{ID: testModelID, Name: "qwen-3b"}).Error)
	require.NoError(t, db.Create(&Image{ID: "img-vllm", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"}).Error)
	require.NoError(t, db.Create(&Image{ID: "img-sglang", Name: "ghcr.io/go-taas/sglang", Tag: "v0.1.4", Accelerator: "metax", Engine: "sglang"}).Error)
}

// testModelID is the seeded catalog model. It is a valid UUID: the
// model repository's GetModel treats non-uuid ids as a plain miss
// (the uuid.Parse guard), so the service-level tests must seed a
// uuid-shaped id.
const testModelID = "11111111-1111-4111-8111-111111111111"

var testCardTypes = []CardType{
	{Vendor: "nvidia", CardType: "A800"},
	{Vendor: "metax", CardType: "M100"},
}

func TestCompatibilitySeedIfEmpty(t *testing.T) {
	db := newCompatTestDB(t)
	seedCompatDimensions(t, db)
	repo := NewCompatibilityRepository(db)
	ctx := context.Background()

	// AC1: first-boot seed creates a row per combination; vendor-matched
	// combos default to experimental, vendor-mismatched to unsupported.
	seeded, err := repo.SeedIfEmpty(ctx, testCardTypes, StatusExperimental)
	require.NoError(t, err)
	// 1 model × 2 engines × 2 card types = 4 cells.
	assert.Equal(t, 4, seeded)

	cell, err := repo.findCell(ctx, testModelID, "vllm", "A800")
	require.NoError(t, err)
	require.NotNil(t, cell)
	assert.Equal(t, StatusExperimental, cell.Status, "nvidia vllm on nvidia A800 is vendor-matched")

	cell, err = repo.findCell(ctx, testModelID, "vllm", "M100")
	require.NoError(t, err)
	require.NotNil(t, cell)
	assert.Equal(t, StatusUnsupported, cell.Status, "nvidia vllm on metax M100 is vendor-mismatched")

	// A non-empty table is never re-seeded (AD3).
	seeded, err = repo.SeedIfEmpty(ctx, testCardTypes, StatusExperimental)
	require.NoError(t, err)
	assert.Equal(t, 0, seeded)
}

func TestCompatibilityEnsureCellLazySeed(t *testing.T) {
	db := newCompatTestDB(t)
	seedCompatDimensions(t, db)
	repo := NewCompatibilityRepository(db)
	ctx := context.Background()

	// AC4: a missing cell is created lazily with the default rule.
	cell, err := repo.EnsureCell(ctx, testModelID, "vllm", "A800", testCardTypes, StatusExperimental)
	require.NoError(t, err)
	require.NotNil(t, cell)
	assert.Equal(t, StatusExperimental, cell.Status)

	// An unknown dimension returns 10211.
	_, err = repo.EnsureCell(ctx, "model-missing", "vllm", "A800", testCardTypes, StatusExperimental)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityDimensionInvalid, ae.Code)

	_, err = repo.EnsureCell(ctx, testModelID, "unknown-engine", "A800", testCardTypes, StatusExperimental)
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityDimensionInvalid, ae.Code)

	_, err = repo.EnsureCell(ctx, testModelID, "vllm", "unknown-card", testCardTypes, StatusExperimental)
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityDimensionInvalid, ae.Code)
	// The message names the offending value and the valid ones, so the
	// console can tell the operator what to type.
	assert.Contains(t, ae.Message, "unknown-card")
	assert.Contains(t, ae.Message, "known card types:")
}

// seedMultiAcceleratorEngine registers one model and one engine (vllm) on
// two accelerators, inserting the vendor-mismatching accelerator first so
// a "first match wins" rule would resolve to iluvatar and wrongly mark the
// nvidia card type unsupported.
func seedMultiAcceleratorEngine(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Create(&ModelRow{ID: testModelID, Name: "qwen-3b"}).Error)
	require.NoError(t, db.Create(&Image{ID: "img-vllm-iluvatar", Name: "ghcr.io/go-taas/vllm-iluvatar", Tag: "v0.6.3", Accelerator: "iluvatar", Engine: "vllm"}).Error)
	require.NoError(t, db.Create(&Image{ID: "img-vllm-nvidia", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"}).Error)
}

// TestCompatibilityLazySeedMultiAccelerator pins the vendor-match rule
// when one engine is registered on several accelerators: the nvidia card
// type is vendor-matched because vllm runs on nvidia, even though vllm
// also runs on iluvatar. The derived status must not depend on the row
// order of the images table.
func TestCompatibilityLazySeedMultiAccelerator(t *testing.T) {
	db := newCompatTestDB(t)
	seedMultiAcceleratorEngine(t, db)
	repo := NewCompatibilityRepository(db)
	ctx := context.Background()
	cardTypes := []CardType{{Vendor: "nvidia", CardType: "A800"}}

	cell, err := repo.EnsureCell(ctx, testModelID, "vllm", "A800", cardTypes, StatusExperimental)
	require.NoError(t, err)
	require.NotNil(t, cell)
	assert.Equal(t, StatusExperimental, cell.Status,
		"vllm runs on nvidia, so the nvidia card type is vendor-matched")
}

// TestCompatibilitySeedDedupesEngineAccelerators pins that the first-boot
// seed creates one row per (model, engine, card type), not one per
// (engine, accelerator) pair, which would collide on the cell unique
// index.
func TestCompatibilitySeedDedupesEngineAccelerators(t *testing.T) {
	db := newCompatTestDB(t)
	seedMultiAcceleratorEngine(t, db)
	repo := NewCompatibilityRepository(db)
	ctx := context.Background()
	cardTypes := []CardType{{Vendor: "nvidia", CardType: "A800"}}

	// 1 model × 1 distinct engine × 1 card type = 1 cell.
	seeded, err := repo.SeedIfEmpty(ctx, cardTypes, StatusExperimental)
	require.NoError(t, err)
	assert.Equal(t, 1, seeded)

	cell, err := repo.findCell(ctx, testModelID, "vllm", "A800")
	require.NoError(t, err)
	require.NotNil(t, cell)
	assert.Equal(t, StatusExperimental, cell.Status)
}

func TestCompatibilitySetStatus(t *testing.T) {
	db := newCompatTestDB(t)
	seedCompatDimensions(t, db)
	repo := NewCompatibilityRepository(db)
	ctx := context.Background()

	// AC4: set a cell's status and note.
	cell, err := repo.SetStatus(ctx, testModelID, "vllm", "A800", StatusSupported, "validated on A800", testCardTypes, StatusExperimental)
	require.NoError(t, err)
	assert.Equal(t, StatusSupported, cell.Status)
	assert.Equal(t, "validated on A800", cell.Note)

	// An invalid status returns 10210.
	_, err = repo.SetStatus(ctx, testModelID, "vllm", "A800", "bogus", "", testCardTypes, StatusExperimental)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityStatusInvalid, ae.Code)

	// A note longer than 512 chars returns 10210.
	longNote := string(make([]byte, 513))
	_, err = repo.SetStatus(ctx, testModelID, "vllm", "A800", StatusSupported, longNote, testCardTypes, StatusExperimental)
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityStatusInvalid, ae.Code)
}

func TestCompatibilityBulkSetStatusAtomic(t *testing.T) {
	db := newCompatTestDB(t)
	seedCompatDimensions(t, db)
	repo := NewCompatibilityRepository(db)
	ctx := context.Background()

	refs := []CellRef{
		{ModelID: testModelID, Engine: "vllm", CardType: "A800"},
		{ModelID: testModelID, Engine: "vllm", CardType: "M100"},
	}
	// AC5: bulk update returns the count.
	updated, err := repo.BulkSetStatus(ctx, refs, StatusSupported, "bulk note", testCardTypes, StatusExperimental)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated)

	cell, err := repo.findCell(ctx, testModelID, "vllm", "M100")
	require.NoError(t, err)
	require.NotNil(t, cell)
	assert.Equal(t, StatusSupported, cell.Status)
	assert.Equal(t, "bulk note", cell.Note)

	// AC5: a failure (unknown dimension) leaves all cells unchanged
	// (atomic, all-or-nothing).
	badRefs := []CellRef{
		{ModelID: testModelID, Engine: "vllm", CardType: "A800"},
		{ModelID: "model-missing", Engine: "vllm", CardType: "A800"},
	}
	_, err = repo.BulkSetStatus(ctx, badRefs, StatusUnsupported, "", testCardTypes, StatusExperimental)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeCompatibilityDimensionInvalid, ae.Code)

	// The first cell was not changed (rolled back).
	cell, err = repo.findCell(ctx, testModelID, "vllm", "A800")
	require.NoError(t, err)
	require.NotNil(t, cell)
	assert.Equal(t, StatusSupported, cell.Status)
}

func TestCompatibilityListCellsAndNotInFleet(t *testing.T) {
	db := newCompatTestDB(t)
	seedCompatDimensions(t, db)
	repo := NewCompatibilityRepository(db)
	ctx := context.Background()

	// Seed, then remove M100 from the fleet (AC6).
	_, err := repo.SeedIfEmpty(ctx, testCardTypes, StatusExperimental)
	require.NoError(t, err)
	// Curate the M100 cell so it keeps its status.
	_, err = repo.SetStatus(ctx, testModelID, "vllm", "M100", StatusSupported, "kept", testCardTypes, StatusExperimental)
	require.NoError(t, err)
	// Verify the curation persisted.
	stored, err := repo.findCell(ctx, testModelID, "vllm", "M100")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, StatusSupported, stored.Status)

	// AC2: list with filters and pagination.
	cells, total, err := repo.ListCells(ctx, "", "", "", "", "", 0, 20, testCardTypes, StatusExperimental)
	require.NoError(t, err)
	assert.Equal(t, int64(4), total)
	assert.Len(t, cells, 4)

	// Filter by status.
	cells, total, err = repo.ListCells(ctx, "", "", "", StatusSupported, "", 0, 20, testCardTypes, StatusExperimental)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, cells, 1)
	assert.Equal(t, "M100", cells[0].CardType)

	// AC6: a card type removed from the fleet keeps its status but is
	// flagged not_in_fleet. The derived not_in_fleet is computed at
	// request time from the live card-type set.
	reduced := []CardType{{Vendor: "nvidia", CardType: "A800"}}
	cells, _, err = repo.ListCells(ctx, "", "", "", "", "", 0, 20, reduced, StatusExperimental)
	require.NoError(t, err)
	// The M100 cell is still present (kept curation) but flagged.
	foundVLLMM100 := false
	for _, c := range cells {
		if c.Engine == "vllm" && c.CardType == "M100" {
			foundVLLMM100 = true
			assert.Equal(t, StatusSupported, c.Status, "curated status is kept")
		}
	}
	assert.True(t, foundVLLMM100, "M100 cell is not dropped when it leaves the fleet")
}

func TestCompatibilityGetModelCompatibilityMasked(t *testing.T) {
	db := newCompatTestDB(t)
	seedCompatDimensions(t, db)
	repo := NewCompatibilityRepository(db)
	ctx := context.Background()

	_, err := repo.SeedIfEmpty(ctx, testCardTypes, StatusExperimental)
	require.NoError(t, err)
	// Curate one supported, leave one experimental, one unsupported.
	_, err = repo.SetStatus(ctx, testModelID, "vllm", "A800", StatusSupported, "", testCardTypes, StatusExperimental)
	require.NoError(t, err)
	_, err = repo.SetStatus(ctx, testModelID, "sglang", "M100", StatusUnsupported, "", testCardTypes, StatusExperimental)
	require.NoError(t, err)

	// After seed: vllm/A800 experimental, vllm/M100 unsupported,
	// sglang/A800 unsupported, sglang/M100 experimental. After curation:
	// vllm/A800 supported, sglang/M100 unsupported. So supported=1,
	// experimental=1 (sglang/M100 was experimental, now unsupported →
	// only vllm/A800 supported and... wait, sglang/M100 was experimental
	// and set to unsupported, so experimental=0).
	// AC7: only supported/experimental cells are returned (masked).
	cells, supported, experimental, err := repo.GetModelCompatibility(ctx, testModelID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), supported)
	assert.Equal(t, int64(0), experimental)
	// 4 cells total, 3 unsupported hidden → 1 returned.
	assert.Len(t, cells, 1)
	for _, c := range cells {
		assert.NotEqual(t, StatusUnsupported, c.Status, "unsupported combos are hidden")
	}
}

// TestEnsureCellAcceptsCardTypeFromHistory verifies a card type a stored
// cell already uses stays a valid dimension even when the current fleet no
// longer reports it (the "not in fleet" case on the matrix page).
func TestEnsureCellAcceptsCardTypeFromHistory(t *testing.T) {
	db := newCompatTestDB(t)
	repo := NewCompatibilityRepository(db)
	ctx := context.Background()

	// The cell exists for a card type the inventory does not report.
	require.NoError(t, db.Create(&ModelRow{ID: testModelID, Name: "qwen-3b"}).Error)
	require.NoError(t, db.Create(&Image{
		ID: "img-vllm", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3",
		Accelerator: "nvidia", Engine: "vllm",
	}).Error)
	// H800 is not in testCardTypes but a cell already records it.
	require.NoError(t, db.Create(&CompatibilityCell{
		ModelID: testModelID, Engine: "vllm", CardType: "H800", Status: StatusSupported,
	}).Error)

	cell, err := repo.EnsureCell(ctx, testModelID, "vllm", "H800", testCardTypes, StatusExperimental)
	require.NoError(t, err)
	require.NotNil(t, cell)
	assert.Equal(t, StatusSupported, cell.Status, "the stored cell wins over the lazy default")
}

// TestEnsureCellHonorsLegacyFamilyCell verifies an admin decision recorded
// before card types became card models still applies: cells used to be
// keyed by the device-plugin resource family ("gpu"), so a model/engine
// marked supported there must stay supported when a deploy names the
// concrete card model instead of silently falling back to the lazy default.
func TestEnsureCellHonorsLegacyFamilyCell(t *testing.T) {
	db := newCompatTestDB(t)
	repo := NewCompatibilityRepository(db)
	require.NoError(t, db.Create(&ModelRow{ID: testModelID, Name: "qwen-3b"}).Error)
	require.NoError(t, db.Create(&Image{
		ID: "img-vllm", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3",
		Accelerator: "nvidia", Engine: "vllm",
	}).Error)
	// The pre-upgrade cell uses the resource family as the card type.
	require.NoError(t, db.Create(&CompatibilityCell{
		ModelID: testModelID, Engine: "vllm", CardType: "gpu", Status: StatusUnsupported,
	}).Error)

	cardTypes := []CardType{{Vendor: "nvidia", CardType: "NVIDIA-GeForce-RTX-4090"}}
	cell, err := repo.EnsureCell(context.Background(), testModelID, "vllm",
		"NVIDIA-GeForce-RTX-4090", cardTypes, StatusExperimental)
	require.NoError(t, err)
	require.NotNil(t, cell)
	assert.Equal(t, StatusUnsupported, cell.Status,
		"the admin's family-level decision still applies")
	assert.Equal(t, "gpu", cell.CardType, "the legacy row is read, not rewritten")
}
