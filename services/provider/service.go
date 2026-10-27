package provider

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	providerv1 "github.com/go-taas/go-taas/proto/taas/provider/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/model"
)

// ServiceName is the unique name of this service.
const ServiceName = "provider"

// Defaults and bounds.
const (
	roleAdmin                    = "admin"
	defaultRequestTimeoutSeconds = 10
	minRequestTimeoutSeconds     = 1
	maxRequestTimeoutSeconds     = 120
	maxNameLen                   = 64
	maxBaseURLLen                = 512
	maxUpstreamModelIDLen        = 128
	maxDisplayNameLen            = 128
	maxContextWindow             = 10_000_000
	maxHeaderValueLen            = 1024
	maxTenantFacingNameLen       = 128
)

// namePattern is the accepted provider-name shape.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// tenantFacingNamePattern is the accepted catalog-name shape.
var tenantFacingNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// headerNamePattern is the accepted extra-header-name shape.
var headerNamePattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// ModelCatalog is the seam to the model module: it publishes/re-enables an
// external catalog model, toggles its published state, and looks it up for
// resolution (the SetModelAuthorizer pattern).
type ModelCatalog interface {
	// CreateExternalModel creates a catalog model with external=true, or
	// re-enables a same-named unpublished one. A live name conflict
	// returns 13014.
	CreateExternalModel(ctx context.Context, name, providerID, upstreamModelID string) (*model.ExternalModel, error)
	// SetExternalModelPublished toggles an external catalog model's
	// published state (13015 when the model is unknown or not external).
	SetExternalModelPublished(ctx context.Context, modelID string, published bool) error
	// LookupExternalModel returns the catalog model by id (preferred) or
	// name, or 13015 when it is not an external model.
	LookupExternalModel(ctx context.Context, modelID, modelName string) (*model.ExternalModel, error)
}

// SessionOrgResolver resolves the session's active organization.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the provider RPCs.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// PlatformAdminGuard answers whether the caller is a platform
// administrator. The model provider is a platform-owned asset, so the admin provider
// RPCs answer to the platform, never to an organization administrator.
// It is implemented by tenancy.Service.
type PlatformAdminGuard interface {
	RequirePlatformAdmin(ctx context.Context) error
}

// AuditRecorder records provider mutations best-effort.
type AuditRecorder interface {
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// Service implements the provider gRPC service (feature #48). It is
// admin-surface, admin-role, platform-scoped (AD1, AD10).
type Service struct {
	providerv1.UnimplementedProviderServiceServer

	components server.Components

	providerRepo *ProviderRepository
	modelRepo    *ProviderModelRepository
	probeClient  *ProbeClient

	// secretKey is the resolved AES-256-GCM master key.
	secretKey string
	// defaultTimeout is the default per-provider request timeout.
	defaultTimeout int

	// catalog is the model-module publish/lookup seam (AD4/AD14).
	catalog ModelCatalog
	// publisher publishes metering events on the proxy path (AD14).
	publisher mq.Client

	sessionOrgResolver  SessionOrgResolver
	sessionUserResolver SessionUserResolver
	roleGuard           RoleGuard
	// platformAdminGuard gates the provider RPCs on the platform
	// administrator role: providers belong to the platform.
	platformAdminGuard PlatformAdminGuard
	auditRecorder       AuditRecorder
}

// New constructs the provider service. The master key resolves exactly
// like the webhook's: an explicit provider.secretEncryptionKey, else the
// dev-only "dev-provider-key-<mq.namespace>" fallback (AD3/AD12).
func New(components server.Components) *Service {
	cfg := config.GetConfig()
	secretKey := cfg.Provider.SecretEncryptionKey
	if secretKey == "" {
		secretKey = devKeyPrefix + cfg.MQ.Namespace
	}
	timeout := cfg.Provider.DefaultRequestTimeoutSeconds
	if timeout <= 0 {
		timeout = defaultRequestTimeoutSeconds
	}
	return &Service{
		components:     components,
		probeClient:    NewProbeClient(),
		secretKey:      secretKey,
		defaultTimeout: timeout,
	}
}

// NewForFVT constructs a provider service bound to a caller-provided GORM
// database for full-verification tests.
//
//nolint:gosec // G101: dev-only test key, never a production secret
func NewForFVT(db *gorm.DB) *Service {
	return &Service{
		providerRepo:   NewProviderRepository(db),
		modelRepo:      NewProviderModelRepository(db),
		probeClient:    NewProbeClient(),
		secretKey:      "dev-provider-key-fvt",
		defaultTimeout: defaultRequestTimeoutSeconds,
	}
}

// MigrateSchemaForFVT applies the provider schema onto a caller-provided
// database.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Provider{}, &ProviderModel{})
}

