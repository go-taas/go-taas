package image

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// maxNoteLen bounds the operator note (AD2, Section 6.3).
const maxNoteLen = 512

// ModelRow is the minimal projection of the models table the matrix
// reads (the image module does not import the model package; it reads
// the shared table directly).
type ModelRow struct {
	ID   string `gorm:"column:id"`
	Name string `gorm:"column:name"`
}

// TableName returns the models table name.
func (ModelRow) TableName() string { return "models" }

// CompatibilityRepository persists compatibility matrix cells on top of
// the generic repository base (feature #19, Section 5.1).
type CompatibilityRepository struct {
	*database.BaseRepository[CompatibilityCell]
	db *database.Manager
}

// NewCompatibilityRepository constructs a CompatibilityRepository bound
// to a database Manager.
func NewCompatibilityRepository(db *gorm.DB) *CompatibilityRepository {
	mgr := database.NewManager(db)
	return &CompatibilityRepository{
		BaseRepository: database.NewBaseRepository[CompatibilityCell](mgr),
		db:             mgr,
	}
}

// CountAll returns the total number of compatibility cells. It gates the
// first-boot seed: a non-empty table is never re-seeded (AD3).
func (r *CompatibilityRepository) CountAll(ctx context.Context) (int64, error) {
	return r.Count(ctx)
}

