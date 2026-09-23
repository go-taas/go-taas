// Package image implements the inference engine image registry service:
// registering engine images, the compatibility catalog and warmup
// (pre-pull) tasks.
package image

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "image"

// DeleteGuard blocks the deletion of an image. It returns a non-nil
// error (10206 CodeImageInUse with a detail naming the blocking
// services) when the image is still referenced.
type DeleteGuard func(ctx context.Context, imageID string) error

// InUseProvider lists the non-terminated inference services referencing
// an image (service_id, name, state). It is injected at wiring time so
// the image module stays free of an infer dependency.
type InUseProvider func(ctx context.Context, imageID string) ([]*imagev1.InUseService, error)

// Service implements the image registry gRPC service.
type Service struct {
	imagev1.UnimplementedImageServiceServer

	components server.Components

	// repo and tasks are the injection points used by tests and FVT;
	// production resolves them lazily from the shared components.
	repo  *Repository
	tasks *WarmupTaskRepository

	// publisher is the optional direct MQ client injection point used
	// by FVT; production resolves the client from the components.
	publisher mq.Client

	deleteGuard DeleteGuard
	inUse       InUseProvider
}

// New constructs the image registry service. The repositories are
// wired lazily on first use from the shared components (the database
// component is initialized by server Init, which runs after service
// construction).
func New(components server.Components) *Service {
	return &Service{components: components}
}

// NewWithRepositories constructs an image service bound directly to
// repositories. It is the injection point used by tests and by any
// embedding that bypasses the shared components.
func NewWithRepositories(repo *Repository, tasks *WarmupTaskRepository) *Service {
	return &Service{repo: repo, tasks: tasks}
}

// SetDeleteGuard installs the delete-image reference guard (D7). It
// must be called before the service starts serving.
func (s *Service) SetDeleteGuard(guard DeleteGuard) {
	s.deleteGuard = guard
}

// SetInUseProvider installs the in-use services provider (GetImage and
// the list in_use_count). It must be called before serving.
func (s *Service) SetInUseProvider(provider InUseProvider) {
	s.inUse = provider
}

// NewForFVT constructs an image service bound to a caller-provided GORM
// database and MQ client. It exists so full-verification tests can
// wire the real service stack against a disposable database and bus.
func NewForFVT(db *gorm.DB, publisher mq.Client) *Service {
	svc := &Service{repo: NewRepository(db), tasks: NewWarmupTaskRepository(db), publisher: publisher}
	wireRegistry(db)
	return svc
}

// MigrateSchemaForFVT applies the image schema (images, warmup_tasks)
// onto a caller-provided database for full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	if err := db.AutoMigrate(&Image{}, &WarmupTask{}); err != nil {
		return err
	}
	return db.Exec(oneActivePartialIndex).Error
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	imagev1.RegisterImageServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return imagev1.RegisterImageServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the images and
// warmup_tasks tables via GORM AutoMigrate, creates the one-active-task
// partial unique index, and runs the first-boot seed from the
// image.registry config (D2: insert-only, never re-runs on a non-empty
// table).
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	if err := db.WithContext(ctx).AutoMigrate(&Image{}, &WarmupTask{}); err != nil {
		return err
	}
	if err := db.WithContext(ctx).Exec(oneActivePartialIndex).Error; err != nil {
		return err
	}

	// First-boot seed: an empty table gets the config entries as rows
	// preserving their imageId values; a non-empty table is never
	// re-seeded (deletions stick, edits win).
	repo, err := s.repository()
	if err != nil {
		return err
	}
	count, err := repo.CountAll(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		logger.S().Infow("image: table non-empty, skipping first-boot seed", "rows", count)
		return nil
	}
	cfg := config.GetConfig()
	if cfg == nil || len(cfg.Image.Registry) == 0 {
		return nil
	}
	if err := repo.SeedFromConfig(ctx, cfg.Image.Registry); err != nil {
		return fmt.Errorf("image: first-boot seed failed: %w", err)
	}
	logger.S().Infow("image: first-boot seed complete", "seeded", len(cfg.Image.Registry))
	return nil
}

// gormDB resolves the *gorm.DB from the wired repositories or the
// shared components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, errors.Newf(errors.CodeInternal, "image: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, errors.Newf(errors.CodeInternal, "image: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the image repository. The first
// resolution also binds the package-level Lookup/List helpers to the
// database (D1: the infer module consumes them unchanged).
func (s *Service) repository() (*Repository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.repo = NewRepository(db)
	wireRegistry(db)
	return s.repo, nil
}

