package account

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// ExportRepository persists data-export jobs.
type ExportRepository struct {
	db *database.Manager
}

// NewExportRepository constructs an ExportRepository bound to a GORM
// database.
func NewExportRepository(db *gorm.DB) *ExportRepository {
	return &ExportRepository{db: database.NewManager(db)}
}

// CreateExport inserts a new export row.
func (r *ExportRepository) CreateExport(ctx context.Context, e *DataExport) error {
	return r.db.DB(ctx).Create(e).Error
}

// FindExportByID returns one export by id. A miss maps to
// CodeDataExportNotFound.
func (r *ExportRepository) FindExportByID(ctx context.Context, exportID string) (*DataExport, error) {
	if _, err := uuid.Parse(exportID); err != nil {
		return nil, apierrors.New(apierrors.CodeDataExportNotFound)
	}
	var row DataExport
	err := r.db.DB(ctx).Where("id = ?", exportID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeDataExportNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListExports returns the exports of an org, newest first.
func (r *ExportRepository) ListExports(ctx context.Context, orgID string) ([]DataExport, error) {
	var rows []DataExport
	err := r.db.DB(ctx).Where("organization_id = ?", orgID).Order("created_at DESC").Find(&rows).Error
	return rows, err
}

// ListPendingExports returns the pending exports (for the generator).
func (r *ExportRepository) ListPendingExports(ctx context.Context) ([]DataExport, error) {
	var rows []DataExport
	err := r.db.DB(ctx).Where("status = ?", ExportStatusPending).Order("created_at ASC").Find(&rows).Error
	return rows, err
}

// UpdateExportStatus sets an export's status, row count, file, and error
// in one transaction.
func (r *ExportRepository) UpdateExportStatus(ctx context.Context, exportID, status string, rowCount int64, file, errMsg string) error {
	updates := map[string]any{"status": status, "row_count": rowCount}
	if file != "" {
		updates["file"] = file
	}
	if errMsg != "" {
		updates["error"] = errMsg
	}
	return r.db.DB(ctx).Model(&DataExport{}).Where("id = ?", exportID).Updates(updates).Error
}