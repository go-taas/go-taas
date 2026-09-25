// Package auth implements the authentication and authorization service:
// local accounts, API keys and the gateway-facing key verification used
// to authenticate inference traffic.
package auth

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "auth"

// apiKeyRole is the role bound to every API key until per-key roles
// exist (features #6/#7).
const apiKeyRole = "agent"

// Pagination bounds for ListAPIKeys.
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization, set by the gateway from the
// X-Organization-Id HTTP header. Replaced by session-derived identity
// in feature #7.
const organizationMetadataKey = "x-organization-id"

// Service implements the auth gRPC service.
type Service struct {
	authv1.UnimplementedAuthServiceServer

	components server.Components
	repo       *APIKeyRepository
	cache      verdictCache
	hashParams config.Argon2Params

	// orgGuard validates the transitional organization context against
	// the organizations table (feature #6). Nil until wired: unit tests
	// skip validation; main.go and FVT always wire it.
	orgGuard *tenancy.OrgGuard

	// ssoRepo persists SSO providers, identity bindings, and users
	// (feature #7). Wired lazily from the shared components.
	ssoRepo *SSORepository

	// sessionStore is the Redis-backed session store (feature #7). Nil
	// until wired: session RPCs return 10027; main.go and FVT wire it.
	sessionStore *SessionStore

	// membershipResolver derives session roles/accessible orgs from
	// org_members (feature #10, AD2/AD11). Nil until wired: unit tests
	// fall back to IdP claims; main.go and FVT wire it.
	membershipResolver *tenancy.MembershipResolver

	// modelAuthorizer resolves whether an organization may use a model
	// (feature-13, AD5): the data-plane half of the per-tenant model
	// authorization gate. Nil until wired: the model field of
	// VerifyAPIKey stays accepted-but-ignored.
	modelAuthorizer ModelAuthorizer

	// modelAuthCache caches the per-(org, model) authorization verdict
	// (feature-13, AD6). Wired lazily from the Redis component, or
	// directly by unit tests; nil means the check runs uncached.
	modelAuthCache modelAuthCache

	// pluginFactory builds the IdP plugin for a provider type. It is
	// the injection point for tests to substitute a fake plugin.
	pluginFactory func(string) (IDPPlugin, error)

	// auditRecorder is the best-effort audit recorder (feature #15,
	// AD3). Nil until wired: no audit events are produced. Mutating
	// RPCs call it after the mutation succeeds and ignore its failure.
	auditRecorder AuditRecorder
}

// AuditRecorder is the best-effort, non-fatal audit recorder seam
// (feature #15, AD3). It is implemented by the audit module and injected
// at wiring time. A failure is logged and never fails the mutation.
type AuditRecorder interface {
	// Record writes one audit event best-effort; it never returns an
	// error.
	Record(ctx context.Context, ev *audit.AuditEvent)
}

// New constructs the auth service. The repository and cache are wired
// lazily on first use from the shared components (the database and
// Redis components are initialized by server Init, which runs after
// service construction).
func New(components server.Components) *Service {
	return &Service{components: components}
}

// NewWithRepositoryAndCache constructs an auth service bound directly to
// a repository and verdict cache. It is the injection point used by
// tests and by any embedding that bypasses the shared components.
func NewWithRepositoryAndCache(repo *APIKeyRepository, cache verdictCache, hashParams config.Argon2Params) *Service {
	return &Service{repo: repo, cache: cache, hashParams: hashParams}
}

// NewForFVT constructs an auth service bound to a caller-provided GORM
// database with the in-memory verdict cache and fast test hash
// parameters. It exists so full-verification tests can wire the real
// service stack against a disposable database.
func NewForFVT(db *gorm.DB) *Service {
	repo := NewAPIKeyRepository(db)
	return &Service{
		repo:          repo,
		cache:         newFakeVerdictCache(),
		hashParams:    config.Argon2Params{Algorithm: "argon2id", Time: 1, MemoryMiB: 16, Parallelism: 1},
		ssoRepo:       NewSSORepository(db),
		pluginFactory: NewPlugin,
	}
}

