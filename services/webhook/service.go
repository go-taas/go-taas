// Package webhook implements the webhook service: outbound webhook
// endpoint CRUD, event subscription, HMAC signing, retry and the
// delivery log (feature #23).
package webhook

import (
	"context"
	"math"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	webhookv1 "github.com/go-taas/go-taas/proto/taas/webhook/v1"

	"github.com/go-taas/go-taas/pkg/authctx"
	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
)

// ServiceName is the unique name of this service.
const ServiceName = "webhook"

// Pagination bounds (architecture §5.2).
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// Field length limits (architecture §5.2).
const (
	maxNameLen = 64
	maxURLLen  = 2048
)

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization, set by the gateway from the
// X-Organization-Id HTTP header (the auth module's pattern).
const organizationMetadataKey = "x-organization-id"

// SessionOrgResolver resolves the session's active organization
// (feature-17 AD6). It is implemented by the auth module and injected at
// wiring time. Nil until wired: the transitional X-Organization-Id
// header is used.
type SessionOrgResolver interface {
	// SessionActiveOrg returns the session's active organization, or
	// ("", nil) when no session is present (transitional access).
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id from
// the session (feature #10). It is implemented by the auth module and
// injected at wiring time.
type SessionUserResolver interface {
	// SessionUserID returns the authenticated caller's user id.
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the admin webhook RPCs. It
// is implemented by tenancy.RoleGuard.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleMember is the minimum org role for the admin webhook RPCs.
const roleMember = "member"

// AuditRecorder is the best-effort, non-fatal audit recorder seam
// (feature #15, AD3). It is implemented by the audit module and injected
// at wiring time.
type AuditRecorder interface {
	// Record writes one audit event best-effort; it never returns an
	// error.
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// Service implements the webhook gRPC service.
type Service struct {
	webhookv1.UnimplementedWebhookServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT; production
	// resolves it lazily from the shared components.
	repo *Repository

	// deliverer posts signed payloads to endpoints (AD11).
	deliverer *Deliverer

	// secretKey is the AES-256-GCM master key for secret-at-rest
	// encryption (AD3).
	secretKey string

	// sessionOrgResolver resolves the session's active organization for
	// the admin-realm reads (feature-17 AD6). Nil until wired: the
	// transitional X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// sessionUserResolver resolves the caller's user id for the admin
	// role check (feature #10). Nil until wired: no role check.
	sessionUserResolver SessionUserResolver

	// roleGuard gates the admin webhook RPCs by the caller's role in the
	// resolved org context (feature #10, AD7). Nil until wired: no role
	// check (unit tests).
	roleGuard RoleGuard

	// auditRecorder is the best-effort audit recorder (feature #15, AD3).
	// Nil until wired: no audit events are produced.
	auditRecorder AuditRecorder
}

// New constructs the webhook service. The repository is wired lazily on
// first use from the shared components.
func New(components server.Components) *Service {
	cfg := config.GetConfig()
	secretKey := cfg.Webhook.SecretEncryptionKey
	if secretKey == "" {
		// Dev-only fallback derived from the MQ namespace (AD3). Not a
		// production secret.
		secretKey = "dev-webhook-key-" + cfg.MQ.Namespace
	}
	return &Service{
		components: components,
		deliverer:  NewDeliverer(cfg.Webhook.Delivery.Timeout),
		secretKey:  secretKey,
	}
}

// SetSessionOrgResolver injects the session-organization resolver used
// by the admin-realm reads (feature-17 AD6).
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver used by the
// admin role check (feature #10).
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard that gates the admin webhook
// RPCs by the caller's role (feature #10, AD7).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetAuditRecorder injects the best-effort audit recorder (feature #15,
// AD3).
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// NewForFVT constructs a webhook service bound to a caller-provided GORM
// database. It exists so full-verification tests can wire the real
// service stack against a disposable database.
//
//nolint:gosec // G101: dev-only test key, never a production secret
func NewForFVT(db *gorm.DB) *Service {
	return &Service{
		repo:      NewRepository(db),
		deliverer: NewDeliverer(10 * time.Second),
		secretKey: "dev-webhook-key-fvt",
	}
}

// MigrateSchemaForFVT applies the webhook schema (webhooks,
// webhook_deliveries) onto a caller-provided database for
// full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Webhook{}, &WebhookDelivery{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	webhookv1.RegisterWebhookServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return webhookv1.RegisterWebhookServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the webhooks
// and webhook_deliveries tables via GORM AutoMigrate. There is nothing
// to seed.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Webhook{}, &WebhookDelivery{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "webhook: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "webhook: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the webhook repository.
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

// resolveOrg returns the organization context for a read (feature-17
// AD6): the session's active org when a session is present, otherwise
// the transitional X-Organization-Id header.
func (s *Service) resolveOrg(ctx context.Context) (string, error) {
	if s.sessionOrgResolver != nil {
		if org, err := s.sessionOrgResolver.SessionActiveOrg(ctx); err != nil {
			return "", err
		} else if org != "" {
			return org, nil
		}
	}
	org, err := resolveOrganizationID(ctx)
	if err != nil && authctx.IsPlatformAdmin(ctx) {
		// A platform administrator belongs to no organization: the platform
		// console is platform-wide, so an empty organization is a valid
		// context. Reads then span every organization (or return nothing);
		// a write still needs a real, active organization.
		return "", nil
	}
	return org, err
}

// resolveOrganizationID reads the transitional caller organization from
// the x-organization-id gRPC metadata (set by the gateway from the
// X-Organization-Id HTTP header). Missing or empty values are
// unauthorized (the auth module's pattern).
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

// resolveSessionActor returns the caller's user id and whether a session
// is present.
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

// requireAdminRole enforces the minimum org role on the admin webhook
// RPCs. The RoleGuard resolves the caller from a session; in the
// transitional (session-less) path there is no session user to check, so
// the check is skipped and the org header itself is the access boundary
// (feature-17 AD6).
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

// recordAudit writes one audit event best-effort (feature #15, AD3).
func (s *Service) recordAudit(ctx context.Context, ev *audit.AuditEvent) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, ev)
}

// surfaceFromContext derives the webhook surface from the request path.
// The surface is a property of the binding, never a request field
// (feature #23, §3.4). The gateway forwards the request path in the
// x-request-path metadata; the admin prefix maps to "admin", everything
// else to "user".
func surfaceFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return SurfaceUser
	}
	paths := md.Get("x-request-path")
	if len(paths) > 0 && strings.HasPrefix(paths[0], "/api/v1/admin/") {
		return SurfaceAdmin
	}
	return SurfaceUser
}

