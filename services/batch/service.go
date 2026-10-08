// Package batch implements the batch inference service (feature #42).
package batch

import (
	"context"
	"math"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	batchv1 "github.com/go-taas/go-taas/proto/taas/batch/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "batch"

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization.
const organizationMetadataKey = "x-organization-id"

// Pagination bounds (§5.2).
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// SessionOrgResolver resolves the session's active organization.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the admin batch RPCs.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleMember is the minimum org role for the admin batch RPCs.
const roleMember = tenancy.RoleMember

// AuditRecorder is the best-effort audit recorder seam (AD9).
type AuditRecorder interface {
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// Service implements the batch gRPC service.
type Service struct {
	batchv1.UnimplementedBatchServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT.
	repo *Repository
	// store is the file store.
	store FileStore

	// sessionOrgResolver resolves the session's active organization.
	sessionOrgResolver SessionOrgResolver
	// sessionUserResolver resolves the caller's user id.
	sessionUserResolver SessionUserResolver
	// roleGuard gates the admin batch RPCs.
	roleGuard RoleGuard
	// auditRecorder records batch actions best-effort (AD9).
	auditRecorder AuditRecorder
}

// New constructs the batch service.
func New(components server.Components) *Service {
	return &Service{
		components: components,
	}
}

// SetSessionOrgResolver injects the session-organization resolver.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver.
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard.
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetAuditRecorder injects the best-effort audit recorder (AD9).
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// NewForFVT constructs a batch service bound to a caller-provided GORM
// database and file store.
func NewForFVT(db *gorm.DB, store FileStore) *Service {
	return &Service{
		repo:  NewRepository(db),
		store: store,
	}
}

// MigrateSchemaForFVT applies the batch schema onto a caller-provided
// database.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&BatchJob{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	batchv1.RegisterBatchServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return batchv1.RegisterBatchServiceHandler
}

// Migrate implements server.Migrator.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&BatchJob{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "batch: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "batch: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the batch repository.
func (s *Service) repository() (*Repository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.repo = NewRepository(db)
	return s.repo, nil
}

// fileStore lazily wires and returns the file store.
func (s *Service) fileStore() FileStore {
	if s.store != nil {
		return s.store
	}
	dir := "/data/batch"
	if cfg := config.GetConfig(); cfg != nil && cfg.Batch.StoreDir != "" {
		dir = cfg.Batch.StoreDir
	}
	s.store = NewLocalFileStore(dir)
	return s.store
}

// resolveOrg returns the organization context for a read.
func (s *Service) resolveOrg(ctx context.Context) (string, error) {
	if s.sessionOrgResolver != nil {
		if org, err := s.sessionOrgResolver.SessionActiveOrg(ctx); err != nil {
			return "", err
		} else if org != "" {
			return org, nil
		}
	}
	return resolveOrganizationID(ctx)
}

func resolveOrganizationID(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	values := md.Get(organizationMetadataKey)
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	return strings.TrimSpace(values[0]), nil
}

func (s *Service) resolveSessionActor(ctx context.Context) (string, bool, error) {
	if s.sessionUserResolver == nil {
		return "", false, nil
	}
	userID, err := s.sessionUserResolver.SessionUserID(ctx)
	if err == nil && userID != "" {
		return userID, true, nil
	}
	if err != nil && apierrors.CodeOf(err) != apierrors.CodeSessionInvalid {
		return "", false, err
	}
	return "", false, nil
}

func (s *Service) requireAdminRole(ctx context.Context, orgID string) error {
	if s.roleGuard == nil || orgID == "" {
		return nil
	}
	userID, hasSession, err := s.resolveSessionActor(ctx)
	if err != nil {
		return err
	}
	if !hasSession {
		return nil
	}
	return s.roleGuard.RequireRole(ctx, orgID, userID, roleMember)
}

func (s *Service) recordAudit(ctx context.Context, action, orgID, batchID string) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         action,
		ResourceType:   "batch_job",
		ResourceID:     batchID,
		Result:         "success",
	})
}

// CreateBatchJob creates a batch job from an uploaded JSONL input file
// (AC1).
func (s *Service) CreateBatchJob(ctx context.Context, req *batchv1.CreateBatchJobRequest) (*batchv1.CreateBatchJobResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	window, err := validateCompletionWindow(req.GetCompletionWindow())
	if err != nil {
		return nil, err
	}
	cfg := config.GetConfig()
	maxBytes := int64(524288000)
	maxLines := 50000
	if cfg != nil {
		if cfg.Batch.MaxFileBytes > 0 {
			maxBytes = cfg.Batch.MaxFileBytes
		}
		if cfg.Batch.MaxLines > 0 {
			maxLines = cfg.Batch.MaxLines
		}
	}
	requests, model, err := ValidateJSONL(req.GetInputFile(), maxBytes, maxLines)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	store := s.fileStore()
	job := &BatchJob{
		OrganizationID:   orgID,
		Model:            model,
		Status:           StatusValidating,
		CompletionWindow: window,
		TotalRequests:    int64(len(requests)),
		Currency:         "USD",
		CreatedAt:        time.Now().UTC(),
	}
	batchID, err := repo.InsertBatchJob(ctx, job)
	if err != nil {
		return nil, err
	}
	key, err := store.PutInput(ctx, orgID, batchID, req.GetInputFile())
	if err != nil {
		return nil, err
	}
	job.InputFileKey = key
	_ = repo.UpdateBatchJob(ctx, job)
	s.recordAudit(ctx, "batch.created", orgID, batchID)
	return &batchv1.CreateBatchJobResponse{
		Response: okResponse(),
		BatchJob: batchJobToProto(job),
	}, nil
}

