package account

import (
	"context"
	"time"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// ExportDataProvider aggregates the caller's data for an export (AD4).
// It is implemented by the metering/billing/auth modules and injected at
// wiring time.
type ExportDataProvider interface {
	// ExportRows returns the data rows for an export type and range.
	ExportRows(ctx context.Context, orgID, exportType string, since, until int64) ([]map[string]any, error)
}

// ExportGeneratorRunner picks up pending exports and renders them. It
// implements server.Runner so the framework starts it with the server
// and cancels it on shutdown.
type ExportGeneratorRunner struct {
	svc      *Service
	provider ExportDataProvider
	interval time.Duration
}

// NewExportGeneratorRunner builds an ExportGeneratorRunner.
func NewExportGeneratorRunner(svc *Service, provider ExportDataProvider, interval time.Duration) *ExportGeneratorRunner {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &ExportGeneratorRunner{svc: svc, provider: provider, interval: interval}
}

// NewExportGeneratorRunnerFromConfig builds the runner from the shared
// server components and config.
func NewExportGeneratorRunnerFromConfig(_ server.Components, svc *Service, provider ExportDataProvider) *ExportGeneratorRunner {
	cfg := config.GetConfig()
	return NewExportGeneratorRunner(svc, provider, cfg.Account.Export.GeneratorInterval)
}

// Run implements server.Runner: it ticks on the interval and generates
// pending exports until ctx is cancelled.
func (r *ExportGeneratorRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	// Generate once immediately.
	r.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce picks up pending exports and renders them. It is extracted for
// tests.
func (r *ExportGeneratorRunner) RunOnce(ctx context.Context) {
	repo, err := r.svc.repository()
	if err != nil {
		logger.S().Warnw("account: export generator repository unavailable", "err", err)
		return
	}
	pending, err := repo.ListPendingExports(ctx)
	if err != nil {
		logger.S().Warnw("account: list pending exports failed", "err", err)
		return
	}
	for _, e := range pending {
		r.generate(ctx, repo, &e)
	}
}

// generate renders one pending export and marks it ready or failed.
func (r *ExportGeneratorRunner) generate(ctx context.Context, repo *ExportRepository, e *DataExport) {
	rows, err := r.provider.ExportRows(ctx, e.OrganizationID, e.Type, e.Since, e.Until)
	if err != nil {
		logger.S().Warnw("account: export generation failed", "export_id", e.ID, "err", err)
		_ = repo.UpdateExportStatus(ctx, e.ID, ExportStatusFailed, 0, "", "generation failed")
		return
	}
	var file string
	var rowCount int64
	if e.Format == ExportFormatCSV {
		file, rowCount, err = renderCSV(rows)
	} else {
		file, rowCount, err = renderJSON(e.Type, rows)
	}
	if err != nil {
		logger.S().Warnw("account: export render failed", "export_id", e.ID, "err", err)
		_ = repo.UpdateExportStatus(ctx, e.ID, ExportStatusFailed, 0, "", "render failed")
		return
	}
	if err := repo.UpdateExportStatus(ctx, e.ID, ExportStatusReady, rowCount, file, ""); err != nil {
		logger.S().Warnw("account: export status update failed", "export_id", e.ID, "err", err)
	}
}