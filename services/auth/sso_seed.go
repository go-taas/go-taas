// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// First-boot SSO seeding (feature-22 AD6/AD10): the auth module's Migrate
// hook seeds the Keycloak OIDC provider row, the admin platform user, and
// the admin user's identity binding idempotently, so a fresh install is
// verifiable end to end without manual configuration. The values come from
// auth.seedSso config (defaults match the compose Keycloak); the Helm chart
// points them at the bundled Keycloak and the exposed URL.
package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/services/tenancy"
)

// seedKeycloakProviderID is the provider id of the seeded Keycloak IdP.
const seedKeycloakProviderID = "keycloak"

// seedKeycloakIssuer is the issuer of the seeded Keycloak realm. It is
// reachable from both the host browser and the taas-server container
// (the compose network resolves `keycloak` to the container).
const seedKeycloakIssuer = "http://keycloak:8080/realms/go-taas"

// seedKeycloakClientID is the confidential client of the seeded realm.
const seedKeycloakClientID = "go-taas-console"

// seedKeycloakClientSecret is the client secret of the seeded realm.
// It exists only in the realm-export.json and this seed; the design doc
// §9 states seeded credentials are compose-only and production
// deployments configure their own IdP.
//nolint:gosec // seeded credential, never used in production
const seedKeycloakClientSecret = "go-taas-console-secret"

// seedKeycloakRedirectURI is the redirect URI of the seeded client.
const seedKeycloakRedirectURI = "http://localhost:9091/api/v1/auth/sso/*"

// seedAdminUsername is the seeded admin user's username.
const seedAdminUsername = "admin"

// seedAdminPassword is the seeded admin user's password at the IdP.
const seedAdminPassword = "admin"

// seedAdminOrgID is the seeded organization the admin user belongs to.
// The admin user needs an org_members row with an admin role so the
// session realm derives as admin when the membership resolver is wired
// (feature-22 AD8).
const seedAdminOrgID = "org-admin"

// seedKeycloakAttributeMapping maps the Keycloak admin role claim to the
// TaaS admin role (feature-22 §5.4). The role claim is the nested
// realm_access.roles array, flattened by decodeIDTokenClaims.
const seedKeycloakAttributeMapping = `{"username":"preferred_username","email":"email","role":"realm_access.roles"}`

// seedSsoConfig returns the effective first-boot SSO seed settings,
// falling back to the compose defaults when the global config is absent
// or a field is unset. The seed is enabled by default; an explicit
// `auth.seedSso.enabled: false` (or CONFIG_AUTH_SEEDSSO_ENABLED=false)
// disables it.
func seedSsoConfig() config.SeedSsoConfig {
	cfg := config.SeedSsoConfig{
		Enabled:       true,
		ProviderID:    seedKeycloakProviderID,
		Issuer:        seedKeycloakIssuer,
		ClientID:      seedKeycloakClientID,
		ClientSecret:  seedKeycloakClientSecret,
		RedirectURI:   seedKeycloakRedirectURI,
		AdminUsername: seedAdminUsername,
		AdminPassword: seedAdminPassword,
		AdminOrgID:    seedAdminOrgID,
	}
	if c := config.GetConfig(); c != nil {
		s := c.Auth.SeedSso
		// Enabled is honored from the config. The viper default sets it
		// true when the key is absent; an explicit false is preserved.
		// A test-set config that omits SeedSso leaves it false, which
		// the seed treats as disabled — tests that set config must reset
		// to nil (SetConfigForTest(nil)) in cleanup.
		cfg.Enabled = s.Enabled
		if s.ProviderID != "" {
			cfg.ProviderID = s.ProviderID
		}
		if s.Issuer != "" {
			cfg.Issuer = s.Issuer
		}
		if s.ClientID != "" {
			cfg.ClientID = s.ClientID
		}
		if s.ClientSecret != "" {
			cfg.ClientSecret = s.ClientSecret
		}
		if s.RedirectURI != "" {
			cfg.RedirectURI = s.RedirectURI
		}
		if s.AdminUsername != "" {
			cfg.AdminUsername = s.AdminUsername
		}
		if s.AdminPassword != "" {
			cfg.AdminPassword = s.AdminPassword
		}
		if s.AdminOrgID != "" {
			cfg.AdminOrgID = s.AdminOrgID
		}
	}
	return cfg
}