// taskRepository lazily wires and returns the warmup task repository.
func (s *Service) taskRepository() (*WarmupTaskRepository, error) {
	if s.tasks != nil {
		return s.tasks, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.tasks = NewWarmupTaskRepository(db)
	return s.tasks, nil
}

// mqClient resolves the mq.Client from the direct injection point or
// the shared components.
func (s *Service) mqClient() (mq.Client, error) {
	if s.publisher != nil {
		return s.publisher, nil
	}
	if s.components == nil || s.components.MQ() == nil {
		return nil, errors.Newf(errors.CodeInternal, "image: mq component unavailable")
	}
	client, ok := s.components.MQ().Client().(mq.Client)
	if !ok {
		return nil, errors.Newf(errors.CodeInternal, "image: unexpected mq handle type %T", s.components.MQ().Client())
	}
	return client, nil
}

// Field length and syntax limits (architecture Section 4.3).
const (
	maxNameLen        = 255
	maxTagLen         = 128
	maxEngineLen      = 64
	maxDescriptionLen = 1024
)

// namePattern accepts a lowercase repository reference without tag:
// registry host and path segments, no colon, no whitespace.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*[a-z0-9]$|^[a-z0-9]$`)

// tagPattern accepts a non-empty tag without whitespace.
var tagPattern = regexp.MustCompile(`^[^\s]+$`)

// digestPattern accepts sha256 followed by a colon and 64 hex chars.
var digestPattern = regexp.MustCompile(`^sha256:[a-fA-F0-9]{64}$`)

// RegisterImage registers an inference engine image (architecture
// Section 4.3 validation matrix; the first failure returns immediately
// and nothing is written).
func (s *Service) RegisterImage(ctx context.Context, req *imagev1.RegisterImageRequest) (*imagev1.RegisterImageResponse, error) {
	name := strings.TrimSpace(req.GetName())
	tag := strings.TrimSpace(req.GetTag())
	digest := strings.TrimSpace(req.GetDigest())
	accelerator := strings.ToLower(strings.TrimSpace(req.GetAccelerator()))
	engine := strings.TrimSpace(req.GetEngine())
	description := strings.TrimSpace(req.GetDescription())

	if len(name) < 1 || len(name) > maxNameLen || strings.Contains(name, ":") || !namePattern.MatchString(name) {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: name must be a lowercase reference without tag, 1-%d characters", maxNameLen)
	}
	if len(tag) < 1 || len(tag) > maxTagLen || !tagPattern.MatchString(tag) {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: tag must be 1-%d characters without whitespace", maxTagLen)
	}
	if digest != "" && !digestPattern.MatchString(digest) {
		return nil, errors.New(errors.CodeImageDigestInvalid)
	}
	if !IsValidAccelerator(accelerator) {
		return nil, errors.New(errors.CodeImageIncompatible)
	}
	if len(engine) < 1 || len(engine) > maxEngineLen {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: engine must be 1-%d characters", maxEngineLen)
	}
	if len(description) > maxDescriptionLen {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: description must be at most %d characters", maxDescriptionLen)
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := repo.FindByTriple(ctx, name, tag, accelerator); err == nil {
		return nil, errors.New(errors.CodeImageExists)
	}

	img := &Image{
		ID:          uuid.NewString(),
		Name:        name,
		Tag:         tag,
		Digest:      digest,
		Accelerator: accelerator,
		Engine:      engine,
		Description: description,
	}
	if err := repo.CreateImage(ctx, img); err != nil {
		return nil, err
	}
	return &imagev1.RegisterImageResponse{Response: okResponse(), ImageId: img.ID}, nil
}

// ListImages returns registered images, optionally filtered by the
// accelerator and engine they support.
func (s *Service) ListImages(ctx context.Context, req *imagev1.ListImagesRequest) (*imagev1.ListImagesResponse, error) {
	offset, limit := normalizePage(req.GetPage())
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	rows, total, err := repo.ListImages(ctx, req.GetAccelerator(), req.GetEngine(), offset, limit)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	inUseCounts, err := s.inUseCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	tasksRepo, err := s.taskRepository()
	if err != nil {
		return nil, err
	}
	latest, err := tasksRepo.LatestByImageIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	images := make([]*imagev1.ImageSummary, 0, len(rows))
	for _, row := range rows {
		summary := rowToSummary(row)
		summary.InUseCount = inUseCounts[row.ID]
		if task, ok := latest[row.ID]; ok {
			summary.LastWarmupState = task.State
			summary.LastWarmupAt = task.UpdatedAt.Unix()
		}
		images = append(images, summary)
	}
	return &imagev1.ListImagesResponse{
		Response: okResponse(),
		Images:   images,
		//nolint:gosec // G115: limit is capped at 100 by normalizePage.
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: int32(limit)},
	}, nil
}

// GetImage returns one image with the non-terminated inference services
// referencing it.
func (s *Service) GetImage(ctx context.Context, req *imagev1.GetImageRequest) (*imagev1.GetImageResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindByID(ctx, req.GetImageId())
	if err != nil {
		return nil, err
	}

	summary := rowToSummary(row)
	inUse := []*imagev1.InUseService{}
	if s.inUse != nil {
		inUse, err = s.inUse(ctx, row.ID)
		if err != nil {
			return nil, err
		}
	}
	summary.InUseCount = int64(len(inUse))

	tasksRepo, err := s.taskRepository()
	if err != nil {
		return nil, err
	}
	latest, err := tasksRepo.LatestByImageIDs(ctx, []string{row.ID})
	if err != nil {
		return nil, err
	}
	if task, ok := latest[row.ID]; ok {
		summary.LastWarmupState = task.State
		summary.LastWarmupAt = task.UpdatedAt.Unix()
	}

	return &imagev1.GetImageResponse{
		Response:      okResponse(),
		Image:         summary,
		InUseServices: inUse,
	}, nil
}

// UpdateImage edits the description only (D6): the request carries no
// identity or compatibility fields, making immutability structural.
func (s *Service) UpdateImage(ctx context.Context, req *imagev1.UpdateImageRequest) (*imagev1.UpdateImageResponse, error) {
	description := strings.TrimSpace(req.GetDescription())
	if len(description) > maxDescriptionLen {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: description must be at most %d characters", maxDescriptionLen)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := repo.FindByID(ctx, req.GetImageId()); err != nil {
		return nil, err
	}
	if err := repo.UpdateDescription(ctx, req.GetImageId(), description); err != nil {
		return nil, err
	}
	return &imagev1.UpdateImageResponse{Response: okResponse()}, nil
}

// DeleteImage removes a catalog entry, guarded while non-terminated
// inference services reference the image (D7): the guard returns 10206
// naming the blocking services.
func (s *Service) DeleteImage(ctx context.Context, req *imagev1.DeleteImageRequest) (*imagev1.DeleteImageResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := repo.FindByID(ctx, req.GetImageId()); err != nil {
		return nil, err
	}
	if s.deleteGuard != nil {
		if err := s.deleteGuard(ctx, req.GetImageId()); err != nil {
			return nil, err
		}
	}
	if err := repo.Delete(ctx, req.GetImageId()); err != nil {
		return nil, err
	}
	return &imagev1.DeleteImageResponse{Response: okResponse()}, nil
}

// TriggerWarmup enqueues a pre-pull task: it writes the task row
// (state=pending), publishes the dispatch event and returns the
// task_id immediately (D8). A second trigger while a task is active
// returns 10205; the partial unique index is the race-free backstop.
func (s *Service) TriggerWarmup(ctx context.Context, req *imagev1.TriggerWarmupRequest) (*imagev1.TriggerWarmupResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	img, err := repo.FindByID(ctx, req.GetImageId())
	if err != nil {
		return nil, err
	}
	tasksRepo, err := s.taskRepository()
	if err != nil {
		return nil, err
	}
	if active, err := tasksRepo.HasActiveByImageID(ctx, img.ID); err != nil {
		return nil, err
	} else if active {
		return nil, errors.Newf(errors.CodeImageWarmupFailed, "image: a warmup task is already active for this image")
	}

	selector := map[string]string{}
	for k, v := range req.GetNodeSelector() {
		selector[k] = v
	}
	selectorJSON, err := json.Marshal(selector)
	if err != nil {
		return nil, err
	}
	task := &WarmupTask{
		ImageID:      img.ID,
		State:        TaskStatePending,
		NodeSelector: datatypes.JSON(selectorJSON),
	}
	if err := tasksRepo.Create(ctx, task); err != nil {
		return nil, err
	}

	client, clientErr := s.mqClient()
	if clientErr == nil {
		evt := buildWarmupTaskEvent(task, img.Name+":"+img.Tag, selector)
		clientErr = publishWarmupTask(ctx, client, evt)
	}
	if clientErr != nil {
		// Compensate: the task row is marked failed with the publish
		// error as the reason, mirroring infer's publish compensation.
		if markErr := tasksRepo.MarkFailed(ctx, task.ID, "publish failed: "+clientErr.Error()); markErr != nil {
			logger.S().Errorw("image: compensating mark-failed after publish failure failed",
				"task_id", task.ID, "err", markErr)
		}
		return nil, errors.Newf(errors.CodeInternal, "image: publish warmup task failed")
	}
	return &imagev1.TriggerWarmupResponse{Response: okResponse(), TaskId: task.ID}, nil
}

// ListWarmupTasks returns the warmup task history of one image, newest
// first.
func (s *Service) ListWarmupTasks(ctx context.Context, req *imagev1.ListWarmupTasksRequest) (*imagev1.ListWarmupTasksResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := repo.FindByID(ctx, req.GetImageId()); err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	tasksRepo, err := s.taskRepository()
	if err != nil {
		return nil, err
	}
	rows, total, err := tasksRepo.ListByImageID(ctx, req.GetImageId(), offset, limit)
	if err != nil {
		return nil, err
	}
	tasks := make([]*imagev1.WarmupTaskSummary, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, taskToSummary(row))
	}
	return &imagev1.ListWarmupTasksResponse{
		Response: okResponse(),
		Tasks:    tasks,
		//nolint:gosec // G115: limit is capped at 100 by normalizePage.
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: int32(limit)},
	}, nil
}

