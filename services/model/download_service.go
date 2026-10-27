package model

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/modelhub"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	"github.com/go-taas/go-taas/services/audit"
)

// downloadWorkers bounds the concurrent hub downloads of one server
// instance: a download saturates the hub connection and the JuiceFS
// mount, so running many at once only slows every job down.
const downloadWorkers = 2

// downloadProgressInterval is how often the on-disk size of a running
// download is sampled for the progress column.
const downloadProgressInterval = time.Second

// StartModelDownload queues a hub download and registers the model when
// the weights land. It returns immediately with the queued job; the
// console polls ListModelDownloads for progress.
func (s *Service) StartModelDownload(ctx context.Context, req *modelv1.StartModelDownloadRequest) (*modelv1.StartModelDownloadResponse, error) {
	source := strings.TrimSpace(req.GetSource())
	hubModelID := strings.TrimSpace(req.GetHubModelId())
	if source == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: source is required (modelscope or huggingface)")
	}
	if _, err := modelhub.ParseSource(source); err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	if hubModelID == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: hub_model_id is required")
	}
	if err := validateHubModelID(hubModelID); err != nil {
		return nil, err
	}
	if s.downloader == nil || s.weightsDir == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: model download is not configured")
	}

	name := strings.TrimSpace(req.GetName())
	if name == "" {
		name = defaultModelName(hubModelID)
	}
	if len(name) > 128 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: name must be 1-128 characters")
	}
	version := strings.TrimSpace(req.GetVersion())
	if version == "" {
		version = defaultModelVersion(hubModelID)
	}
	if len(version) > 64 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: version must be 1-64 characters")
	}
	description := strings.TrimSpace(req.GetDescription())
	if len(description) > 1024 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: description must be at most 1024 characters")
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	// Reject an import that can never register, before moving any bytes:
	// the hub id is the model's globally unique identity.
	if err := s.checkHubBinding(ctx, repo, name, hubModelID); err != nil {
		return nil, err
	}

	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	job := &DownloadJob{
		ID:          uuid.NewString(),
		Source:      string(source),
		HubModelID:  hubModelID,
		Name:        name,
		Version:     version,
		Description: description,
		State:       DownloadStateQueued,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := db.WithContext(ctx).Create(job).Error; err != nil {
		return nil, err
	}
	// The download must outlive the request that queued it, so the job
	// runs with its own context rather than the request-scoped one.
	//
	//nolint:gosec // G118: a detached job intentionally ignores the request context
	go s.runDownloadJob(job)

	return &modelv1.StartModelDownloadResponse{
		Response: okResponse(),
		Download: downloadProjection(job),
	}, nil
}

// ListModelDownloads returns one page of download jobs, newest first.
func (s *Service) ListModelDownloads(ctx context.Context, req *modelv1.ListModelDownloadsRequest) (*modelv1.ListModelDownloadsResponse, error) {
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	var (
		rows  []*DownloadJob
		total int64
	)
	query := db.WithContext(ctx).Model(&DownloadJob{})
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	if err := query.Order("created_at DESC").Order("id DESC").
		Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	downloads := make([]*modelv1.ModelDownload, 0, len(rows))
	for _, row := range rows {
		downloads = append(downloads, downloadProjection(row))
	}
	return &modelv1.ListModelDownloadsResponse{
		Response:  okResponse(),
		Downloads: downloads,
		PageMeta:  &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// runDownloadJob performs one download and registers the model version.
// It runs on its own goroutine with a background context: the job must
// outlive the HTTP request that queued it.
func (s *Service) runDownloadJob(job *DownloadJob) {
	s.downloadSemaphore() <- struct{}{}
	defer func() { <-s.downloadSemaphore() }()

	ctx := context.Background()
	destDir := modelhub.JoinPath(s.weightsDir, job.HubModelID)
	s.updateDownload(ctx, job.ID, map[string]any{
		"state":      DownloadStateDownloading,
		"message":    "",
		"updated_at": time.Now().UTC(),
	})

	// Learn the model's size first so the console can show a percentage.
	// A hub that reports no sizes leaves the total at zero and the console
	// falls back to an indeterminate bar.
	if total := s.measureDownloadTotal(ctx, job); total > 0 {
		s.updateDownload(ctx, job.ID, map[string]any{
			"total_bytes": total,
			"updated_at":  time.Now().UTC(),
		})
	}

	stop := make(chan struct{})
	trackerDone := make(chan struct{})
	go func() {
		defer close(trackerDone)
		s.trackDownloadProgress(job.ID, destDir, stop)
	}()

	parsedSource, err := modelhub.ParseSource(job.Source)
	if err == nil {
		err = s.downloader.Download(ctx, parsedSource, job.HubModelID, destDir)
	}
	close(stop)
	<-trackerDone

	bytes := dirSize(destDir)
	if err != nil {
		s.updateDownload(ctx, job.ID, map[string]any{
			"state":      DownloadStateFailed,
			"message":    truncateMessage(err.Error()),
			"bytes":      bytes,
			"updated_at": time.Now().UTC(),
		})
		return
	}

	repo, repoErr := s.repository()
	if repoErr != nil {
		s.updateDownload(ctx, job.ID, map[string]any{
			"state": DownloadStateFailed, "message": truncateMessage(repoErr.Error()),
			"bytes": bytes, "updated_at": time.Now().UTC(),
		})
		return
	}
	weightPath := modelhub.CleanModelID(job.HubModelID) + "/"
	modelID, regErr := repo.RegisterModelOrCreateVersion(ctx, job.Name, job.Description, job.HubModelID, job.Version, weightPath)
	if regErr != nil {
		s.updateDownload(ctx, job.ID, map[string]any{
			"state": DownloadStateFailed, "message": truncateMessage(regErr.Error()),
			"bytes": bytes, "updated_at": time.Now().UTC(),
		})
		return
	}
	s.updateDownload(ctx, job.ID, map[string]any{
		"state":       DownloadStateSucceeded,
		"message":     "",
		"bytes":       bytes,
		"weight_path": weightPath,
		"model_id":    modelID,
		"updated_at":  time.Now().UTC(),
	})
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  s.grantedBy(ctx),
		ActorType:    "user",
		Action:       "model.download",
		ResourceType: "model",
		ResourceID:   modelID,
		Result:       "success",
	})
}

