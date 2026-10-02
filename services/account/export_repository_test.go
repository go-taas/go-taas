package account

import (
	"context"
	"testing"

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
	require.NoError(t, db.AutoMigrate(&DataExport{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestExportRepository(t *testing.T) {
	db := newTestDB(t)
	repo := NewExportRepository(db)
	exportID := "11111111-1111-1111-1111-111111111111"

	// AC1: CreateExport + ListExports.
	e := &DataExport{ID: exportID, OrganizationID: "org-1", Type: ExportTypeUsage, Format: ExportFormatJSON, Status: ExportStatusPending}
	require.NoError(t, repo.CreateExport(context.Background(), e))
	rows, err := repo.ListExports(context.Background(), "org-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, ExportTypeUsage, rows[0].Type)

	// AC2: FindExportByID.
	found, err := repo.FindExportByID(context.Background(), exportID)
	require.NoError(t, err)
	assert.Equal(t, ExportStatusPending, found.Status)

	// AC2: unknown export -> 12501.
	_, err = repo.FindExportByID(context.Background(), "missing")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeDataExportNotFound, apierrors.CodeOf(err))

	// AC3: UpdateExportStatus.
	require.NoError(t, repo.UpdateExportStatus(context.Background(), exportID, ExportStatusReady, 3, `{"usage":[]}`, ""))
	found, err = repo.FindExportByID(context.Background(), exportID)
	require.NoError(t, err)
	assert.Equal(t, ExportStatusReady, found.Status)
	assert.Equal(t, int64(3), found.RowCount)
	assert.Equal(t, `{"usage":[]}`, found.File)
}

func TestValidExportType(t *testing.T) {
	assert.True(t, validExportType(ExportTypeUsage))
	assert.True(t, validExportType(ExportTypeBilling))
	assert.True(t, validExportType(ExportTypeRequestLogs))
	assert.True(t, validExportType(ExportTypeAccount))
	assert.False(t, validExportType("bogus"))
}

func TestValidExportFormat(t *testing.T) {
	assert.True(t, validExportFormat(ExportTypeUsage, ExportFormatJSON))
	assert.True(t, validExportFormat(ExportTypeUsage, ExportFormatCSV))
	// Account is always JSON.
	assert.True(t, validExportFormat(ExportTypeAccount, ExportFormatJSON))
	assert.False(t, validExportFormat(ExportTypeAccount, ExportFormatCSV))
}

func TestRenderJSON(t *testing.T) {
	rows := []map[string]any{{"id": "1", "tokens": 100}}
	file, count, err := renderJSON(ExportTypeUsage, rows)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	assert.Contains(t, file, `"usage"`)
}

func TestRenderCSV(t *testing.T) {
	rows := []map[string]any{{"id": "1", "tokens": 100}}
	file, count, err := renderCSV(rows)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	// UTF-8 BOM prefix.
	assert.Contains(t, file, "\ufeff")
	assert.Contains(t, file, "id")
}