// MigrateSchemaForFVT applies the auth schema (api_keys, sso_providers,
// identity_bindings, users) onto a caller-provided database for
// full-verification tests.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&APIKey{}, &SSOProvider{}, &IdentityBinding{}, &User{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	authv1.RegisterAuthServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return authv1.RegisterAuthServiceHandler
}

// Migrate implements server.Migrator: it creates/updates the api_keys,
// sso_providers, identity_bindings, and users tables via GORM
// AutoMigrate. The GORM model is the single source of truth for the
// schema.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&APIKey{}, &SSOProvider{}, &IdentityBinding{}, &User{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "auth: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "auth: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the API key repository.
func (s *Service) repository() (*APIKeyRepository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.repo = NewAPIKeyRepository(db)
	return s.repo, nil
}

// verdictCacheFor lazily wires and returns the verdict cache.
func (s *Service) verdictCacheFor() (verdictCache, error) {
	if s.cache != nil {
		return s.cache, nil
	}
	if s.components == nil || s.components.Redis() == nil {
		// No Redis component: fail closed with an explicit error rather
		// than silently skipping the cache.
		return nil, apierrors.Newf(apierrors.CodeInternal, "auth: redis component unavailable")
	}
	raw := s.components.Redis().Client()
	client, ok := raw.(*goredis.Client)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "auth: unexpected redis handle type %T", raw)
	}
	s.cache = newRedisVerdictCache(client, s.apiKeyCacheTTL())
	return s.cache, nil
}

// apiKeyCacheTTL returns the configured positive-cache TTL, defaulting
// to 30s (aligned to the Wasm local cache TTL so the revoke propagation
// bound holds even when the Redis delete fails).
func (s *Service) apiKeyCacheTTL() time.Duration {
	if cfg := config.GetConfig(); cfg != nil && cfg.Auth.APIKeyCacheTTL > 0 {
		return cfg.Auth.APIKeyCacheTTL
	}
	return 30 * time.Second
}

// hashParamsFor returns the Argon2id parameters, falling back to the
// shipped defaults for zero values.
func (s *Service) hashParamsFor() config.Argon2Params {
	if s.hashParams != (config.Argon2Params{}) {
		return s.hashParams
	}
	if cfg := config.GetConfig(); cfg != nil {
		return cfg.Auth.APIKeyHash.WithDefaults()
	}
	return config.Argon2Params{}.WithDefaults()
}

// resolveOrganizationID reads the transitional caller organization from
// the x-organization-id gRPC metadata (set by the gateway from the
// X-Organization-Id HTTP header). Missing or empty values are
// unauthorized. Replaced by session-derived identity in feature #7.
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

// CreateUser registers a local account.
func (s *Service) CreateUser(_ context.Context, _ *authv1.CreateUserRequest) (*authv1.CreateUserResponse, error) {
	return nil, apierrors.Newf(apierrors.CodeUnauthorized, "auth: not implemented")
}

// Login authenticates a local account and issues a session token.
func (s *Service) Login(_ context.Context, _ *authv1.LoginRequest) (*authv1.LoginResponse, error) {
	return nil, apierrors.Newf(apierrors.CodeUnauthorized, "auth: not implemented")
}

// SetOrgGuard injects the tenancy read guard (the SetDeleteModelGuard
// pattern). Production and FVT wire it; unit tests leave it nil so
// checkOrg no-ops.
func (s *Service) SetOrgGuard(g *tenancy.OrgGuard) { s.orgGuard = g }

// SetSessionStore injects the Redis-backed session store (feature #7).
// Production and FVT wire it; unit tests leave it nil so session RPCs
// return 10027.
func (s *Service) SetSessionStore(st *SessionStore) { s.sessionStore = st }