// measureDownloadTotal asks the downloader for the model's total size. It
// is best-effort: a hub that cannot be queried, or a downloader that
// cannot report sizes, leaves the total unknown.
func (s *Service) measureDownloadTotal(ctx context.Context, job *DownloadJob) int64 {
	reporter, ok := s.downloader.(modelhub.SizeReporter)
	if !ok {
		return 0
	}
	source, err := modelhub.ParseSource(job.Source)
	if err != nil {
		return 0
	}
	total, err := reporter.TotalBytes(ctx, source, job.HubModelID)
	if err != nil || total < 0 {
		return 0
	}
	return total
}

// trackDownloadProgress samples the download directory size until stop
// is closed, so the console can show how many bytes already landed.
func (s *Service) trackDownloadProgress(jobID, dir string, stop <-chan struct{}) {
	ticker := time.NewTicker(downloadProgressInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.updateDownload(context.Background(), jobID, map[string]any{
				"bytes":      dirSize(dir),
				"updated_at": time.Now().UTC(),
			})
		}
	}
}

// updateDownload applies one best-effort update to a job row. A failed
// update never fails the job: the next sample or the final write covers
// it.
func (s *Service) updateDownload(ctx context.Context, jobID string, fields map[string]any) {
	db, err := s.gormDB()
	if err != nil {
		return
	}
	// A terminal state must land even when a progress sample is writing
	// at the same time: retry a few times before giving up.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = db.WithContext(ctx).Model(&DownloadJob{}).Where("id = ?", jobID).
			Updates(fields).Error
		if lastErr == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	logger.S().Warnw("model: download job update failed", "job_id", jobID, "err", lastErr)
}

// downloadSemaphore lazily creates the per-service concurrency limiter.
func (s *Service) downloadSemaphore() chan struct{} {
	s.downloadSemOnce.Do(func() {
		s.downloadSem = make(chan struct{}, downloadWorkers)
	})
	return s.downloadSem
}

// checkHubBinding rejects an import whose hub id is already bound to a
// different model, or whose model name is bound to a different hub id.
func (s *Service) checkHubBinding(ctx context.Context, repo *Repository, name, hubModelID string) error {
	bound, err := repo.FindByHubModelID(ctx, hubModelID)
	if err != nil && apierrors.CodeOf(err) != apierrors.CodeModelNotFound {
		return err
	}
	if err == nil && bound.Name != name {
		return apierrors.Newf(apierrors.CodeModelExists,
			"model: hub model id %q is already bound to model %q", hubModelID, bound.Name)
	}
	byName, err := repo.FindByName(ctx, name)
	if err != nil {
		if apierrors.CodeOf(err) == apierrors.CodeModelNotFound {
			return nil
		}
		return err
	}
	if byName.HubModelID != "" && byName.HubModelID != hubModelID {
		return apierrors.Newf(apierrors.CodeModelExists,
			"model: model %q is already bound to hub model id %q", name, byName.HubModelID)
	}
	return nil
}

// RecoverInterruptedDownloads marks jobs a previous process left running
// as failed: their goroutine is gone, so they would otherwise stay
// "downloading" forever.
func (s *Service) RecoverInterruptedDownloads(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&DownloadJob{}).
		Where("state IN ?", []string{DownloadStateQueued, DownloadStateDownloading}).
		Updates(map[string]any{
			"state":      DownloadStateFailed,
			"message":    "download interrupted by a server restart",
			"updated_at": time.Now().UTC(),
		}).Error
}

// downloadProjection maps a job row to its API projection.
func downloadProjection(job *DownloadJob) *modelv1.ModelDownload {
	return &modelv1.ModelDownload{
		DownloadId: job.ID,
		Source:     job.Source,
		HubModelId: job.HubModelID,
		Name:       job.Name,
		Version:    job.Version,
		State:      job.State,
		Message:    job.Message,
		Bytes:      job.Bytes,
		TotalBytes: job.TotalBytes,
		WeightPath: job.WeightPath,
		ModelId:    job.ModelID,
		CreatedAt:  job.CreatedAt.Unix(),
		UpdatedAt:  job.UpdatedAt.Unix(),
	}
}

// defaultModelName derives the catalog name from the hub id's last
// segment: "MiniMax/MiniMax-M3.1-Flash" → "MiniMax-M3.1-Flash".
func defaultModelName(hubModelID string) string {
	parts := strings.Split(hubModelID, "/")
	return parts[len(parts)-1]
}

// defaultModelVersion derives the initial version from the hub id's last
// segment.
func defaultModelVersion(hubModelID string) string {
	return defaultModelName(hubModelID)
}

// truncateMessage keeps an error message within the column size.
func truncateMessage(msg string) string {
	const maxLen = 1000
	if len(msg) <= maxLen {
		return msg
	}
	return msg[:maxLen]
}

// dirSize sums the size of the regular files under dir. A missing
// directory is size 0.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, infoErr := d.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
