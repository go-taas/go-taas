// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// SSO federation service RPCs (feature #7): provider management, the
// SSO login flow, sessions, and identity bindings.
package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// ssoProviderIDRegex matches the provider id syntax.
var ssoProviderIDRegex = config.TenancyIDRegex()

// ssoRepository lazily wires and returns the SSO repository.
func (s *Service) ssoRepository() (*SSORepository, error) {
	if s.ssoRepo != nil {
		return s.ssoRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.ssoRepo = NewSSORepository(db)
	return s.ssoRepo, nil
}

// sessionTTL returns the configured session TTL, defaulting to 24h.
func (s *Service) sessionTTL() time.Duration {
	if cfg := config.GetConfig(); cfg != nil && cfg.Auth.SessionTTL > 0 {
		return cfg.Auth.SessionTTL
	}
	return 24 * time.Hour
}

// ---- Provider management (admin surface) ----

// CreateSSOProvider creates an SSO identity provider.
func (s *Service) CreateSSOProvider(ctx context.Context, req *authv1.CreateSSOProviderRequest) (*authv1.CreateSSOProviderResponse, error) {
	p := req.GetProvider()
	if p == nil {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	prov, err := s.validateProvider(p, true)
	if err != nil {
		return nil, err
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	if err := repo.CreateProvider(ctx, prov); err != nil {
		return nil, err
	}
	return &authv1.CreateSSOProviderResponse{
		Response: okResponse(),
		Provider: summarizeProvider(prov),
	}, nil
}

// ListSSOProviders returns the SSO provider directory.
func (s *Service) ListSSOProviders(ctx context.Context, req *authv1.ListSSOProvidersRequest) (*authv1.ListSSOProvidersResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	typ := strings.TrimSpace(req.GetType())
	if typ != "" && typ != ProviderTypeOIDC && typ != ProviderTypeSAML && typ != ProviderTypeLDAP {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	var enabled *bool
	if req.GetEnabled() {
		v := true
		enabled = &v
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListProviders(ctx, ProviderFilter{Type: typ, Enabled: enabled}, offset, limit)
	if err != nil {
		return nil, err
	}
	providers := make([]*authv1.SSOProvider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, summarizeProvider(row))
	}
	return &authv1.ListSSOProvidersResponse{
		Response:  okResponse(),
		Providers: providers,
		PageMeta:  &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetSSOProvider returns one SSO provider.
func (s *Service) GetSSOProvider(ctx context.Context, req *authv1.GetSSOProviderRequest) (*authv1.GetSSOProviderResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	prov, err := repo.FindProvider(ctx, req.GetProviderId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeSSOProviderNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &authv1.GetSSOProviderResponse{
		Response: okResponse(),
		Provider: summarizeProvider(prov),
	}, nil
}

// UpdateSSOProvider edits a provider's mutable fields.
func (s *Service) UpdateSSOProvider(ctx context.Context, req *authv1.UpdateSSOProviderRequest) (*authv1.UpdateSSOProviderResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	existing, err := repo.FindProvider(ctx, req.GetProviderId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeSSOProviderNotFound)
	}
	if err != nil {
		return nil, err
	}
	p := req.GetProvider()
	if p == nil {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	// The id and type are immutable: take them from the existing row.
	// Merge the request's mutable fields over the existing row so a
	// partial update preserves the unchanged protocol params.
	prov := mergeProvider(existing, p)
	prov.ID = existing.ID
	prov.Type = existing.Type
	prov.Enabled = existing.Enabled
	// Validate the merged provider (type-specific params come from the
	// existing row, so they are present).
	if err := s.validateProviderType(prov); err != nil {
		return nil, err
	}
	if _, err := parseAttributeMapping(prov.AttributeMapping); err != nil {
		return nil, err
	}
	updated, err := repo.UpdateProvider(ctx, existing.ID, prov)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeSSOProviderNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &authv1.UpdateSSOProviderResponse{
		Response: okResponse(),
		Provider: summarizeProvider(updated),
	}, nil
}

// EnableSSOProvider activates a provider (idempotent).
func (s *Service) EnableSSOProvider(ctx context.Context, req *authv1.EnableSSOProviderRequest) (*authv1.EnableSSOProviderResponse, error) {
	return s.setProviderEnabled(ctx, req.GetProviderId(), true)
}

// DisableSSOProvider pauses a provider (idempotent).
func (s *Service) DisableSSOProvider(ctx context.Context, req *authv1.DisableSSOProviderRequest) (*authv1.DisableSSOProviderResponse, error) {
	prov, err := s.setProviderEnabled(ctx, req.GetProviderId(), false)
	if err != nil {
		return nil, err
	}
	return &authv1.DisableSSOProviderResponse{
		Response: okResponse(),
		Provider: prov.GetProvider(),
	}, nil
}

func (s *Service) setProviderEnabled(ctx context.Context, id string, enabled bool) (*authv1.EnableSSOProviderResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	prov, err := repo.SetProviderEnabled(ctx, id, enabled)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeSSOProviderNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &authv1.EnableSSOProviderResponse{
		Response: okResponse(),
		Provider: summarizeProvider(prov),
	}, nil
}

// DeleteSSOProvider removes a provider with no identity bindings.
func (s *Service) DeleteSSOProvider(ctx context.Context, req *authv1.DeleteSSOProviderRequest) (*authv1.DeleteSSOProviderResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	if _, err := repo.FindProvider(ctx, req.GetProviderId()); isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeSSOProviderNotFound)
	} else if err != nil {
		return nil, err
	}
	count, err := repo.CountBindingsByProvider(ctx, req.GetProviderId())
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, apierrors.New(apierrors.CodeIdentityBindingExists)
	}
	if err := repo.DeleteProvider(ctx, req.GetProviderId()); err != nil {
		return nil, err
	}
	return &authv1.DeleteSSOProviderResponse{Response: okResponse()}, nil
}

// ---- SSO login flow (user surface) ----

// RealmUser and RealmAdmin are the two session realms minted from the
// login binding (feature-17 AD2).
const (
	RealmUser  = "user"
	RealmAdmin = "admin"
)

// SSOAuthorize initiates an SSO login for a provider on the user
// surface.
func (s *Service) SSOAuthorize(ctx context.Context, req *authv1.SSOAuthorizeRequest) (*authv1.SSOAuthorizeResponse, error) {
	return s.doSSOAuthorize(ctx, req)
}

// AdminSSOAuthorize initiates an SSO login for a provider on the admin
// surface (feature-17).
func (s *Service) AdminSSOAuthorize(ctx context.Context, req *authv1.SSOAuthorizeRequest) (*authv1.SSOAuthorizeResponse, error) {
	return s.doSSOAuthorize(ctx, req)
}

// doSSOAuthorize is the shared SSO authorize body, reached by both the
// user and admin bindings.
func (s *Service) doSSOAuthorize(ctx context.Context, req *authv1.SSOAuthorizeRequest) (*authv1.SSOAuthorizeResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	prov, err := repo.FindProvider(ctx, req.GetProviderId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeSSOProviderNotFound)
	}
	if err != nil {
		return nil, err
	}
	if !prov.Enabled {
		return nil, apierrors.New(apierrors.CodeSSOProviderDisabled)
	}
	plugin, err := s.pluginFor(prov.Type)
	if err != nil {
		return nil, err
	}
	result, err := plugin.Authorize(ctx, prov)
	if err != nil {
		return nil, err
	}
	return &authv1.SSOAuthorizeResponse{
		Response:    okResponse(),
		RedirectUrl: result.RedirectURL,
		BindForm:    result.BindForm,
	}, nil
}

// SSOCallback completes an SSO login and issues a user-realm session.
func (s *Service) SSOCallback(ctx context.Context, req *authv1.SSOCallbackRequest) (*authv1.SSOCallbackResponse, error) {
	return s.doSSOCallback(ctx, req, RealmUser)
}

// AdminSSOCallback completes an SSO login and issues an admin-realm
// session (feature-17 AD2).
func (s *Service) AdminSSOCallback(ctx context.Context, req *authv1.SSOCallbackRequest) (*authv1.SSOCallbackResponse, error) {
	return s.doSSOCallback(ctx, req, RealmAdmin)
}

// doSSOCallback is the shared SSO callback body. The realm is derived
// from the binding, never from a request field (feature-17 AD2).
func (s *Service) doSSOCallback(ctx context.Context, req *authv1.SSOCallbackRequest, realm string) (*authv1.SSOCallbackResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	prov, err := repo.FindProvider(ctx, req.GetProviderId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeSSOProviderNotFound)
	}
	if err != nil {
		return nil, err
	}
	plugin, err := s.pluginFor(prov.Type)
	if err != nil {
		return nil, err
	}
	identity, err := plugin.Callback(ctx, prov, req)
	if err != nil {
		// Feature #15: record the failed login best-effort (AC3).
		s.recordAudit(ctx, &audit.AuditEvent{
			OrganizationID: prov.DefaultOrg,
			ActorUserID:    "system",
			ActorType:      "system",
			Action:         "auth.login",
			ResourceType:   "session",
			ResourceID:     req.GetProviderId(),
			Result:         "failure",
		})
		return nil, err
	}

	// Resolve the identity: match a binding or JIT-provision.
	user, err := s.resolveIdentity(ctx, repo, prov, identity)
	if err != nil {
		return nil, err
	}

	// Map org and roles from the claims (or org_members when the
	// membership resolver is wired, feature #10).
	roles, accessibleOrgs, activeOrg, err := s.mapAttributes(ctx, prov, identity, user)
	if err != nil {
		return nil, err
	}

	// Issue the session.
	if s.sessionStore == nil {
		return nil, apierrors.New(apierrors.CodeSessionInvalid)
	}
	sessionID := uuid.NewString()
	accessToken := newAccessToken()
	now := time.Now()
	expiresAt := now.Add(s.sessionTTL()).Unix()
	sess := &Session{
		SessionID:      sessionID,
		UserID:         user.ID,
		Username:       user.Username,
		Email:          user.Email,
		Roles:          roles,
		AccessibleOrgs: accessibleOrgs,
		ActiveOrg:      activeOrg,
		ExpiresAt:      expiresAt,
		CreatedAt:      now.Unix(),
		Realm:          realm,
	}
	if err := s.sessionStore.Create(ctx, sess, accessToken); err != nil {
		return nil, err
	}
	// Feature #15: record the successful login best-effort (AC3).
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: activeOrg,
		ActorUserID:    user.ID,
		ActorType:      "user",
		Action:         "auth.login",
		ResourceType:   "session",
		ResourceID:     sessionID,
		Result:         "success",
	})
	return &authv1.SSOCallbackResponse{
		Response:     okResponse(),
		SessionToken: sessionID,
		AccessToken:  accessToken,
		ExpiresAt:    expiresAt,
	}, nil
}