// SetMembershipResolver injects the org_members membership resolver
// (feature #10, AD2/AD11). Production and FVT wire it; unit tests leave
// it nil so session derivation falls back to IdP claims.
func (s *Service) SetMembershipResolver(r *tenancy.MembershipResolver) { s.membershipResolver = r }

// SetAuditRecorder injects the best-effort audit recorder (feature #15,
// AD3). Production and FVT wire it; unit tests leave it nil so no audit
// events are produced.
func (s *Service) SetAuditRecorder(r AuditRecorder) { s.auditRecorder = r }

// CreateSessionForTest creates a session in the store directly. It is
// used by FVT to seed a session without going through the SSO flow.
func (s *Service) CreateSessionForTest(ctx context.Context, sess *Session, accessToken string) error {
	if s.sessionStore == nil {
		return apierrors.New(apierrors.CodeSessionInvalid)
	}
	return s.sessionStore.Create(ctx, sess, accessToken)
}

// checkOrg validates the org context: existence on reads, active
// state on gated writes. No-op when the guard is not wired.
func (s *Service) checkOrg(ctx context.Context, orgID string, requireActive bool) error {
	if s.orgGuard == nil {
		return nil
	}
	if requireActive {
		return s.orgGuard.RequireActive(ctx, orgID)
	}
	return s.orgGuard.RequireExists(ctx, orgID)
}

// ListAPIKeys returns the API keys of the caller's organization.
func (s *Service) ListAPIKeys(ctx context.Context, req *authv1.ListAPIKeysRequest) (*authv1.ListAPIKeysResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}

	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListByOrganization(ctx, orgID, offset, limit, req.GetActiveOnly(), time.Now())
	if err != nil {
		return nil, err
	}

	keys := make([]*authv1.APIKeySummary, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, summarizeAPIKey(row))
	}
	return &authv1.ListAPIKeysResponse{
		Response: okResponse(),
		Keys:     keys,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
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

// summarizeAPIKey maps a row to the masked summary. It never carries the
// secret or the hash.
func summarizeAPIKey(row *APIKey) *authv1.APIKeySummary {
	summary := &authv1.APIKeySummary{
		KeyId:        row.ID,
		Name:         row.Name,
		Prefix:       row.Prefix,
		CreatedAt:    row.CreatedAt.Unix(),
		Revoked:      row.Revoked,
		RateLimitRpm: row.RateLimitRPM,
		RateLimitTpm: row.RateLimitTPM,
	}
	if row.ExpiresAt != nil {
		summary.ExpiresAt = row.ExpiresAt.Unix()
	}
	if row.RevokedAt != nil {
		summary.RevokedAt = row.RevokedAt.Unix()
	}
	return summary
}

// CreateAPIKey issues a new API key. The plaintext key is returned
// exactly once; only its salted hash is stored.
func (s *Service) CreateAPIKey(ctx context.Context, req *authv1.CreateAPIKeyRequest) (*authv1.CreateAPIKeyResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
		return nil, err
	}
	// A disabled organization cannot accrue new keys (FR3.2, 10017).
	if err := s.checkOrg(ctx, orgID, true); err != nil {
		return nil, err
	}

	// Validate the name: 1-64 characters after trimming.
	name := strings.TrimSpace(req.GetName())
	if name == "" || len(name) > 64 {
		return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: name must be 1-64 characters")
	}

	// Optional expiry must be strictly in the future.
	var expiresAt *time.Time
	if req.GetExpiresAt() != 0 {
		t := time.Unix(req.GetExpiresAt(), 0).UTC()
		if !t.After(time.Now()) {
			return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: expires_at must be in the future")
		}
		expiresAt = &t
	}

	// Rate limits must be non-negative (0 = unlimited, feature #11).
	if req.GetRateLimitRpm() < 0 || req.GetRateLimitTpm() < 0 {
		return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: rate limits must be non-negative")
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}

	// Generate the key material. The plaintext never leaves this scope
	// except in the response below; it is never logged.
	plaintext, err := GenerateAPIKey()
	if err != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "auth: generate api key")
	}
	digest := KeyDigest(plaintext)
	salt, err := NewSalt()
	if err != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "auth: generate salt")
	}
	saltedHash := HashKey(digest, salt, s.hashParamsFor())

	row := &APIKey{
		ID:             uuid.NewString(),
		Name:           name,
		Prefix:         plaintext[:8],
		LookupHash:     digest,
		Salt:           salt,
		SaltedHash:     saltedHash,
		OrganizationID: orgID,
		CreatedAt:      time.Now().UTC(),
		ExpiresAt:      expiresAt,
		Revoked:        false,
		RateLimitRPM:   req.GetRateLimitRpm(),
		RateLimitTPM:   req.GetRateLimitTpm(),
	}
	if err := repo.Create(ctx, row); err != nil {
		return nil, err
	}

	return &authv1.CreateAPIKeyResponse{
		Response: okResponse(),
		ApiKey:   plaintext,
		KeyId:    row.ID,
	}, nil
}