// seedComposeProviderAndAdmin seeds the Keycloak provider, the admin
// user, and the admin user's identity binding idempotently (feature-22
// AD6/AD10). Each step checks existence before inserting, so re-running
// startup does not duplicate the provider, the user, or the binding
// (FR4.4). It is a no-op when auth.seedSso.enabled is false.
func (s *Service) seedComposeProviderAndAdmin(ctx context.Context) error {
	seed := seedSsoConfig()
	if !seed.Enabled {
		return nil
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return err
	}

	// 1. The Keycloak OIDC provider row.
	if _, err := repo.FindProvider(ctx, seed.ProviderID); isRecordNotFound(err) {
		prov := &SSOProvider{
			ID:                 seed.ProviderID,
			Type:               ProviderTypeOIDC,
			DisplayName:        "Keycloak",
			Issuer:             seed.Issuer,
			ClientID:           seed.ClientID,
			ClientSecret:       seed.ClientSecret,
			RedirectURI:        seed.RedirectURI,
			Scopes:             "openid profile email",
			Enabled:            true,
			AllowAutoProvision: true,
			PasswordLogin:      true,
			AttributeMapping:   seedKeycloakAttributeMapping,
		}
		if err := repo.CreateProvider(ctx, prov); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// 2. The admin platform user.
	adminUser, err := repo.FindUserByUsername(ctx, seed.AdminUsername)
	if isRecordNotFound(err) {
		adminUser = &User{
			ID:       uuid.NewString(),
			Username: seed.AdminUsername,
			Email:    "admin@example.com",
		}
		if err := repo.CreateUser(ctx, adminUser); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// 3. The admin user's identity binding. The external subject is
	// issuer + ":" + the Keycloak admin subject. The realm-export.json
	// does not fix the subject, so the seed resolves it from the IdP at
	// first boot; if it cannot be resolved, the binding is created on
	// the first successful login via JIT provisioning (AD10).
	prov, err := repo.FindProvider(ctx, seed.ProviderID)
	if err != nil {
		return err
	}

	// 2b. The admin user's org membership with the admin role. When the
	// membership resolver is wired (feature #10, AD2/AD11), the session
	// roles come from org_members (authoritative), so the admin user
	// must have an org_members row with an admin role for the realm to
	// derive as admin (feature-22 AD8). The owning organization is
	// created first (idempotent). Idempotent: skip if the membership
	// already exists. The step is skipped when the tenancy tables are
	// absent (e.g. a unit test that migrates only the auth schema).
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	if db.Migrator().HasTable(&tenancy.Organization{}) {
		var orgCount int64
		if err := db.WithContext(ctx).Model(&tenancy.Organization{}).
			Where("id = ?", seed.AdminOrgID).Count(&orgCount).Error; err != nil {
			return err
		}
		if orgCount == 0 {
			org := &tenancy.Organization{
				ID:          seed.AdminOrgID,
				DisplayName: "Platform Administrators",
				State:       tenancy.StateActive,
			}
			if err := db.WithContext(ctx).Create(org).Error; err != nil {
				return err
			}
		}
		var memberCount int64
		if err := db.WithContext(ctx).Model(&tenancy.OrgMember{}).
			Where("user_id = ?", adminUser.ID).Count(&memberCount).Error; err != nil {
			return err
		}
		if memberCount == 0 {
			member := &tenancy.OrgMember{
				OrganizationID: seed.AdminOrgID,
				UserID:         adminUser.ID,
				Role:           tenancy.RoleAdmin,
				JoinedAt:       time.Now().UTC(),
			}
			if err := db.WithContext(ctx).Create(member).Error; err != nil {
				return err
			}
		}
		// Grant the seeded admin the platform-admin role on a dedicated
		// platform organization so the session carries it (organizations
		// are created only by platform admins). Idempotent.
		if err := seedPlatformAdminMembership(ctx, db, adminUser.ID); err != nil {
			return err
		}
	}

	return s.seedAdminBinding(ctx, repo, prov, adminUser)
}

// seedAdminBinding creates the admin user's identity binding if it does
// not exist (feature-22 AD10). The external subject is issuer + ":" +
// the Keycloak admin subject. The realm-export.json does not fix the
// subject, so the seed resolves it from the IdP at first boot; if it
// cannot be resolved, the binding is created on the first successful
// login via JIT provisioning.
func (s *Service) seedAdminBinding(ctx context.Context, repo *SSORepository, prov *SSOProvider, adminUser *User) error {
	seed := seedSsoConfig()
	adminSubject, err := s.resolveKeycloakAdminSubject(ctx, prov)
	if err != nil || adminSubject == "" {
		// Best-effort: JIT provisioning covers the binding on first
		// login. The provider and user are already seeded.
		return nil
	}
	externalSubject := prov.Issuer + ":" + adminSubject
	if _, err := repo.FindBindingBySubject(ctx, seed.ProviderID, externalSubject); isRecordNotFound(err) {
		binding := &IdentityBinding{
			ID:              uuid.NewString(),
			ProviderID:      seed.ProviderID,
			ExternalSubject: externalSubject,
			UserID:          adminUser.ID,
		}
		if err := repo.CreateBinding(ctx, binding); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}

// resolveKeycloakAdminSubject resolves the Keycloak admin user's subject
// from the IdP at first boot. The realm-export.json does not fix the
// subject, so the seed reads it from the IdP's admin user. A failure
// returns ("", nil) so the caller falls back to JIT provisioning.
func (s *Service) resolveKeycloakAdminSubject(ctx context.Context, prov *SSOProvider) (string, error) {
	seed := seedSsoConfig()
	plugin, err := s.pluginFor(prov.Type)
	if err != nil {
		return "", err
	}
	// The admin user's credentials are the seed defaults. The password
	// grant returns the identity with the subject claim. The external
	// subject prefix is the provider's actual issuer.
	identity, err := plugin.PasswordGrant(ctx, prov, seed.AdminUsername, seed.AdminPassword)
	if err != nil {
		return "", nil // best-effort: JIT covers the binding
	}
	return subjectFromExternal(identity.ExternalSubject, prov.Issuer), nil
}

// subjectFromExternal extracts the subject from an external subject of
// the form "issuer:subject".
func subjectFromExternal(external, issuer string) string {
	prefix := issuer + ":"
	if len(external) > len(prefix) && external[:len(prefix)] == prefix {
		return external[len(prefix):]
	}
	return ""
}

// EnsureComposeSeed runs the compose provider/admin seeding using the
// service's wired repository. It is the injection point for tests and
// FVT, which construct the service directly instead of running the full
// Migrate hook.
func (s *Service) EnsureComposeSeed(ctx context.Context) error {
	if s.ssoRepo == nil {
		return apierrors.Newf(apierrors.CodeInternal, "auth: sso repository not wired")
	}
	return s.seedComposeProviderAndAdmin(ctx)
}
// seedPlatformAdminMembership ensures the seeded admin holds the
// platform-admin role on the dedicated "platform" organization, so a
// session derived from org_members carries the platform-admin role.
func seedPlatformAdminMembership(ctx context.Context, db *gorm.DB, userID string) error {
	const platformOrgID = "platform"
	var orgCount int64
	if err := db.WithContext(ctx).Model(&tenancy.Organization{}).
		Where("id = ?", platformOrgID).Count(&orgCount).Error; err != nil {
		return err
	}
	if orgCount == 0 {
		org := &tenancy.Organization{
			ID: platformOrgID, DisplayName: "Platform", State: tenancy.StateActive,
		}
		if err := db.WithContext(ctx).Create(org).Error; err != nil {
			return err
		}
	}
	var memberCount int64
	if err := db.WithContext(ctx).Model(&tenancy.OrgMember{}).
		Where("organization_id = ? AND user_id = ?", platformOrgID, userID).
		Count(&memberCount).Error; err != nil {
		return err
	}
	if memberCount > 0 {
		return nil
	}
	return db.WithContext(ctx).Create(&tenancy.OrgMember{
		OrganizationID: platformOrgID,
		UserID:         userID,
		Role:           tenancy.RolePlatformAdmin,
		JoinedAt:       time.Now().UTC(),
	}).Error
}