// ListBatchJobs returns the caller's batch jobs (AC2).
func (s *Service) ListBatchJobs(ctx context.Context, req *batchv1.ListBatchJobsRequest) (*batchv1.ListBatchJobsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := pageBounds(req.GetPage())
	rows, total, err := repo.ListBatchJobs(ctx, BatchFilter{
		OrganizationID: orgID,
		Status:         req.GetStatus(),
		Model:          req.GetModel(),
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]*batchv1.BatchJob, 0, len(rows))
	for _, r := range rows {
		jobs = append(jobs, batchJobToProto(r))
	}
	return &batchv1.ListBatchJobsResponse{
		Response:  okResponse(),
		BatchJobs: jobs,
		PageMeta:  pageMeta(offset, limit, total),
	}, nil
}

// GetBatchJob returns a job's status and stats (AC2).
func (s *Service) GetBatchJob(ctx context.Context, req *batchv1.GetBatchJobRequest) (*batchv1.GetBatchJobResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	job, err := repo.FindBatchJobByID(ctx, orgID, strings.TrimSpace(req.GetBatchId()))
	if err != nil {
		return nil, err
	}
	return &batchv1.GetBatchJobResponse{
		Response: okResponse(),
		BatchJob: batchJobToProto(job),
	}, nil
}

// CancelBatchJob cancels a job in validating or in_progress (AC3).
func (s *Service) CancelBatchJob(ctx context.Context, req *batchv1.CancelBatchJobRequest) (*batchv1.CancelBatchJobResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	job, err := repo.FindBatchJobByID(ctx, orgID, strings.TrimSpace(req.GetBatchId()))
	if err != nil {
		return nil, err
	}
	if !cancellable(job.Status) {
		return nil, apierrors.New(apierrors.CodeBatchJobStateInvalid)
	}
	now := time.Now().UTC()
	if err := repo.SetStatus(ctx, job.BatchID, StatusCancelled, &now, nil); err != nil {
		return nil, err
	}
	job.Status = StatusCancelled
	job.CompletedAt = &now
	s.recordAudit(ctx, "batch.cancelled", orgID, job.BatchID)
	return &batchv1.CancelBatchJobResponse{
		Response: okResponse(),
		BatchJob: batchJobToProto(job),
	}, nil
}

// DownloadBatchResult returns the result file when completed (AC4).
func (s *Service) DownloadBatchResult(ctx context.Context, req *batchv1.DownloadBatchResultRequest) (*batchv1.DownloadBatchResultResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	job, err := repo.FindBatchJobByID(ctx, orgID, strings.TrimSpace(req.GetBatchId()))
	if err != nil {
		return nil, err
	}
	if job.Status != StatusCompleted || job.ResultFileKey == "" {
		return nil, apierrors.New(apierrors.CodeBatchNotCompleted)
	}
	content, err := s.fileStore().GetFile(ctx, job.ResultFileKey)
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "batch.downloaded", orgID, job.BatchID)
	return &batchv1.DownloadBatchResultResponse{
		Response: okResponse(),
		Content:  content,
		Filename: "batch-" + job.BatchID + "-result.jsonl",
	}, nil
}

// DownloadBatchError returns the error file when completed (AC4).
func (s *Service) DownloadBatchError(ctx context.Context, req *batchv1.DownloadBatchErrorRequest) (*batchv1.DownloadBatchErrorResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	job, err := repo.FindBatchJobByID(ctx, orgID, strings.TrimSpace(req.GetBatchId()))
	if err != nil {
		return nil, err
	}
	if job.Status != StatusCompleted || job.ErrorFileKey == "" {
		return nil, apierrors.New(apierrors.CodeBatchNotCompleted)
	}
	content, err := s.fileStore().GetFile(ctx, job.ErrorFileKey)
	if err != nil {
		return nil, err
	}
	return &batchv1.DownloadBatchErrorResponse{
		Response: okResponse(),
		Content:  content,
		Filename: "batch-" + job.BatchID + "-error.jsonl",
	}, nil
}