// RevokeAPIKey revokes an API key. Revocation takes effect after the
// gateway-side cache TTL expires.
func (s *Service) RevokeAPIKey(ctx context.Context, req *authv1.RevokeAPIKeyRequest) (*authv1.RevokeAPIKeyResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
		return nil, err
	}
	// Revocation stays allowed under a disabled organization: it
	// reduces risk, it never accrues spend.
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	if req.GetKeyId() == "" {
		return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: key_id is required")
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	lookupHash, err := repo.RevokeByIDAndOrganization(ctx, orgID, req.GetKeyId(), time.Now().UTC())
	if err != nil {
		return nil, err
	}

	// Invalidate the positive-cache entry. A failure only degrades the
	// propagation bound to the Redis TTL; it must not fail the revoke.
	cache, err := s.verdictCacheFor()
	if err == nil {
		if delErr := cache.Delete(ctx, lookupHash); delErr != nil {
			logger.S().Warnw("auth: delete api key cache entry failed",
				"key_id", req.GetKeyId(), "err", delErr)
		}
	} else {
		logger.S().Warnw("auth: verdict cache unavailable during revoke", "err", err)
	}

	// Feature #15: record the successful revoke best-effort (AC1/AC3).
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    "system",
		ActorType:      "system",
		Action:         "api_key.revoke",
		ResourceType:   "api_key",
		ResourceID:     req.GetKeyId(),
		Result:         "success",
	})

	return &authv1.RevokeAPIKeyResponse{Response: okResponse()}, nil
}

// recordAudit writes one audit event best-effort (feature #15, AD3). A
// recorder failure is logged and never fails or rolls back the mutation.
func (s *Service) recordAudit(ctx context.Context, ev *audit.AuditEvent) {
	if s.auditRecorder == nil {
		return
	}
	s.auditRecorder.Record(ctx, ev)
}

// UpdateAPIKey edits a key's name, expiry and rate limits post-creation
// (feature #11, AD4). It never returns the plaintext and never changes
// the secret.
func (s *Service) UpdateAPIKey(ctx context.Context, req *authv1.UpdateAPIKeyRequest) (*authv1.UpdateAPIKeyResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	if req.GetKeyId() == "" {
		return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: key_id is required")
	}

	// Validate the name: 1-64 characters after trimming.
	name := strings.TrimSpace(req.GetName())
	if name == "" || len(name) > 64 {
		return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: name must be 1-64 characters")
	}

	// Optional expiry must be strictly in the future; 0 clears it.
	var expiresAt *time.Time
	if req.GetExpiresAt() != 0 {
		t := time.Unix(req.GetExpiresAt(), 0).UTC()
		if !t.After(time.Now()) {
			return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: expires_at must be in the future")
		}
		expiresAt = &t
	}

	// Rate limits must be non-negative (0 = unlimited).
	if req.GetRateLimitRpm() < 0 || req.GetRateLimitTpm() < 0 {
		return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: rate limits must be non-negative")
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.UpdateByIDAndOrganization(ctx, orgID, req.GetKeyId(), name, expiresAt, req.GetRateLimitRpm(), req.GetRateLimitTpm())
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, apierrors.New(apierrors.CodeAPIKeyNotFound)
	}

	// Invalidate the positive-cache entry so the new limits propagate
	// within the cache TTL (AD1). A failure only degrades the bound.
	cache, err := s.verdictCacheFor()
	if err == nil {
		if delErr := cache.Delete(ctx, row.LookupHash); delErr != nil {
			logger.S().Warnw("auth: delete api key cache entry failed on update",
				"key_id", req.GetKeyId(), "err", delErr)
		}
	}

	return &authv1.UpdateAPIKeyResponse{
		Response: okResponse(),
		Key:      summarizeAPIKey(row),
	}, nil
}