// eventTypesForSurface returns the event catalog of a surface.
func eventTypesForSurface(surface string) []string {
	if surface == SurfaceAdmin {
		return AdminEventTypes
	}
	return UserEventTypes
}

// validateConfig validates the create/update config (AC2).
func validateConfig(name, endpointURL string, eventTypes []string, maxAttempts, backoffSeconds int) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameLen {
		return apierrors.New(apierrors.CodeWebhookConfigInvalid)
	}
	if len(endpointURL) > maxURLLen {
		return apierrors.New(apierrors.CodeWebhookConfigInvalid)
	}
	u, err := url.Parse(endpointURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return apierrors.New(apierrors.CodeWebhookConfigInvalid)
	}
	if len(eventTypes) == 0 {
		return apierrors.New(apierrors.CodeWebhookConfigInvalid)
	}
	if maxAttempts < 1 || maxAttempts > 10 {
		return apierrors.New(apierrors.CodeWebhookConfigInvalid)
	}
	if backoffSeconds < 1 || backoffSeconds > 3600 {
		return apierrors.New(apierrors.CodeWebhookConfigInvalid)
	}
	return nil
}

// validateEventTypes checks that every event type is in the surface's
// catalog (AC2, 10705).
func validateEventTypes(surface string, eventTypes []string) error {
	catalog := eventTypesForSurface(surface)
	valid := make(map[string]bool, len(catalog))
	for _, t := range catalog {
		valid[t] = true
	}
	for _, t := range eventTypes {
		if !valid[t] {
			return apierrors.New(apierrors.CodeWebhookEventTypeInvalid)
		}
	}
	return nil
}