// AdminListBatchJobs lists all jobs across tenants, masked (AC10).
func (s *Service) AdminListBatchJobs(ctx context.Context, req *batchv1.AdminListBatchJobsRequest) (*batchv1.AdminListBatchJobsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := pageBounds(req.GetPage())
	rows, total, err := repo.ListBatchJobsAnyOrg(ctx, BatchFilter{
		OrganizationID: req.GetOrganizationId(),
		Status:         req.GetStatus(),
		Model:          req.GetModel(),
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]*batchv1.BatchJob, 0, len(rows))
	for _, r := range rows {
		jobs = append(jobs, batchJobToProto(r))
	}
	return &batchv1.AdminListBatchJobsResponse{
		Response:  okResponse(),
		BatchJobs: jobs,
		PageMeta:  pageMeta(offset, limit, total),
	}, nil
}

// AdminGetBatchJob returns any job's metadata and aggregate stats,
// masked (AC10).
func (s *Service) AdminGetBatchJob(ctx context.Context, req *batchv1.AdminGetBatchJobRequest) (*batchv1.AdminGetBatchJobResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	job, err := repo.FindBatchJobAnyOrg(ctx, strings.TrimSpace(req.GetBatchId()))
	if err != nil {
		return nil, err
	}
	return &batchv1.AdminGetBatchJobResponse{
		Response: okResponse(),
		BatchJob: batchJobToProto(job),
	}, nil
}

// AdminCancelBatchJob cancels any job in validating or in_progress
// (AC10).
func (s *Service) AdminCancelBatchJob(ctx context.Context, req *batchv1.AdminCancelBatchJobRequest) (*batchv1.AdminCancelBatchJobResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	job, err := repo.FindBatchJobAnyOrg(ctx, strings.TrimSpace(req.GetBatchId()))
	if err != nil {
		return nil, err
	}
	if !cancellable(job.Status) {
		return nil, apierrors.New(apierrors.CodeBatchJobStateInvalid)
	}
	now := time.Now().UTC()
	if err := repo.SetStatus(ctx, job.BatchID, StatusCancelled, &now, nil); err != nil {
		return nil, err
	}
	job.Status = StatusCancelled
	job.CompletedAt = &now
	s.recordAudit(ctx, "batch.cancelled", job.OrganizationID, job.BatchID)
	return &batchv1.AdminCancelBatchJobResponse{
		Response: okResponse(),
		BatchJob: batchJobToProto(job),
	}, nil
}

// batchJobToProto converts a batch job row to its wire representation.
func batchJobToProto(j *BatchJob) *batchv1.BatchJob {
	p := &batchv1.BatchJob{
		BatchId:           j.BatchID,
		OrganizationId:    j.OrganizationID,
		Model:             j.Model,
		Status:            statusToProto(j.Status),
		CompletionWindow:  j.CompletionWindow,
		TotalRequests:     j.TotalRequests,
		ProcessedRequests: j.ProcessedRequests,
		SucceededRequests: j.SucceededRequests,
		FailedRequests:    j.FailedRequests,
		InputTokens:       j.InputTokens,
		OutputTokens:      j.OutputTokens,
		Cost:              j.Cost,
		Currency:          j.Currency,
		FilesExpired:      j.FilesExpired,
		CreatedAt:         j.CreatedAt.Unix(),
	}
	if j.CompletedAt != nil {
		p.CompletedAt = j.CompletedAt.Unix()
	}
	if j.FilesExpireAt != nil {
		p.FilesExpireAt = j.FilesExpireAt.Unix()
	}
	return p
}

// statusToProto maps a status string to its proto enum.
func statusToProto(status string) batchv1.BatchJobStatus {
	switch status {
	case StatusValidating:
		return batchv1.BatchJobStatus_BATCH_JOB_STATUS_VALIDATING
	case StatusInProgress:
		return batchv1.BatchJobStatus_BATCH_JOB_STATUS_IN_PROGRESS
	case StatusFinalizing:
		return batchv1.BatchJobStatus_BATCH_JOB_STATUS_FINALIZING
	case StatusCompleted:
		return batchv1.BatchJobStatus_BATCH_JOB_STATUS_COMPLETED
	case StatusFailed:
		return batchv1.BatchJobStatus_BATCH_JOB_STATUS_FAILED
	case StatusExpired:
		return batchv1.BatchJobStatus_BATCH_JOB_STATUS_EXPIRED
	case StatusCancelled:
		return batchv1.BatchJobStatus_BATCH_JOB_STATUS_CANCELLED
	default:
		return batchv1.BatchJobStatus_BATCH_JOB_STATUS_UNSPECIFIED
	}
}

// pageBounds normalizes the pagination request.
func pageBounds(page *commonv1.PageRequest) (int, int) {
	limit := listDefaultLimit
	if page != nil && page.GetLimit() > 0 {
		limit = int(page.GetLimit())
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	offset := 0
	if page != nil && page.GetOffset() > 0 {
		offset = int(page.GetOffset())
	}
	return offset, limit
}

// pageMeta builds the pagination metadata.
func pageMeta(offset, limit int, total int64) *commonv1.PageMeta {
	return &commonv1.PageMeta{
		Total:  total,
		Offset: int64(offset),
		Limit:  clampInt32(limit),
	}
}

// clampInt32 safely converts an int to int32, clamping to the int32
// range to avoid overflow.
func clampInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// okResponse returns the standard success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
