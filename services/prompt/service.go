// Package prompt implements the prompt management & prompt library
// service (feature #43).
package prompt

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	promptv1 "github.com/go-taas/go-taas/proto/taas/prompt/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "prompt"

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

// RoleGuard enforces the minimum org role on the admin prompt RPCs.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleMember is the minimum org role for the admin prompt RPCs.
const roleMember = tenancy.RoleMember

// AuditRecorder is the best-effort audit recorder seam (AD9).
type AuditRecorder interface {
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// Service implements the prompt gRPC service.
type Service struct {
	promptv1.UnimplementedPromptServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT.
	repo *Repository

	// sessionOrgResolver resolves the session's active organization.
	sessionOrgResolver SessionOrgResolver
	// sessionUserResolver resolves the caller's user id.
	sessionUserResolver SessionUserResolver
	// roleGuard gates the admin prompt RPCs.
	roleGuard RoleGuard
	// auditRecorder records prompt actions best-effort (AD9).
	auditRecorder AuditRecorder
}

// New constructs the prompt service.
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

// NewForFVT constructs a prompt service bound to a caller-provided GORM
// database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{
		repo: NewRepository(db),
	}
}

// MigrateSchemaForFVT applies the prompt schema onto a caller-provided
// database.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Prompt{}, &PromptVersion{}, &PromptFolder{}, &PromptTemplate{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	promptv1.RegisterPromptServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return promptv1.RegisterPromptServiceHandler
}

// Migrate implements server.Migrator.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Prompt{}, &PromptVersion{}, &PromptFolder{}, &PromptTemplate{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "prompt: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "prompt: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the prompt repository.
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

func (s *Service) recordAudit(ctx context.Context, action, orgID, resourceID string) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         action,
		ResourceType:   "prompt",
		ResourceID:     resourceID,
		Result:         "success",
	})
}

func (s *Service) maxNameLen() int {
	cfg := config.GetConfig()
	if cfg == nil || cfg.Prompt.MaxNameLength <= 0 {
		return 128
	}
	return cfg.Prompt.MaxNameLength
}

func (s *Service) maxContentLen() int {
	cfg := config.GetConfig()
	if cfg == nil || cfg.Prompt.MaxContentLength <= 0 {
		return 6144
	}
	return cfg.Prompt.MaxContentLength
}