// normalizePagination clamps the page request: offset >= 0, limit
// defaults to 20 when unset or non-positive, capped at 100.
func normalizePagination(page *commonv1.PageRequest) (offset, limit int) {
	offset = 0
	if page != nil && page.GetOffset() > 0 {
		offset = int(page.GetOffset())
	}
	limit = listDefaultLimit
	if page != nil && page.GetLimit() > 0 {
		limit = int(page.GetLimit())
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	return offset, limit
}

// clampInt32 clamps v into the int32 range so proto fields never
// overflow on 32-bit hosts.
func clampInt32(v int) int32 {
	if v < math.MinInt32 {
		return math.MinInt32
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

// okResponse is the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// summarizeWebhook maps a Webhook row to the proto message, including
// the delivery summary (AC3).
func summarizeWebhook(wh *Webhook, total, delivered, failed int64) *webhookv1.Webhook {
	types, _ := wh.EventTypes()
	return &webhookv1.Webhook{
		WebhookId:         wh.WebhookID,
		OrganizationId:    wh.OrganizationID,
		Surface:           wh.Surface,
		Name:              wh.Name,
		Url:               wh.URL,
		Enabled:           wh.Enabled,
		EnabledEventTypes: types,
		MaxAttempts:       clampInt32(wh.MaxAttempts),
		BackoffSeconds:    clampInt32(wh.BackoffSeconds),
		CreatedAt:         wh.CreatedAt.Unix(),
		UpdatedAt:         wh.UpdatedAt.Unix(),
		TotalDeliveries:   total,
		DeliveredCount:    delivered,
		FailedCount:       failed,
	}
}

// summarizeDelivery maps a WebhookDelivery row to the proto message.
func summarizeDelivery(d *WebhookDelivery) *webhookv1.WebhookDelivery {
	status := webhookv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_UNSPECIFIED
	switch d.Status {
	case DeliveryStatusDelivered:
		status = webhookv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_DELIVERED
	case DeliveryStatusFailed:
		status = webhookv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED
	case DeliveryStatusPending:
		status = webhookv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_PENDING
	}
	var httpCode int32
	if d.HTTPStatusCode != nil {
		httpCode = clampInt32(*d.HTTPStatusCode)
	}
	var lastAttempt int64
	if d.LastAttemptAt != nil {
		lastAttempt = d.LastAttemptAt.Unix()
	}
	return &webhookv1.WebhookDelivery{
		DeliveryId:     d.DeliveryID,
		WebhookId:      d.WebhookID,
		EventId:        d.EventID,
		EventType:      d.EventType,
		Status:         status,
		HttpStatusCode: httpCode,
		AttemptCount:   clampInt32(d.AttemptCount),
		FailureReason:  d.FailureReason,
		CreatedAt:      d.CreatedAt.Unix(),
		LastAttemptAt:  lastAttempt,
	}
}

// CreateWebhook registers an endpoint + event subscription and returns
// the webhook with the plaintext signing secret shown once (AC1).
func (s *Service) CreateWebhook(ctx context.Context, req *webhookv1.CreateWebhookRequest) (*webhookv1.CreateWebhookResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	surface := surfaceFromContext(ctx)
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	maxAttempts := int(req.GetMaxAttempts())
	if maxAttempts == 0 {
		maxAttempts = 5
	}
	backoff := int(req.GetBackoffSeconds())
	if backoff == 0 {
		backoff = 60
	}
	if err := validateConfig(req.GetName(), req.GetUrl(), req.GetEnabledEventTypes(), maxAttempts, backoff); err != nil {
		return nil, err
	}
	if err := validateEventTypes(surface, req.GetEnabledEventTypes()); err != nil {
		return nil, err
	}
	secret, err := GenerateSecret()
	if err != nil {
		return nil, err
	}
	ciphertext, err := EncryptSecret(secret, s.secretKey)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	wh := &Webhook{
		OrganizationID:   orgID,
		Surface:          surface,
		Name:             strings.TrimSpace(req.GetName()),
		URL:              req.GetUrl(),
		Enabled:          true,
		MaxAttempts:      maxAttempts,
		BackoffSeconds:   backoff,
		SecretCiphertext: ciphertext,
	}
	if err := wh.SetEventTypes(req.GetEnabledEventTypes()); err != nil {
		return nil, err
	}
	id, err := repo.InsertWebhook(ctx, wh)
	if err != nil {
		return nil, err
	}
	wh.WebhookID = id
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "webhook.create",
		ResourceType:   "webhook",
		ResourceID:     id,
		Result:         "success",
	})
	return &webhookv1.CreateWebhookResponse{
		Response:        okResponse(),
		Webhook:         summarizeWebhook(wh, 0, 0, 0),
		PlaintextSecret: secret,
	}, nil
}

// ListWebhooks returns the surface's webhooks with name search, enabled
// filter, pagination, and a delivery summary (AC3).
func (s *Service) ListWebhooks(ctx context.Context, req *webhookv1.ListWebhooksRequest) (*webhookv1.ListWebhooksResponse, error) {
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
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListWebhooks(ctx, WebhookFilter{
		OrganizationID: orgID,
		Name:           req.GetName(),
		EnabledFilter:  req.GetEnabledFilter(),
		Enabled:        req.GetEnabled(),
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	webhooks := make([]*webhookv1.Webhook, 0, len(rows))
	for _, row := range rows {
		t, d, f, err := repo.DeliverySummary(ctx, row.WebhookID)
		if err != nil {
			return nil, err
		}
		webhooks = append(webhooks, summarizeWebhook(row, t, d, f))
	}
	return &webhookv1.ListWebhooksResponse{
		Response: okResponse(),
		Webhooks: webhooks,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetWebhook returns the full config of one webhook (AC1). The plaintext
// secret is never returned.
func (s *Service) GetWebhook(ctx context.Context, req *webhookv1.GetWebhookRequest) (*webhookv1.GetWebhookResponse, error) {
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
	wh, err := repo.FindWebhookByID(ctx, orgID, req.GetWebhookId())
	if err != nil {
		return nil, err
	}
	t, d, f, err := repo.DeliverySummary(ctx, wh.WebhookID)
	if err != nil {
		return nil, err
	}
	return &webhookv1.GetWebhookResponse{
		Response: okResponse(),
		Webhook:  summarizeWebhook(wh, t, d, f),
	}, nil
}

// UpdateWebhook updates name/url/events/retry (AC4).
func (s *Service) UpdateWebhook(ctx context.Context, req *webhookv1.UpdateWebhookRequest) (*webhookv1.UpdateWebhookResponse, error) {
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
	wh, err := repo.FindWebhookByID(ctx, orgID, req.GetWebhookId())
	if err != nil {
		return nil, err
	}
	maxAttempts := int(req.GetMaxAttempts())
	if maxAttempts == 0 {
		maxAttempts = wh.MaxAttempts
	}
	backoff := int(req.GetBackoffSeconds())
	if backoff == 0 {
		backoff = wh.BackoffSeconds
	}
	name := req.GetName()
	if name == "" {
		name = wh.Name
	}
	endpointURL := req.GetUrl()
	if endpointURL == "" {
		endpointURL = wh.URL
	}
	eventTypes := req.GetEnabledEventTypes()
	if len(eventTypes) == 0 {
		eventTypes, _ = wh.EventTypes()
	}
	if err := validateConfig(name, endpointURL, eventTypes, maxAttempts, backoff); err != nil {
		return nil, err
	}
	if err := validateEventTypes(wh.Surface, eventTypes); err != nil {
		return nil, err
	}
	wh.Name = strings.TrimSpace(name)
	wh.URL = endpointURL
	wh.MaxAttempts = maxAttempts
	wh.BackoffSeconds = backoff
	if err := wh.SetEventTypes(eventTypes); err != nil {
		return nil, err
	}
	if err := repo.UpdateWebhook(ctx, wh); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "webhook.update",
		ResourceType:   "webhook",
		ResourceID:     wh.WebhookID,
		Result:         "success",
	})
	t, d, f, err := repo.DeliverySummary(ctx, wh.WebhookID)
	if err != nil {
		return nil, err
	}
	return &webhookv1.UpdateWebhookResponse{
		Response: okResponse(),
		Webhook:  summarizeWebhook(wh, t, d, f),
	}, nil
}

// DeleteWebhook deletes the webhook and its delivery log (AC4).
func (s *Service) DeleteWebhook(ctx context.Context, req *webhookv1.DeleteWebhookRequest) (*webhookv1.DeleteWebhookResponse, error) {
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
	if err := repo.DeleteWebhook(ctx, orgID, req.GetWebhookId()); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "webhook.delete",
		ResourceType:   "webhook",
		ResourceID:     req.GetWebhookId(),
		Result:         "success",
	})
	return &webhookv1.DeleteWebhookResponse{Response: okResponse()}, nil
}

// SetWebhookEnabled enables or disables (pauses) a webhook (AC4).
func (s *Service) SetWebhookEnabled(ctx context.Context, req *webhookv1.SetWebhookEnabledRequest) (*webhookv1.SetWebhookEnabledResponse, error) {
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
	wh, err := repo.SetWebhookEnabled(ctx, orgID, req.GetWebhookId(), req.GetEnabled())
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "webhook.set_enabled",
		ResourceType:   "webhook",
		ResourceID:     wh.WebhookID,
		Result:         "success",
	})
	t, d, f, err := repo.DeliverySummary(ctx, wh.WebhookID)
	if err != nil {
		return nil, err
	}
	return &webhookv1.SetWebhookEnabledResponse{
		Response: okResponse(),
		Webhook:  summarizeWebhook(wh, t, d, f),
	}, nil
}