// SetModelCatalog injects the model-module publish/lookup seam.
func (s *Service) SetModelCatalog(c ModelCatalog) { s.catalog = c }

// SetPublisher injects the metering publisher.
func (s *Service) SetPublisher(c mq.Client) { s.publisher = c }

// SetSessionOrgResolver injects the session-organization resolver.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver.
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard.
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetPlatformAdminGuard injects the platform-administrator gate. When it
// is wired (production and FVT) the admin RPCs answer to the platform
// alone; a unit test that leaves it nil keeps the org-role fallback below
// so the org guard can be exercised on its own.
func (s *Service) SetPlatformAdminGuard(g PlatformAdminGuard) { s.platformAdminGuard = g }

// SetAuditRecorder injects the best-effort audit recorder.
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// SetSecretKey overrides the resolved master key (used by tests).
func (s *Service) SetSecretKey(key string) { s.secretKey = key }

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	providerv1.RegisterProviderServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return providerv1.RegisterProviderServiceHandler
}

// Migrate implements server.Migrator: it creates the providers and
// provider_models tables (AD6).
func (s *Service) Migrate(ctx context.Context) error {
	if s.providerRepo != nil {
		return nil
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Provider{}, &ProviderModel{})
}

// gormDB resolves the *gorm.DB from the shared components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "provider: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "provider: unexpected database handle type %T", raw)
	}
	return db, nil
}

// providers lazily wires the provider repository.
func (s *Service) providers() (*ProviderRepository, error) {
	if s.providerRepo != nil {
		return s.providerRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.providerRepo = NewProviderRepository(db)
	return s.providerRepo, nil
}

// models lazily wires the curated-model repository.
func (s *Service) models() (*ProviderModelRepository, error) {
	if s.modelRepo != nil {
		return s.modelRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.modelRepo = NewProviderModelRepository(db)
	return s.modelRepo, nil
}

// resolveSessionActor returns the caller's user id and whether a session is
// present.
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

// requireAdminRole enforces the admin role on the provider RPCs. The
// provider registry is platform-scoped, so when the session carries no
// active org there is nothing to resolve a role in and the check is
// skipped (the admin realm guard is the access boundary).
func (s *Service) requireAdminRole(ctx context.Context) error {
	if s.platformAdminGuard != nil {
		return s.platformAdminGuard.RequirePlatformAdmin(ctx)
	}
	if s.roleGuard == nil {
		return nil
	}
	orgID := ""
	if s.sessionOrgResolver != nil {
		org, err := s.sessionOrgResolver.SessionActiveOrg(ctx)
		if err != nil && apierrors.CodeOf(err) != apierrors.CodeSessionInvalid {
			return err
		}
		orgID = org
	}
	if orgID == "" {
		return nil
	}
	userID, hasSession, err := s.resolveSessionActor(ctx)
	if err != nil {
		return err
	}
	if !hasSession {
		return nil
	}
	return s.roleGuard.RequireRole(ctx, orgID, userID, roleAdmin)
}

// actor returns the acting user id for audit, or "system" when unknown.
func (s *Service) actor(ctx context.Context) string {
	userID, ok, _ := s.resolveSessionActor(ctx)
	if ok {
		return userID
	}
	return "system"
}

// recordAudit records one best-effort audit event.
func (s *Service) recordAudit(ctx context.Context, action, resourceID, result string) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, &audit.AuditEvent{
		ActorUserID:  s.actor(ctx),
		ActorType:    "user",
		Action:       action,
		ResourceType: "provider",
		ResourceID:   resourceID,
		Result:       result,
	})
}