// ListPublicSSOProviders returns the enabled providers projected to the
// anonymous login surface (feature-17 AD11): only provider_id, type and
// display_name are exposed.
func (s *Service) ListPublicSSOProviders(ctx context.Context, req *authv1.ListPublicSSOProvidersRequest) (*authv1.ListPublicSSOProvidersResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	enabled := true
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListProviders(ctx, ProviderFilter{Enabled: &enabled}, offset, limit)
	if err != nil {
		return nil, err
	}
	providers := make([]*authv1.PublicSSOProvider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, &authv1.PublicSSOProvider{
			ProviderId:  row.ID,
			Type:        row.Type,
			DisplayName: row.DisplayName,
		})
	}
	return &authv1.ListPublicSSOProvidersResponse{
		Response:  okResponse(),
		Providers: providers,
		PageMeta:  &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// ---- Session (user surface) ----

// GetSession returns the current session.
func (s *Service) GetSession(ctx context.Context, _ *authv1.GetSessionRequest) (*authv1.GetSessionResponse, error) {
	sess, err := s.sessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return &authv1.GetSessionResponse{
		Response:       okResponse(),
		UserId:         sess.UserID,
		Username:       sess.Username,
		Roles:          sess.Roles,
		AccessibleOrgs: sess.AccessibleOrgs,
		ActiveOrg:      sess.ActiveOrg,
		ExpiresAt:      sess.ExpiresAt,
		Realm:          sess.Realm,
	}, nil
}

// UpdateSessionOrg switches the session's active org.
func (s *Service) UpdateSessionOrg(ctx context.Context, req *authv1.UpdateSessionOrgRequest) (*authv1.UpdateSessionOrgResponse, error) {
	sess, err := s.sessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	orgID := strings.TrimSpace(req.GetOrganizationId())
	if !containsString(sess.AccessibleOrgs, orgID) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err := s.sessionStore.UpdateOrg(ctx, sess.SessionID, orgID); err != nil {
		return nil, err
	}
	return &authv1.UpdateSessionOrgResponse{
		Response:  okResponse(),
		ActiveOrg: orgID,
	}, nil
}

// Logout revokes the current session (idempotent).
func (s *Service) Logout(ctx context.Context, _ *authv1.LogoutRequest) (*authv1.LogoutResponse, error) {
	sess, err := s.sessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.sessionStore.Revoke(ctx, sess.SessionID); err != nil {
		return nil, err
	}
	return &authv1.LogoutResponse{Response: okResponse()}, nil
}

// ---- Identity bindings (admin surface) ----

// CreateIdentityBinding pre-assigns an identity binding.
func (s *Service) CreateIdentityBinding(ctx context.Context, req *authv1.CreateIdentityBindingRequest) (*authv1.CreateIdentityBindingResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	providerID := strings.TrimSpace(req.GetProviderId())
	subject := strings.TrimSpace(req.GetExternalSubject())
	userID := strings.TrimSpace(req.GetUserId())
	if providerID == "" || subject == "" || userID == "" {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	if _, err := repo.FindProvider(ctx, providerID); isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeSSOProviderNotFound)
	} else if err != nil {
		return nil, err
	}
	if _, err := repo.FindUser(ctx, userID); isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	} else if err != nil {
		return nil, err
	}
	binding := &IdentityBinding{
		ID:              uuid.NewString(),
		ProviderID:      providerID,
		ExternalSubject: subject,
		UserID:          userID,
	}
	if err := repo.CreateBinding(ctx, binding); err != nil {
		return nil, err
	}
	return &authv1.CreateIdentityBindingResponse{
		Response: okResponse(),
		Binding:  summarizeBinding(binding),
	}, nil
}