// RollWebhookSecret regenerates the signing secret, invalidates the old
// one, and returns the new plaintext once (AC5).
func (s *Service) RollWebhookSecret(ctx context.Context, req *webhookv1.RollWebhookSecretRequest) (*webhookv1.RollWebhookSecretResponse, error) {
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
	if _, err := repo.FindWebhookByID(ctx, orgID, req.GetWebhookId()); err != nil {
		return nil, err
	}
	secret, err := GenerateSecret()
	if err != nil {
		return nil, err
	}
	ciphertext, err := EncryptSecret(secret, s.secretKey)
	if err != nil {
		return nil, err
	}
	if _, err := repo.RollWebhookSecret(ctx, orgID, req.GetWebhookId(), ciphertext); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "webhook.roll_secret",
		ResourceType:   "webhook",
		ResourceID:     req.GetWebhookId(),
		Result:         "success",
	})
	return &webhookv1.RollWebhookSecretResponse{
		Response:        okResponse(),
		PlaintextSecret: secret,
	}, nil
}

// TestWebhook sends a synthetic webhook.ping event and records it in the
// delivery log (AC6). A disabled webhook returns 10703.
func (s *Service) TestWebhook(ctx context.Context, req *webhookv1.TestWebhookRequest) (*webhookv1.TestWebhookResponse, error) {
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
	wh, err := repo.FindWebhookByID(ctx, orgID, req.GetWebhookId())
	if err != nil {
		return nil, err
	}
	if !wh.Enabled {
		return nil, apierrors.New(apierrors.CodeWebhookStateInvalid)
	}
	secret, err := DecryptSecret(wh.SecretCiphertext, s.secretKey)
	if err != nil {
		return nil, err
	}
	ev := &webhookEvent{
		ID:        "ping-" + time.Now().UTC().Format("20060102T150405Z"),
		Type:      PingEventType,
		CreatedAt: time.Now().Unix(),
		Data:      []byte(`{}`),
	}
	res := s.deliverer.Deliver(ctx, wh, secret, ev)
	now := time.Now().UTC()
	delivery := &WebhookDelivery{
		WebhookID:      wh.WebhookID,
		OrganizationID: orgID,
		EventID:        ev.ID,
		EventType:      ev.Type,
		AttemptCount:   1,
		Payload:        mustBuildPayload(ev),
		CreatedAt:      now,
		LastAttemptAt:  &now,
	}
	if res.Err == nil {
		delivery.Status = DeliveryStatusDelivered
		delivery.HTTPStatusCode = &res.HTTPStatusCode
	} else {
		delivery.Status = DeliveryStatusFailed
		code := res.HTTPStatusCode
		delivery.HTTPStatusCode = &code
		delivery.FailureReason = truncate(res.Err.Error(), 512)
	}
	if _, err := repo.InsertDelivery(ctx, delivery); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "webhook.test",
		ResourceType:   "webhook",
		ResourceID:     wh.WebhookID,
		Result:         "success",
	})
	return &webhookv1.TestWebhookResponse{
		Response: okResponse(),
		Delivery: summarizeDelivery(delivery),
	}, nil
}

