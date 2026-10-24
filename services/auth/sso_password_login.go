// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// Unified login & role-based routing (feature-22): SSOPasswordLogin
// authenticates a username/password against a selected provider via the
// OIDC password grant or the LDAP bind, derives the session realm from
// the authenticated role, and mints a role-derived-realm session.
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// defaultAdminRoles is the fallback admin-role set when the config is
// unavailable (feature-22 AD3). The shipped config default is
// platform-admin, org-admin, admin, owner.
var defaultAdminRoles = []string{"platform-admin", "org-admin", "admin", "owner"}

// adminRoles returns the configured admin-role set, defaulting to the
// shipped vocabulary (feature-22 AD3). A user is an admin iff any
// session role is in the set.
func (s *Service) adminRoles() []string {
	if cfg := config.GetConfig(); cfg != nil && len(cfg.Auth.AdminRoles) > 0 {
		return cfg.Auth.AdminRoles
	}
	return defaultAdminRoles
}

// isAdminRole reports whether any of the given roles is in the
// configured admin-role set (feature-22 AD3/AD5).
func (s *Service) isAdminRole(roles []string) bool {
	set := s.adminRoles()
	for _, r := range roles {
		if containsString(set, r) {
			return true
		}
	}
	return false
}

// SSOPasswordLogin authenticates a username/password against a selected
// provider (the OIDC password grant or the LDAP bind) and mints a
// session whose realm is derived from the authenticated role
// (feature-22 AD2). The password is used only for the exchange/bind and
// is never persisted or echoed.
func (s *Service) SSOPasswordLogin(ctx context.Context, req *authv1.SSOPasswordLoginRequest) (*authv1.SSOPasswordLoginResponse, error) {
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
	// A SAML provider cannot use the password grant (feature-22 AD7).
	if prov.Type == ProviderTypeSAML {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	// A provider that does not support the password grant (e.g. GitHub
	// OAuth) must use the redirect flow only.
	if !prov.PasswordLogin {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}

	// Validate the credentials (feature-22 §7): empty → 10024.
	username := strings.TrimSpace(req.GetUsername())
	password := req.GetPassword()
	if username == "" || len(username) > 128 || password == "" || len(password) > 256 {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}

	plugin, err := s.pluginFor(prov.Type)
	if err != nil {
		return nil, err
	}
	identity, err := plugin.PasswordGrant(ctx, prov, username, password)
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

	// Derive the realm from the authenticated role (feature-22 AD2).
	realm := RealmUser
	if s.isAdminRole(roles) {
		realm = RealmAdmin
	}

	// Mint the session with the derived realm.
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
	return &authv1.SSOPasswordLoginResponse{
		Response:     okResponse(),
		SessionToken: sessionID,
		Realm:        realm,
		ExpiresAt:    expiresAt,
	}, nil
}