package image

import (
	"context"
	"time"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/registry"
)

// ImportWorker picks up pending image import tasks and copies each
// image from its source registry into the internal Harbor project, then
// registers the catalog row. It implements server.Runner so the
// framework starts it with the server and cancels it on shutdown.
type ImportWorker struct {
	tasks      *ImportTaskRepository
	images     *Repository
	importer   Importer
	concurrency int
	interval   time.Duration
}

// NewImportWorker constructs an ImportWorker.
func NewImportWorker(tasks *ImportTaskRepository, images *Repository, importer Importer, concurrency int, interval time.Duration) *ImportWorker {
	if concurrency <= 0 {
		concurrency = 1
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &ImportWorker{
		tasks:       tasks,
		images:      images,
		importer:    importer,
		concurrency: concurrency,
		interval:    interval,
	}
}

// RunnerName implements server.Runner.
func (w *ImportWorker) RunnerName() string { return "image-import-worker" }

// Run implements server.Runner: it ticks on the interval and processes
// pending import tasks until ctx is cancelled.
func (w *ImportWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce processes up to concurrency pending import tasks. It is
// extracted for tests.
func (w *ImportWorker) RunOnce(ctx context.Context) {
	for i := 0; i < w.concurrency; i++ {
		task, err := w.tasks.NextPending(ctx)
		if err != nil {
			logger.S().Warnw("image: next pending import task failed", "err", err)
			return
		}
		if task == nil {
			return
		}
		w.processTask(ctx, task)
	}
}

// processTask copies the image and registers the catalog row, updating
// the task state on success or failure.
func (w *ImportWorker) processTask(ctx context.Context, task *ImportTask) {
	cfg := config.GetConfig()
	if cfg == nil || cfg.Image.Harbor.URL == "" {
		_ = w.tasks.ApplyFailure(ctx, task.ID, "image: internal Harbor registry is not configured")
		return
	}
	if w.importer == nil {
		_ = w.tasks.ApplyFailure(ctx, task.ID, "image: image import is not configured")
		return
	}

	var srcCreds *registry.Credentials
	if task.SourceUsername != "" {
		srcCreds = &registry.Credentials{Username: task.SourceUsername, Password: task.SourcePassword}
	}
	dstCreds := &registry.Credentials{Username: cfg.Image.Harbor.Username, Password: cfg.Image.Harbor.Password}

	if err := w.importer.Import(ctx, task.SourceReference, srcCreds, task.DestinationReference, dstCreds); err != nil {
		_ = w.tasks.ApplyFailure(ctx, task.ID, err.Error())
		return
	}

	_, name, tag, err := deriveImportTarget(cfg.Image.Harbor, task.SourceReference)
	if err != nil {
		_ = w.tasks.ApplyFailure(ctx, task.ID, err.Error())
		return
	}
	img := &Image{
		Name:        name,
		Tag:         tag,
		Accelerator: task.Accelerator,
		Engine:      task.Engine,
		Description: task.Description,
	}
	if err := w.images.CreateImage(ctx, img); err != nil {
		_ = w.tasks.ApplyFailure(ctx, task.ID, err.Error())
		return
	}
	_ = w.tasks.ApplySuccess(ctx, task.ID, task.DestinationReference, img.ID)
}