// ListIdentityBindings returns the identity binding directory.
func (s *Service) ListIdentityBindings(ctx context.Context, req *authv1.ListIdentityBindingsRequest) (*authv1.ListIdentityBindingsResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListBindings(ctx, BindingFilter{
		ProviderID: strings.TrimSpace(req.GetProviderId()),
		UserID:     strings.TrimSpace(req.GetUserId()),
	}, offset, limit)
	if err != nil {
		return nil, err
	}
	bindings := make([]*authv1.IdentityBinding, 0, len(rows))
	for _, row := range rows {
		bindings = append(bindings, summarizeBinding(row))
	}
	return &authv1.ListIdentityBindingsResponse{
		Response: okResponse(),
		Bindings: bindings,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// DeleteIdentityBinding severs an identity binding (idempotent).
func (s *Service) DeleteIdentityBinding(ctx context.Context, req *authv1.DeleteIdentityBindingRequest) (*authv1.DeleteIdentityBindingResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	if err := repo.DeleteBinding(ctx, req.GetBindingId()); err != nil {
		return nil, err
	}
	return &authv1.DeleteIdentityBindingResponse{Response: okResponse()}, nil
}

// ---- helpers ----

// pluginFor returns the IdP plugin for a provider type.
func (s *Service) pluginFor(providerType string) (IDPPlugin, error) {
	if s.pluginFactory != nil {
		return s.pluginFactory(providerType)
	}
	return NewPlugin(providerType)
}

// validateProvider validates a provider request and builds the model.
// When create is true, the id and type are validated; otherwise they are
// immutable and taken from the existing row.
func (s *Service) validateProvider(p *authv1.SSOProvider, create bool) (*SSOProvider, error) {
	prov := &SSOProvider{
		Type:               strings.TrimSpace(p.GetType()),
		DisplayName:        strings.TrimSpace(p.GetDisplayName()),
		Issuer:             strings.TrimSpace(p.GetIssuer()),
		ClientID:           strings.TrimSpace(p.GetClientId()),
		ClientSecret:       p.GetClientSecret(),
		RedirectURI:        strings.TrimSpace(p.GetRedirectUri()),
		Scopes:             strings.TrimSpace(p.GetScopes()),
		MetadataURL:        strings.TrimSpace(p.GetMetadataUrl()),
		EntityID:           strings.TrimSpace(p.GetEntityId()),
		ACSUrl:             strings.TrimSpace(p.GetAcsUrl()),
		Host:               strings.TrimSpace(p.GetHost()),
		Port:               int(p.GetPort()),
		BindDN:             p.GetBindDn(),
		BaseDN:             strings.TrimSpace(p.GetBaseDn()),
		UserFilter:         strings.TrimSpace(p.GetUserFilter()),
		DefaultOrg:         strings.TrimSpace(p.GetDefaultOrg()),
		AllowAutoProvision: p.GetAllowAutoProvision(),
		AttributeMapping:   p.GetAttributeMapping(),
	}
	if create {
		id := strings.TrimSpace(p.GetProviderId())
		if !ssoProviderIDRegex.MatchString(id) {
			return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
		}
		prov.ID = id
	}
	if prov.Type != ProviderTypeOIDC && prov.Type != ProviderTypeSAML && prov.Type != ProviderTypeLDAP {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	if len(prov.DisplayName) < 1 || len(prov.DisplayName) > 128 {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	// Protocol params per type. On create they are required; on update
	// only the fields actually provided are validated (the update may
	// change a subset of the mutable fields).
	if create {
		if err := s.validateProviderType(prov); err != nil {
			return nil, err
		}
	}
	// Validate the attribute mapping JSON.
	if _, err := parseAttributeMapping(prov.AttributeMapping); err != nil {
		return nil, err
	}
	return prov, nil
}

// validateProviderType validates the type-specific protocol params.
func (s *Service) validateProviderType(prov *SSOProvider) error {
	switch prov.Type {
	case ProviderTypeOIDC:
		if prov.Issuer == "" || prov.ClientID == "" || prov.RedirectURI == "" {
			return apierrors.New(apierrors.CodeSSOProviderInvalid)
		}
	case ProviderTypeSAML:
		if prov.MetadataURL == "" && (prov.EntityID == "" || prov.ACSUrl == "") {
			return apierrors.New(apierrors.CodeSSOProviderInvalid)
		}
	case ProviderTypeLDAP:
		if prov.Host == "" || prov.BaseDN == "" {
			return apierrors.New(apierrors.CodeSSOProviderInvalid)
		}
	}
	return nil
}

// mergeProvider merges the request's mutable fields over the existing
// row. Non-empty request fields override; empty request fields keep the
// existing value (a partial update preserves the unchanged params).
func mergeProvider(existing *SSOProvider, p *authv1.SSOProvider) *SSOProvider {
	prov := *existing
	if v := strings.TrimSpace(p.GetDisplayName()); v != "" {
		prov.DisplayName = v
	}
	if v := strings.TrimSpace(p.GetIssuer()); v != "" {
		prov.Issuer = v
	}
	if v := strings.TrimSpace(p.GetClientId()); v != "" {
		prov.ClientID = v
	}
	if v := p.GetClientSecret(); v != "" {
		prov.ClientSecret = v
	}
	if v := strings.TrimSpace(p.GetRedirectUri()); v != "" {
		prov.RedirectURI = v
	}
	if v := strings.TrimSpace(p.GetScopes()); v != "" {
		prov.Scopes = v
	}
	if v := strings.TrimSpace(p.GetMetadataUrl()); v != "" {
		prov.MetadataURL = v
	}
	if v := strings.TrimSpace(p.GetEntityId()); v != "" {
		prov.EntityID = v
	}
	if v := strings.TrimSpace(p.GetAcsUrl()); v != "" {
		prov.ACSUrl = v
	}
	if v := strings.TrimSpace(p.GetHost()); v != "" {
		prov.Host = v
	}
	if p.GetPort() != 0 {
		prov.Port = int(p.GetPort())
	}
	if v := p.GetBindDn(); v != "" {
		prov.BindDN = v
	}
	if v := strings.TrimSpace(p.GetBaseDn()); v != "" {
		prov.BaseDN = v
	}
	if v := strings.TrimSpace(p.GetUserFilter()); v != "" {
		prov.UserFilter = v
	}
	if v := strings.TrimSpace(p.GetDefaultOrg()); v != "" {
		prov.DefaultOrg = v
	}
	if v := strings.TrimSpace(p.GetAttributeMapping()); v != "" {
		prov.AttributeMapping = v
	}
	// Booleans: the request explicitly sets them.
	prov.AllowAutoProvision = p.GetAllowAutoProvision()
	return &prov
}

// resolveIdentity matches a binding or JIT-provisions a user.
func (s *Service) resolveIdentity(ctx context.Context, repo *SSORepository, prov *SSOProvider, identity *Identity) (*User, error) {
	binding, err := repo.FindBindingBySubject(ctx, prov.ID, identity.ExternalSubject)
	if err == nil {
		// Binding found: load the bound user.
		user, err := repo.FindUser(ctx, binding.UserID)
		if isRecordNotFound(err) {
			return nil, apierrors.New(apierrors.CodeSSONoAccount)
		}
		if err != nil {
			return nil, err
		}
		return user, nil
	}
	if !isRecordNotFound(err) {
		return nil, err
	}
	// No binding: JIT-provision if allowed.
	if !prov.AllowAutoProvision {
		return nil, apierrors.New(apierrors.CodeSSONoAccount)
	}
	username := identity.Username
	if username == "" {
		username = identity.ExternalSubject
	}
	user := &User{
		ID:       uuid.NewString(),
		Username: username,
		Email:    identity.Email,
	}
	if err := repo.CreateUser(ctx, user); err != nil {
		return nil, err
	}
	binding = &IdentityBinding{
		ID:              uuid.NewString(),
		ProviderID:      prov.ID,
		ExternalSubject: identity.ExternalSubject,
		UserID:          user.ID,
	}
	if err := repo.CreateBinding(ctx, binding); err != nil {
		return nil, err
	}
	return user, nil
}

// mapAttributes maps the identity claims to roles, accessible orgs, and
// the active org. When the membership resolver is wired (feature #10,
// AD2/AD11), roles and accessible orgs are derived from org_members
// (authoritative); the IdP claim is a hint only.
func (s *Service) mapAttributes(ctx context.Context, prov *SSOProvider, identity *Identity, user *User) ([]string, []string, string, error) {
	if s.membershipResolver != nil {
		return s.mapMembership(ctx, prov, user)
	}
	mapping, err := parseAttributeMapping(prov.AttributeMapping)
	if err != nil {
		return nil, nil, "", err
	}
	roles := []string{}
	if mapping.Role != "" {
		if vals := identity.Claims[mapping.Role]; len(vals) > 0 {
			roles = append(roles, vals...)
		}
	}
	// Accessible orgs: the org claim values, validated against the
	// organizations table when the guard is wired.
	accessibleOrgs := []string{}
	if mapping.Org != "" {
		if vals := identity.Claims[mapping.Org]; len(vals) > 0 {
			for _, v := range vals {
				if s.orgGuard != nil {
					if err := s.orgGuard.RequireExists(ctx, v); err != nil {
						continue // skip orgs the user cannot access
					}
				}
				accessibleOrgs = append(accessibleOrgs, v)
			}
		}
	}
	// Active org: the default org, or the first accessible org.
	activeOrg := prov.DefaultOrg
	if activeOrg == "" && len(accessibleOrgs) > 0 {
		activeOrg = accessibleOrgs[0]
	}
	if activeOrg != "" && !containsString(accessibleOrgs, activeOrg) {
		// The default org must be accessible; otherwise fall back.
		activeOrg = ""
		if len(accessibleOrgs) > 0 {
			activeOrg = accessibleOrgs[0]
		}
	}
	return roles, accessibleOrgs, activeOrg, nil
}

// mapMembership derives roles and accessible orgs from org_members
// (feature #10, AD2/AD11). The IdP claim is a hint only; the membership
// table is authoritative.
func (s *Service) mapMembership(ctx context.Context, prov *SSOProvider, user *User) ([]string, []string, string, error) {
	memberships, err := s.membershipResolver.AccessibleOrgsAndRoles(ctx, user.ID)
	if err != nil {
		return nil, nil, "", err
	}
	roles := []string{}
	accessibleOrgs := []string{}
	for _, m := range memberships {
		accessibleOrgs = append(accessibleOrgs, m.OrganizationID)
		if !containsString(roles, m.Role) {
			roles = append(roles, m.Role)
		}
	}
	activeOrg := prov.DefaultOrg
	if activeOrg == "" && len(accessibleOrgs) > 0 {
		activeOrg = accessibleOrgs[0]
	}
	if activeOrg != "" && !containsString(accessibleOrgs, activeOrg) {
		activeOrg = ""
		if len(accessibleOrgs) > 0 {
			activeOrg = accessibleOrgs[0]
		}
	}
	return roles, accessibleOrgs, activeOrg, nil
}

// sessionFromContext resolves the session from the Authorization: Bearer
// metadata. A missing/expired/revoked session returns CodeSessionInvalid.
func (s *Service) sessionFromContext(ctx context.Context) (*Session, error) {
	if s.sessionStore == nil {
		return nil, apierrors.New(apierrors.CodeSessionInvalid)
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, apierrors.New(apierrors.CodeSessionInvalid)
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return nil, apierrors.New(apierrors.CodeSessionInvalid)
	}
	token := strings.TrimSpace(values[0])
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, apierrors.New(apierrors.CodeSessionInvalid)
	}
	return s.sessionStore.Get(ctx, token)
}

// SessionUserID implements tenancy.SessionResolver (feature #10): it
// resolves the authenticated caller's user id from the session.
func (s *Service) SessionUserID(ctx context.Context) (string, error) {
	sess, err := s.sessionFromContext(ctx)
	if err != nil {
		return "", err
	}
	if sess.UserID == "" {
		return "", apierrors.New(apierrors.CodeSessionInvalid)
	}
	return sess.UserID, nil
}

// SessionUserEmail implements tenancy.SessionResolver (feature #10): it
// resolves the authenticated caller's email from the session.
func (s *Service) SessionUserEmail(ctx context.Context) (string, error) {
	sess, err := s.sessionFromContext(ctx)
	if err != nil {
		return "", err
	}
	if sess.Email == "" {
		return "", apierrors.New(apierrors.CodeSessionInvalid)
	}
	return sess.Email, nil
}

// resolveOrgContext returns the org context for an org-scoped API. When
// a session is present, the session's active org wins and the header is
// ignored (D6, FR4.4). When no session is present, the transitional
// header is used (feature #6 behavior unchanged).
func (s *Service) resolveOrgContext(ctx context.Context) (string, error) {
	if sess, err := s.sessionFromContext(ctx); err == nil {
		if sess.ActiveOrg == "" {
			return "", apierrors.New(apierrors.CodeOrganizationNotFound)
		}
		return sess.ActiveOrg, nil
	}
	return resolveOrganizationID(ctx)
}

// SessionRealm resolves a session token to its realm (feature-17 AD2).
// It implements the gateway's SessionRealmResolver seam. A missing,
// expired, realm-less or unknown-realm session answers CodeSessionInvalid
// (AD3).
func (s *Service) SessionRealm(ctx context.Context, token string) (string, error) {
	if s.sessionStore == nil {
		return "", apierrors.New(apierrors.CodeSessionInvalid)
	}
	sess, err := s.sessionStore.Get(ctx, token)
	if err != nil {
		return "", err
	}
	if sess.Realm != RealmUser && sess.Realm != RealmAdmin {
		return "", apierrors.New(apierrors.CodeSessionInvalid)
	}
	return sess.Realm, nil
}

// SessionActiveOrg resolves the session's active organization, or
// ("", nil) when no session is present (transitional access). It
// implements the seam consumed by model/infer/metering/billing so a
// session-bearing call ignores X-Organization-Id (feature-17 AD6).
func (s *Service) SessionActiveOrg(ctx context.Context) (string, error) {
	sess, err := s.sessionFromContext(ctx)
	if err != nil {
		// No session (or a session the store cannot read): transitional.
		return "", nil
	}
	if sess.ActiveOrg == "" {
		return "", apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	return sess.ActiveOrg, nil
}

// summarizeProvider maps a provider row to the proto summary with
// secrets masked.
func summarizeProvider(p *SSOProvider) *authv1.SSOProvider {
	return &authv1.SSOProvider{
		ProviderId:         p.ID,
		Type:               p.Type,
		DisplayName:        p.DisplayName,
		Issuer:             p.Issuer,
		ClientId:           p.ClientID,
		ClientSecret:       maskSecret(p.ClientSecret),
		RedirectUri:        p.RedirectURI,
		Scopes:             p.Scopes,
		MetadataUrl:        p.MetadataURL,
		EntityId:           p.EntityID,
		AcsUrl:             p.ACSUrl,
		Host:               p.Host,
		Port:               clampInt32(p.Port),
		BindDn:             maskSecret(p.BindDN),
		BaseDn:             p.BaseDN,
		UserFilter:         p.UserFilter,
		Enabled:            p.Enabled,
		DefaultOrg:         p.DefaultOrg,
		AllowAutoProvision: p.AllowAutoProvision,
		AttributeMapping:   p.AttributeMapping,
		CreatedAt:          p.CreatedAt.Unix(),
		UpdatedAt:          p.UpdatedAt.Unix(),
	}
}

// maskSecret masks a write-only secret.
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	return "••••"
}

// summarizeBinding maps a binding row to the proto summary.
func summarizeBinding(b *IdentityBinding) *authv1.IdentityBinding {
	return &authv1.IdentityBinding{
		BindingId:       b.ID,
		ProviderId:      b.ProviderID,
		ExternalSubject: b.ExternalSubject,
		UserId:          b.UserID,
		CreatedAt:       b.CreatedAt.Unix(),
	}
}

// newAccessToken generates a random opaque access token.
func newAccessToken() string {
	return uuid.NewString() + uuid.NewString()
}

// containsString reports whether a slice contains a value.
func containsString(s []string, v string) bool {
	for _, item := range s {
		if item == v {
			return true
		}
	}
	return false
}

// isRecordNotFound reports whether err is GORM's ErrRecordNotFound.
func isRecordNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