// ListProviders returns one page of providers (AC1).
func (s *Service) ListProviders(ctx context.Context, req *providerv1.ListProvidersRequest) (*providerv1.ListProvidersResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.providers()
	if err != nil {
		return nil, err
	}
	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	rows, total, err := repo.List(ctx, ProviderFilter{
		Name:        req.GetName(),
		Type:        req.GetType(),
		Enabled:     req.GetEnabled(),
		ProbeStatus: req.GetProbeStatus(),
		Offset:      offset,
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*providerv1.Provider, 0, len(rows))
	for _, row := range rows {
		curated, published, err := modelRepo.CountByProvider(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, providerToProto(row, curated, published))
	}
	return &providerv1.ListProvidersResponse{
		Response:  okResponse(),
		Providers: out,
		PageMeta: &commonv1.PageMeta{
			Total:  total,
			Offset: int64(offset),
			Limit:  int32(limit), //nolint:gosec // limit is capped at 100 by normalizePage.
		},
	}, nil
}

// CreateProvider registers a provider (AC2).
func (s *Service) CreateProvider(ctx context.Context, req *providerv1.CreateProviderRequest) (*providerv1.CreateProviderResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := validateName(name); err != nil {
		return nil, err
	}
	providerType := strings.TrimSpace(req.GetType())
	if !validProviderType(providerType) {
		return nil, apierrors.New(apierrors.CodeProviderTypeInvalid)
	}
	baseURL := strings.TrimSpace(req.GetBaseUrl())
	if err := validateBaseURL(baseURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetApiKey()) == "" {
		return nil, apierrors.New(apierrors.CodeProviderAPIKeyRequired)
	}
	timeout, err := s.resolveTimeout(req.GetRequestTimeoutSeconds())
	if err != nil {
		return nil, err
	}
	headers, err := validateHeaders(req.GetExtraHeaders())
	if err != nil {
		return nil, err
	}
	repo, err := s.providers()
	if err != nil {
		return nil, err
	}
	if existing, err := repo.FindByName(ctx, name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, apierrors.New(apierrors.CodeProviderNameConflict)
	}
	ciphertext, err := s.encryptSecret(req.GetApiKey())
	if err != nil {
		return nil, err
	}
	encHeaders, err := s.encryptHeaders(headers)
	if err != nil {
		return nil, err
	}
	actor := s.actor(ctx)
	now := time.Now().UTC()
	row := &Provider{
		ID:                    uuid.NewString(),
		Name:                  name,
		Type:                  providerType,
		BaseURL:               trimTrailingSlashes(baseURL),
		Enabled:               req.GetEnabled(),
		RequestTimeoutSeconds: timeout,
		ExtraHeaders:          encHeaders,
		APIKeyCiphertext:      ciphertext,
		APIKeyHint:            maskHint(req.GetApiKey()),
		ProbeStatus:           ProbeUnknown,
		CreatedBy:             actor,
		UpdatedBy:             actor,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := repo.Create(ctx, row); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "provider.created", row.ID, "success")
	return &providerv1.CreateProviderResponse{
		Response: okResponse(),
		Provider: providerToProto(row, 0, 0),
	}, nil
}

// GetProvider returns one provider (AC1).
func (s *Service) GetProvider(ctx context.Context, req *providerv1.GetProviderRequest) (*providerv1.GetProviderResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.providers()
	if err != nil {
		return nil, err
	}
	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetProviderId())
	if err != nil {
		return nil, err
	}
	curated, published, err := modelRepo.CountByProvider(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &providerv1.GetProviderResponse{
		Response: okResponse(),
		Provider: providerToProto(row, curated, published),
	}, nil
}

// UpdateProvider edits mutable fields (AC3, AC8). An empty api_key keeps
// the existing credential; a non-empty key rotates it.
func (s *Service) UpdateProvider(ctx context.Context, req *providerv1.UpdateProviderRequest) (*providerv1.UpdateProviderResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.providers()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetProviderId())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := validateName(name); err != nil {
		return nil, err
	}
	providerType := strings.TrimSpace(req.GetType())
	if !validProviderType(providerType) {
		return nil, apierrors.New(apierrors.CodeProviderTypeInvalid)
	}
	baseURL := strings.TrimSpace(req.GetBaseUrl())
	if err := validateBaseURL(baseURL); err != nil {
		return nil, err
	}
	timeout, err := s.resolveTimeout(req.GetRequestTimeoutSeconds())
	if err != nil {
		return nil, err
	}
	headers, err := validateHeaders(req.GetExtraHeaders())
	if err != nil {
		return nil, err
	}
	// AC8: enabling a provider whose last probe is unauthorized is blocked.
	if req.GetEnabled() && !row.Enabled && row.ProbeStatus == ProbeUnauthorized {
		return nil, apierrors.New(apierrors.CodeProviderUpstreamUnauthorized)
	}
	// Name uniqueness against a different provider.
	if existing, err := repo.FindByName(ctx, name); err != nil {
		return nil, err
	} else if existing != nil && existing.ID != row.ID {
		return nil, apierrors.New(apierrors.CodeProviderNameConflict)
	}

	keyRotated := false
	if key := strings.TrimSpace(req.GetApiKey()); key != "" {
		ciphertext, err := s.encryptSecret(key)
		if err != nil {
			return nil, err
		}
		row.APIKeyCiphertext = ciphertext
		row.APIKeyHint = maskHint(key)
		keyRotated = true
	}
	encHeaders, err := s.encryptHeaders(headers)
	if err != nil {
		return nil, err
	}
	wasEnabled := row.Enabled
	row.Name = name
	row.Type = providerType
	row.BaseURL = trimTrailingSlashes(baseURL)
	row.Enabled = req.GetEnabled()
	row.RequestTimeoutSeconds = timeout
	row.ExtraHeaders = encHeaders
	row.UpdatedBy = s.actor(ctx)
	if err := repo.Update(ctx, row); err != nil {
		return nil, err
	}
	if keyRotated {
		s.recordAudit(ctx, "provider.key_rotated", row.ID, "success")
	}
	if wasEnabled != row.Enabled {
		if row.Enabled {
			s.recordAudit(ctx, "provider.enabled", row.ID, "success")
		} else {
			s.recordAudit(ctx, "provider.disabled", row.ID, "success")
		}
	}
	s.recordAudit(ctx, "provider.updated", row.ID, "success")

	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	curated, published, err := modelRepo.CountByProvider(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &providerv1.UpdateProviderResponse{
		Response: okResponse(),
		Provider: providerToProto(row, curated, published),
	}, nil
}

// DeleteProvider removes a provider and its curated rows (AC8). It is
// blocked while any curated model is published (13016).
func (s *Service) DeleteProvider(ctx context.Context, req *providerv1.DeleteProviderRequest) (*providerv1.DeleteProviderResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.providers()
	if err != nil {
		return nil, err
	}
	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetProviderId())
	if err != nil {
		return nil, err
	}
	published, err := modelRepo.CountPublished(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if published > 0 {
		return nil, apierrors.New(apierrors.CodeProviderInUse)
	}
	if err := repo.DeleteWithModels(ctx, row.ID); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "provider.deleted", row.ID, "success")
	return &providerv1.DeleteProviderResponse{Response: okResponse()}, nil
}