// listModels returns all catalog models (id + name).
func (r *CompatibilityRepository) listModels(ctx context.Context) ([]ModelRow, error) {
	var rows []ModelRow
	if err := r.DB(ctx).Model(&ModelRow{}).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// listEngines returns the distinct (engine, accelerator) pairs from the
// images table.
func (r *CompatibilityRepository) listEngines(ctx context.Context) ([]EngineDim, error) {
	var rows []EngineDim
	if err := r.DB(ctx).Model(&Image{}).
		Select("DISTINCT engine, accelerator").
		Order("engine ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// EngineDim is one distinct (engine, accelerator) pair from images.
type EngineDim struct {
	Engine      string `gorm:"column:engine"`
	Accelerator string `gorm:"column:accelerator"`
}

// listStoredCells returns all stored cells.
func (r *CompatibilityRepository) listStoredCells(ctx context.Context) ([]*CompatibilityCell, error) {
	var rows []*CompatibilityCell
	if err := r.DB(ctx).Order("model_id ASC, engine ASC, card_type ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// findCell returns the stored cell for the triple, or nil when absent.
func (r *CompatibilityRepository) findCell(ctx context.Context, modelID, engine, cardType string) (*CompatibilityCell, error) {
	var row CompatibilityCell
	err := r.DB(ctx).
		Where("model_id = ? AND engine = ? AND card_type = ?", modelID, engine, cardType).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// defaultStatusFor applies the vendor-match default rule (AD3): a
// vendor-matched combo (engine.accelerator == card_type.vendor) defaults
// to lazyDefault (the configured lazy-seed default, "experimental" by
// default), a vendor-mismatched combo to unsupported.
func defaultStatusFor(engineAccelerator, cardVendor, lazyDefault string) string {
	if engineAccelerator == cardVendor {
		return lazyDefault
	}
	return StatusUnsupported
}

// SeedIfEmpty seeds the compatibility_cells table on first boot when it
// is empty (AD3, Section 4.1). It reads models, distinct engines, and
// card types, and inserts a row per combination with the vendor-match
// default rule. Insert-only, first-boot-only.
func (r *CompatibilityRepository) SeedIfEmpty(ctx context.Context, cardTypes []CardType, lazyDefault string) (int, error) {
	count, err := r.CountAll(ctx)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		return 0, nil
	}
	models, err := r.listModels(ctx)
	if err != nil {
		return 0, err
	}
	engines, err := r.listEngines(ctx)
	if err != nil {
		return 0, err
	}
	if len(models) == 0 || len(engines) == 0 || len(cardTypes) == 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	var cells []*CompatibilityCell
	for _, m := range models {
		for _, e := range engines {
			for _, ct := range cardTypes {
				cells = append(cells, &CompatibilityCell{
					ID:        uuid.NewString(),
					ModelID:   m.ID,
					Engine:    e.Engine,
					CardType:  ct.CardType,
					Status:    defaultStatusFor(e.Accelerator, ct.Vendor, lazyDefault),
					Note:      "",
					CreatedAt: now,
					UpdatedAt: now,
				})
			}
		}
	}
	if err := r.CreateMany(ctx, cells); err != nil {
		return 0, err
	}
	return len(cells), nil
}

// EnsureCell looks up the cell for the triple; if missing, it validates
// the dimensions (10211) and inserts a default row (vendor-match rule).
// It returns the cell (AD3, Section 4.2).
func (r *CompatibilityRepository) EnsureCell(ctx context.Context, modelID, engine, cardType string, cardTypes []CardType, lazyDefault string) (*CompatibilityCell, error) {
	cell, err := r.findCell(ctx, modelID, engine, cardType)
	if err != nil {
		return nil, err
	}
	if cell != nil {
		return cell, nil
	}
	// Validate the dimensions before materializing a default row.
	engineAcc, err := r.validateDimensions(ctx, modelID, engine, cardType, cardTypes)
	if err != nil {
		return nil, err
	}
	cardVendor := cardVendorFor(cardType, cardTypes)
	now := time.Now().UTC()
	cell = &CompatibilityCell{
		ID:        uuid.NewString(),
		ModelID:   modelID,
		Engine:    engine,
		CardType:  cardType,
		Status:    defaultStatusFor(engineAcc, cardVendor, lazyDefault),
		Note:      "",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := r.Create(ctx, cell); err != nil {
		// A concurrent lazy-seed may have created the cell; re-read.
		if existing, reErr := r.findCell(ctx, modelID, engine, cardType); reErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	return cell, nil
}

// validateDimensions checks that modelID, engine, and cardType identify
// known dimensions. It returns the engine's accelerator. An unknown
// dimension returns 10211 (AD8).
func (r *CompatibilityRepository) validateDimensions(ctx context.Context, modelID, engine, cardType string, cardTypes []CardType) (string, error) {
	models, err := r.listModels(ctx)
	if err != nil {
		return "", err
	}
	modelKnown := false
	for _, m := range models {
		if m.ID == modelID {
			modelKnown = true
			break
		}
	}
	if !modelKnown {
		return "", apierrors.New(apierrors.CodeCompatibilityDimensionInvalid)
	}
	engines, err := r.listEngines(ctx)
	if err != nil {
		return "", err
	}
	engineAcc := ""
	engineKnown := false
	for _, e := range engines {
		if e.Engine == engine {
			engineKnown = true
			engineAcc = e.Accelerator
			break
		}
	}
	if !engineKnown {
		return "", apierrors.New(apierrors.CodeCompatibilityDimensionInvalid)
	}
	cardKnown := false
	for _, ct := range cardTypes {
		if ct.CardType == cardType {
			cardKnown = true
			break
		}
	}
	if !cardKnown {
		return "", apierrors.New(apierrors.CodeCompatibilityDimensionInvalid)
	}
	return engineAcc, nil
}

// cardVendorFor returns the vendor of a card type, or "" when unknown.
func cardVendorFor(cardType string, cardTypes []CardType) string {
	for _, ct := range cardTypes {
		if ct.CardType == cardType {
			return ct.Vendor
		}
	}
	return ""
}

// GetCell returns the cell for the triple, materializing a default row
// lazily if missing (AD3). A cell that cannot be materialized (unknown
// dimension) returns 10211; a cell that still cannot be found returns
// 10209 (AD8).
func (r *CompatibilityRepository) GetCell(ctx context.Context, modelID, engine, cardType string, cardTypes []CardType, lazyDefault string) (*CompatibilityCell, error) {
	cell, err := r.EnsureCell(ctx, modelID, engine, cardType, cardTypes, lazyDefault)
	if err != nil {
		return nil, err
	}
	if cell == nil {
		return nil, apierrors.New(apierrors.CodeCompatibilityCellNotFound)
	}
	return cell, nil
}

// SetStatus validates the status (10210) and note length, ensures the
// cell exists (lazy-seed), and upserts status/note/updated_at. It
// returns the cell (AD3, Section 6.3).
func (r *CompatibilityRepository) SetStatus(ctx context.Context, modelID, engine, cardType, status, note string, cardTypes []CardType, lazyDefault string) (*CompatibilityCell, error) {
	if !IsValidCompatibilityStatus(status) {
		return nil, apierrors.New(apierrors.CodeCompatibilityStatusInvalid)
	}
	if len(note) > maxNoteLen {
		return nil, apierrors.New(apierrors.CodeCompatibilityStatusInvalid)
	}
	cell, err := r.EnsureCell(ctx, modelID, engine, cardType, cardTypes, lazyDefault)
	if err != nil {
		return nil, err
	}
	cell.Status = status
	cell.Note = note
	cell.UpdatedAt = time.Now().UTC()
	if err := r.DB(ctx).Model(&CompatibilityCell{}).
		Where("id = ?", cell.ID).
		Updates(map[string]any{"status": status, "note": note, "updated_at": cell.UpdatedAt}).Error; err != nil {
		return nil, err
	}
	return cell, nil
}

// BulkSetStatus validates all refs (10211), the status (10210), and the
// note, then in one transaction ensures and updates each cell. It is
// atomic (all-or-nothing) and returns the count updated (AD5, Section
// 8.3).
func (r *CompatibilityRepository) BulkSetStatus(ctx context.Context, refs []CellRef, status, note string, cardTypes []CardType, lazyDefault string) (int64, error) {
	if !IsValidCompatibilityStatus(status) {
		return 0, apierrors.New(apierrors.CodeCompatibilityStatusInvalid)
	}
	if len(note) > maxNoteLen {
		return 0, apierrors.New(apierrors.CodeCompatibilityStatusInvalid)
	}
	if len(refs) == 0 {
		return 0, nil
	}
	var updated int64
	err := r.db.WithinTx(ctx, func(tx context.Context) error {
		for _, ref := range refs {
			cell, err := r.EnsureCell(tx, ref.ModelID, ref.Engine, ref.CardType, cardTypes, lazyDefault)
			if err != nil {
				return err
			}
			cell.Status = status
			cell.Note = note
			cell.UpdatedAt = time.Now().UTC()
			if err := r.DB(tx).Model(&CompatibilityCell{}).
				Where("id = ?", cell.ID).
				Updates(map[string]any{"status": status, "note": note, "updated_at": cell.UpdatedAt}).Error; err != nil {
				return err
			}
			updated++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}

// CellRef is one (model, engine, card_type) reference in a bulk edit.
type CellRef struct {
	ModelID  string
	Engine   string
	CardType string
}

// ListCells computes the full cross product of current models × engines
// × card types, unions it with stored rows, applies filters/search/
// pagination, and derives not_in_fleet from the live card-type set. It
// returns the cells and the total count before pagination (AD3, Section
// 4.2).
func (r *CompatibilityRepository) ListCells(ctx context.Context, modelID, engine, cardType, status, search string, offset, limit int, cardTypes []CardType, lazyDefault string) ([]*CompatibilityCell, int64, error) {
	models, err := r.listModels(ctx)
	if err != nil {
		return nil, 0, err
	}
	engines, err := r.listEngines(ctx)
	if err != nil {
		return nil, 0, err
	}
	stored, err := r.listStoredCells(ctx)
	if err != nil {
		return nil, 0, err
	}

	// Build the union: stored cells plus derived (not-yet-stored) cells
	// for the current cross product.
	byKey := map[string]*CompatibilityCell{}
	for _, c := range stored {
		byKey[c.ModelID+"\x00"+c.Engine+"\x00"+c.CardType] = c
	}
	modelName := map[string]string{}
	for _, m := range models {
		modelName[m.ID] = m.Name
	}
	engineAcc := map[string]string{}
	for _, e := range engines {
		engineAcc[e.Engine] = e.Accelerator
	}
	cardVendor := map[string]string{}
	for _, ct := range cardTypes {
		cardVendor[ct.CardType] = ct.Vendor
	}
	// Derived cells carry the default status (vendor-match rule).
	for _, m := range models {
		for _, e := range engines {
			for _, ct := range cardTypes {
				key := m.ID + "\x00" + e.Engine + "\x00" + ct.CardType
				if _, ok := byKey[key]; ok {
					continue
				}
				byKey[key] = &CompatibilityCell{
					ModelID:   m.ID,
					Engine:    e.Engine,
					CardType:  ct.CardType,
					Status:    defaultStatusFor(e.Accelerator, ct.Vendor, lazyDefault),
					Note:      "",
					UpdatedAt: time.Time{},
				}
			}
		}
	}

	// Apply filters.
	search = strings.ToLower(strings.TrimSpace(search))
	var matched []*CompatibilityCell
	for _, c := range byKey {
		if modelID != "" && c.ModelID != modelID {
			continue
		}
		if engine != "" && c.Engine != engine {
			continue
		}
		if cardType != "" && c.CardType != cardType {
			continue
		}
		if status != "" && c.Status != status {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(modelName[c.ModelID]), search) {
			continue
		}
		matched = append(matched, c)
	}

	// Stable ordering by model name, engine, card type.
	sort.Slice(matched, func(i, j int) bool {
		if modelName[matched[i].ModelID] != modelName[matched[j].ModelID] {
			return modelName[matched[i].ModelID] < modelName[matched[j].ModelID]
		}
		if matched[i].Engine != matched[j].Engine {
			return matched[i].Engine < matched[j].Engine
		}
		return matched[i].CardType < matched[j].CardType
	})

	total := int64(len(matched))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if offset >= len(matched) {
		return []*CompatibilityCell{}, total, nil
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], total, nil
}

// ListDimensions returns the three axis lists (models, engines, card
// types with vendors and in_fleet) and the per-status counts (AD3,
// Section 6.2). The counts cover the full cross product (stored cells
// unioned with derived cells), consistent with ListCells, so the summary
// strip reflects the current fleet even before the first-boot seed runs.
//
// The card-type axis is the union of the live fleet's card types and the
// card types present in stored cells (AD10): a card type that left the
// fleet keeps its curated cells and must remain a grid column so the
// operator's curation is never silently dropped. Departed card types are
// returned with an empty vendor (their vendor is not stored) and are
// flagged in_fleet: false by the service, which derives the flag from the
// live fleet set.
func (r *CompatibilityRepository) ListDimensions(ctx context.Context, cardTypes []CardType, lazyDefault string) ([]ModelRow, []EngineDim, []CardType, map[string]int64, error) {
	models, err := r.listModels(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	engines, err := r.listEngines(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stored, err := r.listStoredCells(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	counts := map[string]int64{
		StatusSupported:    0,
		StatusExperimental: 0,
		StatusUnsupported:  0,
		"not_in_fleet":     0,
	}
	inFleet := map[string]bool{}
	for _, ct := range cardTypes {
		inFleet[ct.CardType] = true
	}
	// Build the card-type axis as the union of the live fleet's card
	// types and the card types present in stored cells (AD10). Departed
	// card types keep their curated cells and remain grid columns.
	axis := make([]CardType, 0, len(cardTypes))
	seen := map[string]bool{}
	for _, ct := range cardTypes {
		axis = append(axis, ct)
		seen[ct.CardType] = true
	}
	for _, c := range stored {
		if !seen[c.CardType] {
			axis = append(axis, CardType{CardType: c.CardType})
			seen[c.CardType] = true
		}
	}
	sort.Slice(axis, func(i, j int) bool { return axis[i].CardType < axis[j].CardType })

	// Count the union of stored and derived cells so the summary strip
	// matches the grid even when the first-boot seed has not run. A
	// default cell is only derived for card types still in the fleet (a
	// departed card type has no vendor to match); departed card types
	// contribute only their stored cells.
	byKey := map[string]*CompatibilityCell{}
	for _, c := range stored {
		byKey[c.ModelID+"\x00"+c.Engine+"\x00"+c.CardType] = c
	}
	for _, m := range models {
		for _, e := range engines {
			for _, ct := range axis {
				key := m.ID + "\x00" + e.Engine + "\x00" + ct.CardType
				cell, ok := byKey[key]
				if !ok {
					if !inFleet[ct.CardType] {
						continue
					}
					cell = &CompatibilityCell{
						Status:   defaultStatusFor(e.Accelerator, ct.Vendor, lazyDefault),
						CardType: ct.CardType,
					}
				}
				counts[cell.Status]++
				if !inFleet[cell.CardType] {
					counts["not_in_fleet"]++
				}
			}
		}
	}
	return models, engines, axis, counts, nil
}

// GetModelCompatibility returns the masked user-realm projection: the
// model's cells with status ∈ {supported, experimental}, plus the
// supported/experimental counts (AD7, Section 6.3). Unsupported combos
// are hidden.
func (r *CompatibilityRepository) GetModelCompatibility(ctx context.Context, modelID string) ([]*CompatibilityCell, int64, int64, error) {
	var rows []*CompatibilityCell
	if err := r.DB(ctx).
		Where("model_id = ? AND status IN ?", modelID, []string{StatusSupported, StatusExperimental}).
		Order("engine ASC, card_type ASC").
		Find(&rows).Error; err != nil {
		return nil, 0, 0, err
	}
	var supported, experimental int64
	for _, c := range rows {
		if c.Status == StatusSupported {
			supported++
		} else {
			experimental++
		}
	}
	return rows, supported, experimental, nil
}
