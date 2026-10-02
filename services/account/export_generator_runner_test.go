package account

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	accountv1 "github.com/go-taas/go-taas/proto/taas/account/v1"
)

// fakeExportDataProvider returns fixed rows for an export.
type fakeExportDataProvider struct {
	rows []map[string]any
	err  error
}

func (f fakeExportDataProvider) ExportRows(_ context.Context, _, _ string, _, _ int64) ([]map[string]any, error) {
	return f.rows, f.err
}

func TestGeneratorRunOnce(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	provider := fakeExportDataProvider{rows: []map[string]any{{"id": "1", "tokens": 100}}}
	runner := NewExportGeneratorRunner(svc, provider, 0)

	// Create a pending export.
	resp, err := svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: ExportTypeUsage, Format: ExportFormatJSON,
	})
	require.NoError(t, err)

	// AC1: RunOnce renders it and marks ready.
	runner.RunOnce(context.Background())
	got, err := svc.GetDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.GetDataExportRequest{ExportId: resp.Export.ExportId})
	require.NoError(t, err)
	assert.Equal(t, ExportStatusReady, got.Export.Status)
	assert.Equal(t, int64(1), got.Export.RowCount)
}

func TestGeneratorRunOnceFailed(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	provider := fakeExportDataProvider{err: assert.AnError}
	runner := NewExportGeneratorRunner(svc, provider, 0)

	// Create a pending export.
	resp, err := svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: ExportTypeUsage, Format: ExportFormatJSON,
	})
	require.NoError(t, err)

	// AC1: a provider error marks the export failed.
	runner.RunOnce(context.Background())
	got, err := svc.GetDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.GetDataExportRequest{ExportId: resp.Export.ExportId})
	require.NoError(t, err)
	assert.Equal(t, ExportStatusFailed, got.Export.Status)
}

func TestGeneratorCSV(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	provider := fakeExportDataProvider{rows: []map[string]any{{"id": "1", "tokens": 100}}}
	runner := NewExportGeneratorRunner(svc, provider, 0)

	// Create a CSV export.
	resp, err := svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: ExportTypeUsage, Format: ExportFormatCSV,
	})
	require.NoError(t, err)

	runner.RunOnce(context.Background())
	dl, err := svc.DownloadDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.DownloadDataExportRequest{ExportId: resp.Export.ExportId})
	require.NoError(t, err)
	assert.Equal(t, "text/csv", dl.ContentType)
	assert.Contains(t, dl.File, "\ufeff")
}

// TestGeneratorRun verifies the runner's Run loop generates pending
// exports on the interval.
func TestGeneratorRun(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	provider := fakeExportDataProvider{rows: []map[string]any{{"id": "1"}}}
	runner := NewExportGeneratorRunner(svc, provider, 10*time.Millisecond)

	// Create a pending export.
	resp, err := svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: ExportTypeUsage, Format: ExportFormatJSON,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// The runner generates the export.
	require.Eventually(t, func() bool {
		got, err := svc.GetDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.GetDataExportRequest{ExportId: resp.Export.ExportId})
		return err == nil && got.Export.Status == ExportStatusReady
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}