// TestProvider runs a synchronous connectivity probe and persists the
// result (AC4). A probe is always allowed, even for a disabled provider.
func (s *Service) TestProvider(ctx context.Context, req *providerv1.TestProviderRequest) (*providerv1.TestProviderResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.providers()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetByID(ctx, req.GetProviderId())
	if err != nil {
		return nil, err
	}
	key, err := s.decryptSecret(row.APIKeyCiphertext)
	if err != nil {
		return nil, err
	}
	headers, err := s.decryptHeaders(row.ExtraHeaders)
	if err != nil {
		return nil, err
	}
	res := s.probeClient.Probe(ctx, row.BaseURL, key, headers, row.RequestTimeoutSeconds)
	if err := repo.UpdateProbe(ctx, row.ID, res); err != nil {
		return nil, err
	}
	row.ProbeStatus = res.Status
	return &providerv1.TestProviderResponse{
		Response: okResponse(),
		Probe:    probeToProto(res),
	}, nil
}

// ProviderName returns a provider's name (implements the model module's
// provider-name resolver seam; empty when unknown).
func (s *Service) ProviderName(ctx context.Context, providerID string) (string, error) {
	repo, err := s.providers()
	if err != nil {
		return "", err
	}
	row, err := repo.GetByID(ctx, providerID)
	if err != nil {
		if apierrors.CodeOf(err) == apierrors.CodeProviderNotFound {
			return "", nil
		}
		return "", err
	}
	return row.Name, nil
}