// CreatePrompt creates a prompt with version = 1 (AC1).
func (s *Service) CreatePrompt(ctx context.Context, req *promptv1.CreatePromptRequest) (*promptv1.CreatePromptResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := ValidateName(name, s.maxNameLen()); err != nil {
		return nil, err
	}
	if err := ValidateContent(req.GetContent(), s.maxContentLen()); err != nil {
		return nil, err
	}
	if err := ValidateVariables(req.GetVariables()); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	// Name conflict check (12703).
	if existing, _, _ := repo.ListPrompts(ctx, PromptFilter{OrganizationID: orgID, Search: name, Limit: 1}); len(existing) > 0 && existing[0].Name == name {
		return nil, apierrors.New(apierrors.CodePromptNameConflict)
	}
	if req.GetFolderId() != "" {
		if _, err := repo.FindFolderByID(ctx, orgID, req.GetFolderId()); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	p := &Prompt{
		OrganizationID: orgID,
		Name:           name,
		ModelID:        req.GetModelId(),
		FolderID:       folderIDPtr(req.GetFolderId()),
		ActiveVersion:  1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	promptID, err := repo.InsertPrompt(ctx, p)
	if err != nil {
		return nil, err
	}
	p.PromptID = promptID
	varsJSON, _ := json.Marshal(req.GetVariables())
	if err := repo.InsertVersion(ctx, &PromptVersion{
		PromptID:  promptID,
		Version:   1,
		Content:   req.GetContent(),
		ModelID:   req.GetModelId(),
		Variables: string(varsJSON),
		CreatedAt: now,
		CreatedBy: orgID,
	}); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "prompt.created", orgID, promptID)
	return &promptv1.CreatePromptResponse{
		Response: okResponse(),
		Prompt:   promptToProto(p, req.GetContent(), req.GetVariables()),
	}, nil
}

// ListPrompts returns the caller's prompts (AC5).
func (s *Service) ListPrompts(ctx context.Context, req *promptv1.ListPromptsRequest) (*promptv1.ListPromptsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := pageBounds(req.GetPage())
	rows, total, err := repo.ListPrompts(ctx, PromptFilter{
		OrganizationID: orgID,
		Search:         req.GetSearch(),
		FolderID:       req.GetFolderId(),
		ModelID:        req.GetModelId(),
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	prompts := make([]*promptv1.Prompt, 0, len(rows))
	for _, r := range rows {
		content, vars := s.activeVersion(ctx, repo, r)
		prompts = append(prompts, promptToProto(r, content, vars))
	}
	return &promptv1.ListPromptsResponse{
		Response: okResponse(),
		Prompts:  prompts,
		PageMeta: pageMeta(offset, limit, total),
	}, nil
}

// activeVersion loads a prompt's active version content and variables.
func (s *Service) activeVersion(ctx context.Context, repo *Repository, p *Prompt) (string, []string) {
	v, err := repo.FindVersion(ctx, p.PromptID, p.ActiveVersion)
	if err != nil {
		return "", nil
	}
	var vars []string
	_ = json.Unmarshal([]byte(v.Variables), &vars)
	return v.Content, vars
}

// GetPrompt returns a prompt's active version and metadata (AC2).
func (s *Service) GetPrompt(ctx context.Context, req *promptv1.GetPromptRequest) (*promptv1.GetPromptResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	p, err := repo.FindPromptByID(ctx, orgID, strings.TrimSpace(req.GetPromptId()))
	if err != nil {
		return nil, err
	}
	content, vars := s.activeVersion(ctx, repo, p)
	return &promptv1.GetPromptResponse{
		Response: okResponse(),
		Prompt:   promptToProto(p, content, vars),
	}, nil
}

// UpdatePrompt creates a new version and makes it active (AC2).
func (s *Service) UpdatePrompt(ctx context.Context, req *promptv1.UpdatePromptRequest) (*promptv1.UpdatePromptResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidateContent(req.GetContent(), s.maxContentLen()); err != nil {
		return nil, err
	}
	if err := ValidateVariables(req.GetVariables()); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	p, err := repo.FindPromptByID(ctx, orgID, strings.TrimSpace(req.GetPromptId()))
	if err != nil {
		return nil, err
	}
	next, err := repo.NextVersion(ctx, p.PromptID)
	if err != nil {
		return nil, err
	}
	varsJSON, _ := json.Marshal(req.GetVariables())
	if err := repo.InsertVersion(ctx, &PromptVersion{
		PromptID:  p.PromptID,
		Version:   next,
		Content:   req.GetContent(),
		ModelID:   req.GetModelId(),
		Variables: string(varsJSON),
		CreatedAt: time.Now().UTC(),
		CreatedBy: orgID,
	}); err != nil {
		return nil, err
	}
	if err := repo.SetActiveVersion(ctx, p.PromptID, next); err != nil {
		return nil, err
	}
	p.ActiveVersion = next
	p.ModelID = req.GetModelId()
	s.recordAudit(ctx, "prompt.updated", orgID, p.PromptID)
	return &promptv1.UpdatePromptResponse{
		Response: okResponse(),
		Prompt:   promptToProto(p, req.GetContent(), req.GetVariables()),
	}, nil
}

// DeletePrompt deletes a prompt and all its versions (AC2).
func (s *Service) DeletePrompt(ctx context.Context, req *promptv1.DeletePromptRequest) (*promptv1.DeletePromptResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if err := repo.DeletePrompt(ctx, orgID, strings.TrimSpace(req.GetPromptId())); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "prompt.deleted", orgID, req.GetPromptId())
	return &promptv1.DeletePromptResponse{Response: okResponse()}, nil
}

// ListPromptVersions returns a prompt's version history (AC2).
func (s *Service) ListPromptVersions(ctx context.Context, req *promptv1.ListPromptVersionsRequest) (*promptv1.ListPromptVersionsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := repo.FindPromptByID(ctx, orgID, strings.TrimSpace(req.GetPromptId())); err != nil {
		return nil, err
	}
	offset, limit := pageBounds(req.GetPage())
	rows, total, err := repo.ListVersions(ctx, strings.TrimSpace(req.GetPromptId()), offset, limit)
	if err != nil {
		return nil, err
	}
	versions := make([]*promptv1.PromptVersion, 0, len(rows))
	for _, v := range rows {
		versions = append(versions, versionToProto(v))
	}
	return &promptv1.ListPromptVersionsResponse{
		Response: okResponse(),
		Versions: versions,
		PageMeta: pageMeta(offset, limit, total),
	}, nil
}

// RollbackPrompt restores a previous version as active (AC2).
func (s *Service) RollbackPrompt(ctx context.Context, req *promptv1.RollbackPromptRequest) (*promptv1.RollbackPromptResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	p, err := repo.FindPromptByID(ctx, orgID, strings.TrimSpace(req.GetPromptId()))
	if err != nil {
		return nil, err
	}
	v, err := repo.FindVersion(ctx, p.PromptID, int(req.GetVersion()))
	if err != nil {
		return nil, err
	}
	if err := repo.SetActiveVersion(ctx, p.PromptID, v.Version); err != nil {
		return nil, err
	}
	p.ActiveVersion = v.Version
	var vars []string
	_ = json.Unmarshal([]byte(v.Variables), &vars)
	s.recordAudit(ctx, "prompt.rolled_back", orgID, p.PromptID)
	return &promptv1.RollbackPromptResponse{
		Response: okResponse(),
		Prompt:   promptToProto(p, v.Content, vars),
	}, nil
}

// CreatePromptFolder creates a folder (AC3).
func (s *Service) CreatePromptFolder(ctx context.Context, req *promptv1.CreatePromptFolderRequest) (*promptv1.CreatePromptFolderResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := ValidateName(name, s.maxNameLen()); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	f := &PromptFolder{OrganizationID: orgID, Name: name}
	folderID, err := repo.InsertFolder(ctx, f)
	if err != nil {
		return nil, err
	}
	f.FolderID = folderID
	return &promptv1.CreatePromptFolderResponse{
		Response: okResponse(),
		Folder:   folderToProto(f),
	}, nil
}

// ListPromptFolders lists the caller's folders (AC3).
func (s *Service) ListPromptFolders(ctx context.Context, _ *promptv1.ListPromptFoldersRequest) (*promptv1.ListPromptFoldersResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	rows, total, err := repo.ListFolders(ctx, orgID)
	if err != nil {
		return nil, err
	}
	folders := make([]*promptv1.PromptFolder, 0, len(rows))
	for _, f := range rows {
		folders = append(folders, folderToProto(f))
	}
	return &promptv1.ListPromptFoldersResponse{
		Response: okResponse(),
		Folders:  folders,
		PageMeta: pageMeta(0, listDefaultLimit, total),
	}, nil
}

// DeletePromptFolder deletes an empty folder (AC3).
func (s *Service) DeletePromptFolder(ctx context.Context, req *promptv1.DeletePromptFolderRequest) (*promptv1.DeletePromptFolderResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if err := repo.DeleteFolder(ctx, orgID, strings.TrimSpace(req.GetFolderId())); err != nil {
		return nil, err
	}
	return &promptv1.DeletePromptFolderResponse{Response: okResponse()}, nil
}

// GetPromptUsage returns a prompt's usage counters (AC4).
func (s *Service) GetPromptUsage(ctx context.Context, req *promptv1.GetPromptUsageRequest) (*promptv1.GetPromptUsageResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	p, err := repo.FindPromptByID(ctx, orgID, strings.TrimSpace(req.GetPromptId()))
	if err != nil {
		return nil, err
	}
	resp := &promptv1.GetPromptUsageResponse{Response: okResponse(), TimesUsed: p.TimesUsed}
	if p.LastUsedAt != nil {
		resp.LastUsedAt = p.LastUsedAt.Unix()
	}
	return resp, nil
}

// RecordPromptUsage increments a prompt's usage counters (AC4).
func (s *Service) RecordPromptUsage(ctx context.Context, req *promptv1.RecordPromptUsageRequest) (*promptv1.RecordPromptUsageResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	p, err := repo.FindPromptByID(ctx, orgID, strings.TrimSpace(req.GetPromptId()))
	if err != nil {
		return nil, err
	}
	if err := repo.IncrementUsage(ctx, p.PromptID); err != nil {
		return nil, err
	}
	return &promptv1.RecordPromptUsageResponse{
		Response:  okResponse(),
		TimesUsed: p.TimesUsed + 1,
	}, nil
}

// ListPromptTemplates lets a tenant browse shared templates (AC6).
func (s *Service) ListPromptTemplates(ctx context.Context, req *promptv1.ListPromptTemplatesRequest) (*promptv1.ListPromptTemplatesResponse, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := pageBounds(req.GetPage())
	rows, total, err := repo.ListTemplates(ctx, TemplateFilter{
		Search:  req.GetSearch(),
		ModelID: req.GetModelId(),
		Offset:  offset,
		Limit:   limit,
	})
	if err != nil {
		return nil, err
	}
	templates := make([]*promptv1.PromptTemplate, 0, len(rows))
	for _, t := range rows {
		templates = append(templates, templateToProto(t))
	}
	return &promptv1.ListPromptTemplatesResponse{
		Response:  okResponse(),
		Templates: templates,
		PageMeta:  pageMeta(offset, limit, total),
	}, nil
}

// CopyPromptTemplate copies a shared template into a tenant-owned prompt
// (AC6).
func (s *Service) CopyPromptTemplate(ctx context.Context, req *promptv1.CopyPromptTemplateRequest) (*promptv1.CopyPromptTemplateResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	t, err := repo.FindTemplateByID(ctx, strings.TrimSpace(req.GetTemplateId()))
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		name = t.Name
	}
	if err := ValidateName(name, s.maxNameLen()); err != nil {
		return nil, err
	}
	var vars []string
	_ = json.Unmarshal([]byte(t.Variables), &vars)
	now := time.Now().UTC()
	p := &Prompt{
		OrganizationID: orgID,
		Name:           name,
		ModelID:        t.ModelID,
		ActiveVersion:  1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	promptID, err := repo.InsertPrompt(ctx, p)
	if err != nil {
		return nil, err
	}
	p.PromptID = promptID
	if err := repo.InsertVersion(ctx, &PromptVersion{
		PromptID:  promptID,
		Version:   1,
		Content:   t.Content,
		ModelID:   t.ModelID,
		Variables: t.Variables,
		CreatedAt: now,
		CreatedBy: orgID,
	}); err != nil {
		return nil, err
	}
	_ = repo.IncrementTemplateUsage(ctx, t.TemplateID)
	s.recordAudit(ctx, "prompt.copied_from_template", orgID, promptID)
	return &promptv1.CopyPromptTemplateResponse{
		Response: okResponse(),
		Prompt:   promptToProto(p, t.Content, vars),
	}, nil
}

// AdminListPrompts lists all prompts across tenants, masked (AC6).
func (s *Service) AdminListPrompts(ctx context.Context, req *promptv1.AdminListPromptsRequest) (*promptv1.AdminListPromptsResponse, error) {
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
	rows, total, err := repo.ListPromptsAnyOrg(ctx, PromptFilter{
		OrganizationID: req.GetOrganizationId(),
		Search:         req.GetSearch(),
		ModelID:        req.GetModelId(),
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	prompts := make([]*promptv1.Prompt, 0, len(rows))
	for _, r := range rows {
		// Masked: no content on the admin surface (AD4).
		prompts = append(prompts, promptToProto(r, "", nil))
	}
	return &promptv1.AdminListPromptsResponse{
		Response: okResponse(),
		Prompts:  prompts,
		PageMeta: pageMeta(offset, limit, total),
	}, nil
}

// AdminPromptUsage returns prompt usage analytics across tenants (AC6).
func (s *Service) AdminPromptUsage(ctx context.Context, req *promptv1.AdminPromptUsageRequest) (*promptv1.AdminPromptUsageResponse, error) {
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
	totalPrompts, totalUses, mostUsed, byOrg, err := repo.AdminUsage(ctx, req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	resp := &promptv1.AdminPromptUsageResponse{
		Response:     okResponse(),
		TotalPrompts: totalPrompts,
		TotalUses:    totalUses,
	}
	for _, p := range mostUsed {
		resp.MostUsed = append(resp.MostUsed, &promptv1.PromptUsageRow{
			PromptId:       p.PromptID,
			Name:           p.Name,
			OrganizationId: p.OrganizationID,
			TimesUsed:      p.TimesUsed,
		})
	}
	for _, o := range byOrg {
		resp.ByOrganization = append(resp.ByOrganization, &promptv1.OrgUsageRow{
			OrganizationId: o.OrganizationID,
			PromptCount:    o.PromptCount,
			TotalUses:      o.TotalUses,
		})
	}
	return resp, nil
}

// AdminCreateTemplate creates a shared template (AC6).
func (s *Service) AdminCreateTemplate(ctx context.Context, req *promptv1.AdminCreateTemplateRequest) (*promptv1.AdminCreateTemplateResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := ValidateName(name, s.maxNameLen()); err != nil {
		return nil, err
	}
	if err := ValidateContent(req.GetContent(), s.maxContentLen()); err != nil {
		return nil, err
	}
	if err := ValidateVariables(req.GetVariables()); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	varsJSON, _ := json.Marshal(req.GetVariables())
	t := &PromptTemplate{
		Name:      name,
		Content:   req.GetContent(),
		ModelID:   req.GetModelId(),
		Variables: string(varsJSON),
	}
	templateID, err := repo.InsertTemplate(ctx, t)
	if err != nil {
		return nil, err
	}
	t.TemplateID = templateID
	return &promptv1.AdminCreateTemplateResponse{
		Response: okResponse(),
		Template: templateToProto(t),
	}, nil
}

// AdminListTemplates lists shared templates (AC6).
func (s *Service) AdminListTemplates(ctx context.Context, req *promptv1.AdminListTemplatesRequest) (*promptv1.AdminListTemplatesResponse, error) {
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
	rows, total, err := repo.ListTemplates(ctx, TemplateFilter{
		Search: req.GetSearch(),
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	templates := make([]*promptv1.PromptTemplate, 0, len(rows))
	for _, t := range rows {
		templates = append(templates, templateToProto(t))
	}
	return &promptv1.AdminListTemplatesResponse{
		Response:  okResponse(),
		Templates: templates,
		PageMeta:  pageMeta(offset, limit, total),
	}, nil
}

// AdminUpdateTemplate updates a shared template (AC6).
func (s *Service) AdminUpdateTemplate(ctx context.Context, req *promptv1.AdminUpdateTemplateRequest) (*promptv1.AdminUpdateTemplateResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := ValidateName(name, s.maxNameLen()); err != nil {
		return nil, err
	}
	if err := ValidateContent(req.GetContent(), s.maxContentLen()); err != nil {
		return nil, err
	}
	if err := ValidateVariables(req.GetVariables()); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	t, err := repo.FindTemplateByID(ctx, strings.TrimSpace(req.GetTemplateId()))
	if err != nil {
		return nil, err
	}
	varsJSON, _ := json.Marshal(req.GetVariables())
	t.Name = name
	t.Content = req.GetContent()
	t.ModelID = req.GetModelId()
	t.Variables = string(varsJSON)
	if err := repo.UpdateTemplate(ctx, t); err != nil {
		return nil, err
	}
	return &promptv1.AdminUpdateTemplateResponse{
		Response: okResponse(),
		Template: templateToProto(t),
	}, nil
}

// AdminDeleteTemplate deletes a shared template (AC6).
func (s *Service) AdminDeleteTemplate(ctx context.Context, req *promptv1.AdminDeleteTemplateRequest) (*promptv1.AdminDeleteTemplateResponse, error) {
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
	if err := repo.DeleteTemplate(ctx, strings.TrimSpace(req.GetTemplateId())); err != nil {
		return nil, err
	}
	return &promptv1.AdminDeleteTemplateResponse{Response: okResponse()}, nil
}

// folderIDPtr returns a *string for a folder id, or nil when empty so
// GORM inserts NULL into the uuid column (PostgreSQL rejects '').
func folderIDPtr(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

// folderIDValue dereferences a nullable folder id.
func folderIDValue(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// promptToProto converts a prompt row to its wire representation.
func promptToProto(p *Prompt, content string, vars []string) *promptv1.Prompt {
	proto := &promptv1.Prompt{
		PromptId:       p.PromptID,
		OrganizationId: p.OrganizationID,
		Name:           p.Name,
		Content:        content,
		ModelId:        p.ModelID,
		FolderId:       folderIDValue(p.FolderID),
		Version:        clampInt32(p.ActiveVersion),
		Variables:      vars,
		TimesUsed:      p.TimesUsed,
		CreatedAt:      p.CreatedAt.Unix(),
		UpdatedAt:      p.UpdatedAt.Unix(),
	}
	if p.LastUsedAt != nil {
		proto.LastUsedAt = p.LastUsedAt.Unix()
	}
	return proto
}

// versionToProto converts a version row to its wire representation.
func versionToProto(v *PromptVersion) *promptv1.PromptVersion {
	var vars []string
	_ = json.Unmarshal([]byte(v.Variables), &vars)
	return &promptv1.PromptVersion{
		PromptId:  v.PromptID,
		Version:   clampInt32(v.Version),
		Content:   v.Content,
		ModelId:   v.ModelID,
		Variables: vars,
		CreatedAt: v.CreatedAt.Unix(),
		CreatedBy: v.CreatedBy,
	}
}

// folderToProto converts a folder row to its wire representation.
func folderToProto(f *PromptFolder) *promptv1.PromptFolder {
	return &promptv1.PromptFolder{
		FolderId:    f.FolderID,
		Name:        f.Name,
		PromptCount: f.PromptCount,
	}
}

// templateToProto converts a template row to its wire representation.
func templateToProto(t *PromptTemplate) *promptv1.PromptTemplate {
	var vars []string
	_ = json.Unmarshal([]byte(t.Variables), &vars)
	return &promptv1.PromptTemplate{
		TemplateId: t.TemplateID,
		Name:       t.Name,
		Content:    t.Content,
		ModelId:    t.ModelID,
		Variables:  vars,
		TimesUsed:  t.TimesUsed,
		CreatedAt:  t.CreatedAt.Unix(),
		UpdatedAt:  t.UpdatedAt.Unix(),
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