// ListWebhookDeliveries returns the delivery log, filterable by status
// and event type, and paginated (AC7).
func (s *Service) ListWebhookDeliveries(ctx context.Context, req *webhookv1.ListWebhookDeliveriesRequest) (*webhookv1.ListWebhookDeliveriesResponse, error) {
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
	if _, err := repo.FindWebhookByID(ctx, orgID, req.GetWebhookId()); err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListDeliveries(ctx, DeliveryFilter{
		OrganizationID: orgID,
		WebhookID:      req.GetWebhookId(),
		Status:         deliveryStatusString(req.GetStatus()),
		EventType:      req.GetEventType(),
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	deliveries := make([]*webhookv1.WebhookDelivery, 0, len(rows))
	for _, row := range rows {
		deliveries = append(deliveries, summarizeDelivery(row))
	}
	return &webhookv1.ListWebhookDeliveriesResponse{
		Response:   okResponse(),
		Deliveries: deliveries,
		PageMeta:   &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// ResendWebhookDelivery resends a failed or delivered delivery (AC7). A
// missing delivery returns 10704; a pending delivery returns 10703.
func (s *Service) ResendWebhookDelivery(ctx context.Context, req *webhookv1.ResendWebhookDeliveryRequest) (*webhookv1.ResendWebhookDeliveryResponse, error) {
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
	wh, err := repo.FindWebhookByID(ctx, orgID, req.GetWebhookId())
	if err != nil {
		return nil, err
	}
	delivery, err := repo.FindDeliveryByID(ctx, orgID, req.GetWebhookId(), req.GetDeliveryId())
	if err != nil {
		return nil, err
	}
	if delivery.Status == DeliveryStatusPending {
		return nil, apierrors.New(apierrors.CodeWebhookStateInvalid)
	}
	if !wh.Enabled {
		return nil, apierrors.New(apierrors.CodeWebhookStateInvalid)
	}
	secret, err := DecryptSecret(wh.SecretCiphertext, s.secretKey)
	if err != nil {
		return nil, err
	}
	var ev webhookEvent
	if err := unmarshalPayload(delivery.Payload, &ev); err != nil {
		return nil, apierrors.New(apierrors.CodeWebhookStateInvalid)
	}
	res := s.deliverer.Deliver(ctx, wh, secret, &ev)
	now := time.Now().UTC()
	delivery.AttemptCount++
	delivery.LastAttemptAt = &now
	if res.Err == nil {
		delivery.Status = DeliveryStatusDelivered
		delivery.HTTPStatusCode = &res.HTTPStatusCode
		delivery.FailureReason = ""
		delivery.NextAttemptAt = nil
	} else {
		code := res.HTTPStatusCode
		delivery.HTTPStatusCode = &code
		delivery.FailureReason = truncate(res.Err.Error(), 512)
		delivery.Status = DeliveryStatusFailed
		delivery.NextAttemptAt = nil
	}
	if err := repo.UpdateDelivery(ctx, delivery); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "webhook.resend",
		ResourceType:   "webhook_delivery",
		ResourceID:     delivery.DeliveryID,
		Result:         "success",
	})
	return &webhookv1.ResendWebhookDeliveryResponse{
		Response: okResponse(),
		Delivery: summarizeDelivery(delivery),
	}, nil
}

// deliveryStatusString maps the proto enum to the storage string.
func deliveryStatusString(status webhookv1.WebhookDeliveryStatus) string {
	switch status {
	case webhookv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_DELIVERED:
		return DeliveryStatusDelivered
	case webhookv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED:
		return DeliveryStatusFailed
	case webhookv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_PENDING:
		return DeliveryStatusPending
	default:
		return ""
	}
}

// mustBuildPayload builds the delivery payload, panicking on error (the
// event is always well-formed here).
func mustBuildPayload(ev *webhookEvent) string {
	body, err := BuildPayload(ev)
	if err != nil {
		return `{}`
	}
	return string(body)
}