// VerifyAPIKey authenticates an inference request by its API key digest.
// It is called by the gateway on the data-plane request path.
func (s *Service) VerifyAPIKey(ctx context.Context, req *authv1.VerifyAPIKeyRequest) (*authv1.VerifyAPIKeyResponse, error) {
	digest := req.GetKeyDigest()
	if digest == "" {
		return nil, status.Error(codes.InvalidArgument, "auth: key digest is required")
	}
	if !IsValidDigest(digest) {
		return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: key digest must be 64 lowercase hex characters")
	}

	cache, err := s.verdictCacheFor()
	if err != nil {
		return nil, err
	}

	// Positive cache hit: return the cached verdict.
	verdict, err := cache.Get(ctx, digest)
	if err != nil {
		// Cache read failure fails closed: reject rather than risk an
		// unverified accept.
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "auth: cache read failed")
	}
	if verdict == nil {
		repo, err := s.repository()
		if err != nil {
			return nil, err
		}
		row, err := repo.FindByLookupHash(ctx, digest)
		if err != nil {
			return nil, err
		}

		// Lazy status checks: no background job is involved.
		if row.Revoked {
			return nil, apierrors.New(apierrors.CodeAPIKeyRevoked)
		}
		if row.ExpiresAt != nil && time.Now().After(*row.ExpiresAt) {
			return nil, apierrors.New(apierrors.CodeAPIKeyExpired)
		}

		// Constant-time hash comparison. A mismatch after a lookup hit is an
		// integrity alert (SHA-256 collision or tampering).
		if !VerifyKeyHash(digest, row.Salt, row.SaltedHash, s.hashParamsFor()) {
			logger.S().Errorw("auth: api key hash mismatch after lookup hit", "key_id", row.ID)
			return nil, apierrors.New(apierrors.CodeAPIKeyInvalid)
		}

		verdict = &keyVerdict{
			OrganizationID: row.OrganizationID,
			KeyID:          row.ID,
			Role:           apiKeyRole,
			RateLimitRPM:   row.RateLimitRPM,
			RateLimitTPM:   row.RateLimitTPM,
		}
		if err := cache.Set(ctx, digest, verdict, s.apiKeyCacheTTL()); err != nil {
			// A cache write failure only costs performance, not security.
			logger.S().Warnw("auth: cache verdict write failed", "key_id", row.ID, "err", err)
		}
	}

	// The model field is the data-plane half of per-tenant model
	// authorization (feature-13, AC7): a restricted model the key's
	// organization is not granted is rejected here. source_ip stays
	// accepted-but-ignored in v1.
	if err := s.checkModelAuthorization(ctx, req.GetModel(), verdict.OrganizationID); err != nil {
		return nil, err
	}

	return &authv1.VerifyAPIKeyResponse{
		Response:       okResponse(),
		OrganizationId: verdict.OrganizationID,
		KeyId:          verdict.KeyID,
		Role:           verdict.Role,
		RateLimitRpm:   verdict.RateLimitRPM,
		RateLimitTpm:   verdict.RateLimitTPM,
	}, nil
}

// okResponse builds the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