// resolveTimeout returns the effective request timeout (default when 0).
func (s *Service) resolveTimeout(v int64) (int, error) {
	if v == 0 {
		if s.defaultTimeout > 0 {
			return s.defaultTimeout, nil
		}
		return defaultRequestTimeoutSeconds, nil
	}
	if v < minRequestTimeoutSeconds || v > maxRequestTimeoutSeconds {
		return 0, apierrors.New(apierrors.CodeProviderModelInvalid)
	}
	return int(v), nil
}

// validateName validates the provider name (13003).
func validateName(name string) error {
	if name == "" || len(name) > maxNameLen || !namePattern.MatchString(name) {
		return apierrors.New(apierrors.CodeProviderNameInvalid)
	}
	return nil
}

// validateBaseURL validates the base URL (13004).
func validateBaseURL(raw string) error {
	if raw == "" || len(raw) > maxBaseURLLen {
		return apierrors.New(apierrors.CodeProviderBaseURLInvalid)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return apierrors.New(apierrors.CodeProviderBaseURLInvalid)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return apierrors.New(apierrors.CodeProviderBaseURLInvalid)
	}
	return nil
}

// validateHeaders validates the extra headers (13010).
func validateHeaders(headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		if !headerNamePattern.MatchString(name) || len(value) > maxHeaderValueLen {
			return nil, apierrors.New(apierrors.CodeProviderModelInvalid)
		}
		out[name] = value
	}
	return out, nil
}

// providerToProto maps a provider row to its masked proto projection.
func providerToProto(p *Provider, curated, published int64) *providerv1.Provider {
	out := &providerv1.Provider{
		ProviderId:            p.ID,
		Name:                  p.Name,
		Type:                  p.Type,
		BaseUrl:               p.BaseURL,
		Enabled:               p.Enabled,
		RequestTimeoutSeconds: int64(p.RequestTimeoutSeconds),
		ExtraHeaderNames:      headerNames(p.ExtraHeaders),
		ApiKeyHint:            p.APIKeyHint,
		LastProbe:             storedProbeToProto(p),
		UpstreamModelCount:    curated,
		PublishedModelCount:   published,
		CreatedAt:             p.CreatedAt.Unix(),
		UpdatedAt:             p.UpdatedAt.Unix(),
	}
	return out
}

// storedProbeToProto renders the persisted probe state.
func storedProbeToProto(p *Provider) *providerv1.ProbeResult {
	status := p.ProbeStatus
	if status == "" {
		status = ProbeUnknown
	}
	checked := int64(0)
	if p.ProbeCheckedAt != nil {
		checked = p.ProbeCheckedAt.Unix()
	}
	return &providerv1.ProbeResult{
		Status:     status,
		LatencyMs:  p.ProbeLatencyMs,
		HttpStatus: int32(p.ProbeHTTPStatus), //nolint:gosec // HTTP status fits int32.
		Message:    p.ProbeMessage,
		CheckedAt:  checked,
	}
}

// probeToProto renders a fresh probe result.
func probeToProto(res ProbeResult) *providerv1.ProbeResult {
	return &providerv1.ProbeResult{
		Status:     res.Status,
		LatencyMs:  res.LatencyMs,
		HttpStatus: int32(res.HTTPStatus), //nolint:gosec // HTTP status fits int32.
		Message:    res.Message,
		CheckedAt:  res.CheckedAt.Unix(),
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

// okResponse is the standard success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