// GetWarmupTask returns one warmup task with per-node results.
func (s *Service) GetWarmupTask(ctx context.Context, req *imagev1.GetWarmupTaskRequest) (*imagev1.GetWarmupTaskResponse, error) {
	tasksRepo, err := s.taskRepository()
	if err != nil {
		return nil, err
	}
	row, err := tasksRepo.FindByID(ctx, req.GetTaskId())
	if err != nil {
		return nil, err
	}
	return &imagev1.GetWarmupTaskResponse{Response: okResponse(), Task: taskToSummary(row)}, nil
}

// inUseCounts counts non-terminated inference services per image id
// through the injected provider. When no provider is wired (unit
// tests), every image reports zero.
func (s *Service) inUseCounts(ctx context.Context, imageIDs []string) (map[string]int64, error) {
	out := map[string]int64{}
	if s.inUse == nil || len(imageIDs) == 0 {
		return out, nil
	}
	for _, id := range imageIDs {
		services, err := s.inUse(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = int64(len(services))
	}
	return out, nil
}

// rowToSummary maps a DB row to the API summary.
func rowToSummary(row *Image) *imagev1.ImageSummary {
	return &imagev1.ImageSummary{
		ImageId:     row.ID,
		Name:        row.Name,
		Tag:         row.Tag,
		Digest:      row.Digest,
		Accelerator: row.Accelerator,
		Engine:      row.Engine,
		CreatedAt:   row.CreatedAt.Unix(),
		Description: row.Description,
	}
}

// taskToSummary maps a task row to the API summary.
func taskToSummary(row *WarmupTask) *imagev1.WarmupTaskSummary {
	results := []NodeResult{}
	if len(row.NodeResults) > 0 {
		_ = json.Unmarshal(row.NodeResults, &results)
	}
	nodeResults := make([]*imagev1.NodeResult, 0, len(results))
	for _, r := range results {
		nodeResults = append(nodeResults, &imagev1.NodeResult{Node: r.Node, State: r.State, Message: r.Message})
	}
	failureReason := ""
	if row.FailureReason != nil {
		failureReason = *row.FailureReason
	}
	return &imagev1.WarmupTaskSummary{
		TaskId:        row.ID,
		ImageId:       row.ImageID,
		State:         row.State,
		NodeResults:   nodeResults,
		FailureReason: failureReason,
		CreatedAt:     row.CreatedAt.Unix(),
		UpdatedAt:     row.UpdatedAt.Unix(),
	}
}

// normalizePage applies the pagination defaults: limit 20, cap 100,
// negative offset clamped to 0.
func normalizePage(page *commonv1.PageRequest) (offset, limit int) {
	offset = int(page.GetOffset())
	limit = int(page.GetLimit())
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return offset, limit
}

// okResponse builds the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
