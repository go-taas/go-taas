package account

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	accountv1 "github.com/go-taas/go-taas/proto/taas/account/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "account"

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization.
const organizationMetadataKey = "x-organization-id"

// defaultRangeDays is the default range when since/until are unset.
const defaultRangeDays = 30

// SessionOrgResolver resolves the session's active organization.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the data-export RPCs.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleMember is the minimum org role for the data-export RPCs (AD1):
// the export surface is user-realm. It must be a key of the tenancy
// roleRank map (RoleMember), so a non-member session is rejected with
// 10036.
const roleMember = tenancy.RoleMember

// Service implements the account gRPC service.
type Service struct {
	accountv1.UnimplementedAccountServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT.
	repo *ExportRepository

	// maxRangeSeconds caps the accepted range (AD6).
	maxRangeSeconds int64

	// sessionOrgResolver resolves the session's active organization.
	sessionOrgResolver SessionOrgResolver
	// sessionUserResolver resolves the caller's user id.
	sessionUserResolver SessionUserResolver
	// roleGuard gates the data-export RPCs.
	roleGuard RoleGuard
}

// New constructs the account service.
func New(components server.Components) *Service {
	return &Service{
		components:      components,
		maxRangeSeconds: 92 * 24 * 3600,
	}
}

// SetMaxRangeSeconds overrides the accepted range cap.
func (s *Service) SetMaxRangeSeconds(v int64) { s.maxRangeSeconds = v }

// SetSessionOrgResolver injects the session-organization resolver.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver.
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard.
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// NewForFVT constructs an account service bound to a caller-provided GORM
// database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{
		repo:            NewExportRepository(db),
		maxRangeSeconds: 92 * 24 * 3600,
	}
}

// MigrateSchemaForFVT applies the account schema onto a caller-provided
// database.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&DataExport{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	accountv1.RegisterAccountServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return accountv1.RegisterAccountServiceHandler
}

// Migrate implements server.Migrator.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&DataExport{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "account: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "account: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the export repository.
func (s *Service) repository() (*ExportRepository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.repo = NewExportRepository(db)
	return s.repo, nil
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

func (s *Service) requireUserRole(ctx context.Context, orgID string) error {
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

// validateRange checks and defaults the since/until pair.
func (s *Service) validateRange(since, until int64) (int64, int64, error) {
	if until <= 0 {
		until = time.Now().Unix()
	}
	if since <= 0 {
		since = until - defaultRangeDays*24*3600
	}
	if since > until {
		return 0, 0, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	if until-since > s.maxRangeSeconds {
		return 0, 0, apierrors.New(apierrors.CodeMeteringRangeInvalid)
	}
	return since, until, nil
}

// CreateDataExport creates a data-export job (AC1).
func (s *Service) CreateDataExport(ctx context.Context, req *accountv1.CreateDataExportRequest) (*accountv1.CreateDataExportResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireUserRole(ctx, orgID); err != nil {
		return nil, err
	}
	exportType := strings.TrimSpace(req.GetType())
	if !validExportType(exportType) {
		return nil, apierrors.New(apierrors.CodeDataExportTypeInvalid)
	}
	format := strings.TrimSpace(req.GetFormat())
	if !validExportFormat(exportType, format) {
		return nil, apierrors.New(apierrors.CodeDataExportFormatInvalid)
	}
	var since, until int64
	if exportType != ExportTypeAccount {
		since, until, err = s.validateRange(req.GetSince(), req.GetUntil())
		if err != nil {
			return nil, err
		}
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	export := &DataExport{
		ID:             newUUID(),
		OrganizationID: orgID,
		Type:           exportType,
		Since:          since,
		Until:          until,
		Format:         format,
		Status:         ExportStatusPending,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repo.CreateExport(ctx, export); err != nil {
		return nil, err
	}
	return &accountv1.CreateDataExportResponse{
		Response: okResponse(),
		Export:   exportToProto(export),
	}, nil
}

// ListDataExports returns the caller's export history (AC2).
func (s *Service) ListDataExports(ctx context.Context, _ *accountv1.ListDataExportsRequest) (*accountv1.ListDataExportsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireUserRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListExports(ctx, orgID)
	if err != nil {
		return nil, err
	}
	exports := make([]*accountv1.DataExport, 0, len(rows))
	for _, e := range rows {
		exports = append(exports, exportToProto(&e))
	}
	return &accountv1.ListDataExportsResponse{
		Response: okResponse(),
		Exports:  exports,
		PageMeta: &commonv1.PageMeta{Total: int64(len(exports))},
	}, nil
}

// GetDataExport returns an export's status and definition (AC2).
func (s *Service) GetDataExport(ctx context.Context, req *accountv1.GetDataExportRequest) (*accountv1.GetDataExportResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireUserRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	export, err := repo.FindExportByID(ctx, strings.TrimSpace(req.GetExportId()))
	if err != nil {
		return nil, err
	}
	// Tenant-scoped (AD4): only the caller's own org's exports.
	if export.OrganizationID != orgID {
		return nil, apierrors.New(apierrors.CodeDataExportNotFound)
	}
	return &accountv1.GetDataExportResponse{
		Response: okResponse(),
		Export:   exportToProto(export),
	}, nil
}

// DownloadDataExport returns the export file when ready (AC3).
func (s *Service) DownloadDataExport(ctx context.Context, req *accountv1.DownloadDataExportRequest) (*accountv1.DownloadDataExportResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireUserRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	export, err := repo.FindExportByID(ctx, strings.TrimSpace(req.GetExportId()))
	if err != nil {
		return nil, err
	}
	if export.OrganizationID != orgID {
		return nil, apierrors.New(apierrors.CodeDataExportNotFound)
	}
	if export.Status != ExportStatusReady {
		return nil, apierrors.New(apierrors.CodeDataExportNotReady)
	}
	contentType := "application/json"
	filename := "data-export-" + export.ID + ".json"
	if export.Format == ExportFormatCSV {
		contentType = "text/csv"
		filename = "data-export-" + export.ID + ".csv"
	}
	return &accountv1.DownloadDataExportResponse{
		Response:    okResponse(),
		File:        export.File,
		ContentType: contentType,
		Filename:    filename,
	}, nil
}

// exportToProto converts an export row to its wire representation.
func exportToProto(e *DataExport) *accountv1.DataExport {
	return &accountv1.DataExport{
		ExportId:  e.ID,
		Type:      e.Type,
		Since:     e.Since,
		Until:     e.Until,
		Format:    e.Format,
		Status:    e.Status,
		RowCount:  e.RowCount,
		CreatedAt: e.CreatedAt.Unix(),
	}
}

// okResponse returns the standard success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}