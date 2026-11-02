package auth

import (
	"context"
	"strings"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/tenancy"
)

// API key ownership (organization + optional project + personal owner).
//
// The organization stays the tenant, model-authorization and billing
// boundary. The personal owner adds management rights, a personal quota
// tier and a metering attribution dimension; the project optionally
// narrows both. A key with no owner is organization-shared and keeps
// the pre-ownership behavior.
//
// Access rules:
//   - a key's owner manages their own key;
//   - an organization owner/admin (or a platform administrator)
//     manages every key of the organization;
//   - a shared key is managed by administrators only;
//   - a caller with no session (the transitional X-Organization-Id
//     path used by scripts) keeps full access to the organization.

// ProjectLookup resolves the project facts an API key's ownership needs.
// Implemented by the tenancy service; nil until wired (unit tests), in
// which case project assignment is validated for shape only.
type ProjectLookup interface {
	// ProjectInOrg returns the project's display name and whether it
	// belongs to the organization.
	ProjectInOrg(ctx context.Context, projectID, orgID string) (string, bool, error)
	// ProjectName returns the project's display name, or "" when unknown.
	ProjectName(ctx context.Context, projectID string) (string, error)
	// UserInProject reports whether the user belongs to the project.
	UserInProject(ctx context.Context, projectID, userID string) (bool, error)
	// UserInOrg reports whether the user belongs to the organization.
	UserInOrg(ctx context.Context, orgID, userID string) (bool, error)
}

// SetProjectLookup injects the project resolver. Production and FVT wire
// it; unit tests leave it nil.
func (s *Service) SetProjectLookup(l ProjectLookup) { s.projectLookup = l }

// SetQuotaResolver injects the three-level effective-quota resolver.
// Production and FVT wire it; nil keeps the organization-only verdict.
func (s *Service) SetQuotaResolver(r tenancy.QuotaResolver) { s.quotaResolver = r }

// SetRoleGuard injects the membership role guard used to decide whether
// the caller administers the organization. Production and FVT wire it;
// nil keeps the transitional full access.
func (s *Service) SetRoleGuard(g *tenancy.RoleGuard) { s.roleGuard = g }

// adminRoles are the organization roles that may manage every key.
var adminRoles = map[string]bool{
	tenancy.RoleOwner:         true,
	tenancy.RoleAdmin:         true,
	tenancy.RolePlatformAdmin: true,
}

// keyAccess is the caller's authority over an organization's keys.
type keyAccess struct {
	// userID is the session user; empty on the transitional path.
	userID string
	// hasSession reports whether a valid session identified the caller.
	hasSession bool
	// admin reports whether the caller may manage every key.
	admin bool
}

// manages reports whether the caller may manage the given key.
func (a keyAccess) manages(row *APIKey) bool {
	if !a.hasSession || a.admin {
		return true
	}
	if row.OwnerUserID == nil {
		// A shared key belongs to the organization, not to one person.
		return false
	}
	return *row.OwnerUserID == a.userID
}

// resolveKeyAccess resolves the caller's authority in one organization.
// A session-less caller keeps the transitional full access; a session
// that cannot be read is a different story and is reported as an error.
func (s *Service) resolveKeyAccess(ctx context.Context, orgID string) (keyAccess, error) {
	userID, err := s.SessionUserID(ctx)
	if err != nil {
		code := apierrors.CodeOf(err)
		if code == apierrors.CodeSessionInvalid || code == apierrors.CodeUnauthorized {
			return keyAccess{}, nil
		}
		return keyAccess{}, err
	}
	access := keyAccess{userID: userID, hasSession: true}
	if s.roleGuard == nil {
		// Transitional wiring: without a role resolver the organization
		// boundary is the only gate, matching pre-ownership behavior.
		access.admin = true
		return access, nil
	}
	role, err := s.roleGuard.ResolveRole(ctx, orgID, userID)
	if err != nil {
		return keyAccess{}, err
	}
	access.admin = adminRoles[role]
	return access, nil
}

// requireKeyManage loads a key inside the organization and enforces the
// caller's authority over it. A key the caller may not manage answers
// CodeAPIKeyNotFound: a member must not learn that another member's key
// exists.
func (s *Service) requireKeyManage(ctx context.Context, orgID, keyID string) (*APIKey, keyAccess, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, keyAccess{}, err
	}
	row, err := repo.FindByIDAndOrganization(ctx, orgID, keyID)
	if err != nil {
		return nil, keyAccess{}, err
	}
	if row == nil {
		return nil, keyAccess{}, apierrors.New(apierrors.CodeAPIKeyNotFound)
	}
	access, err := s.resolveKeyAccess(ctx, orgID)
	if err != nil {
		return nil, keyAccess{}, err
	}
	if !access.manages(row) {
		return nil, keyAccess{}, apierrors.New(apierrors.CodeAPIKeyNotFound)
	}
	return row, access, nil
}

// requireAdminAccess rejects a caller who does not administer the
// organization. The transitional (session-less) path is permissive.
func (s *Service) requireAdminAccess(ctx context.Context, orgID string) (keyAccess, error) {
	access, err := s.resolveKeyAccess(ctx, orgID)
	if err != nil {
		return keyAccess{}, err
	}
	if access.hasSession && !access.admin {
		return keyAccess{}, apierrors.New(apierrors.CodeForbidden)
	}
	return access, nil
}

// resolveRequestedOwner validates and returns the owner a create or
// transfer request asks for. An empty owner means organization-shared,
// which only an administrator may choose.
func (s *Service) resolveRequestedOwner(ctx context.Context, orgID string, access keyAccess, requested string, shared bool) (*string, error) {
	owner := strings.TrimSpace(requested)
	if shared {
		if owner != "" {
			return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid,
				"auth: shared and owner_user_id are mutually exclusive")
		}
		if access.hasSession && !access.admin {
			return nil, apierrors.New(apierrors.CodeForbidden)
		}
		return nil, nil
	}
	if owner == "" {
		// The key belongs to the caller; a session-less caller keeps the
		// pre-ownership shared behavior.
		if access.hasSession {
			return &access.userID, nil
		}
		return nil, nil
	}
	if access.hasSession && owner != access.userID {
		if !access.admin {
			return nil, apierrors.New(apierrors.CodeForbidden)
		}
	}
	if s.projectLookup != nil {
		member, err := s.projectLookup.UserInOrg(ctx, orgID, owner)
		if err != nil {
			return nil, err
		}
		if !member {
			return nil, apierrors.New(apierrors.CodeUserNotFound)
		}
	}
	return &owner, nil
}

// resolveRequestedProject validates a requested project against the
// organization and, for a non-administrator, against the caller's
// project membership.
func (s *Service) resolveRequestedProject(ctx context.Context, orgID string, access keyAccess, requested string) (*string, error) {
	projectID := strings.TrimSpace(requested)
	if projectID == "" {
		return nil, nil
	}
	if s.projectLookup != nil {
		name, ok, err := s.projectLookup.ProjectInOrg(ctx, projectID, orgID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid,
				"auth: project %q does not belong to the organization", projectID)
		}
		_ = name
		if access.hasSession && !access.admin {
			member, err := s.projectLookup.UserInProject(ctx, projectID, access.userID)
			if err != nil {
				return nil, err
			}
			if !member {
				return nil, apierrors.New(apierrors.CodeForbidden)
			}
		}
	}
	return &projectID, nil
}

// UpdateAPIKeyOwnership transfers a key's personal owner and project
// inside its organization. Only an organization administrator may
// transfer a key; the organization and the secret never change.
func (s *Service) UpdateAPIKeyOwnership(ctx context.Context, req *authv1.UpdateAPIKeyOwnershipRequest) (*authv1.UpdateAPIKeyOwnershipResponse, error) {
	orgID, err := s.resolveOrgContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	if req.GetKeyId() == "" {
		return nil, apierrors.Newf(apierrors.CodeAPIKeyInvalid, "auth: key_id is required")
	}
	access, err := s.requireAdminAccess(ctx, orgID)
	if err != nil {
		return nil, err
	}
	owner, err := s.resolveRequestedOwner(ctx, orgID, access, req.GetOwnerUserId(), false)
	if err != nil {
		return nil, err
	}
	project, err := s.resolveRequestedProject(ctx, orgID, access, req.GetProjectId())
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.UpdateOwnershipByIDAndOrganization(ctx, orgID, req.GetKeyId(), owner, project)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, apierrors.New(apierrors.CodeAPIKeyNotFound)
	}
	// The verdict carries the ownership, so drop the cache entry to make
	// the new attribution visible within the cache TTL.
	if cache, cacheErr := s.verdictCacheFor(); cacheErr == nil {
		if delErr := cache.Delete(ctx, row.LookupHash); delErr != nil {
			logger.S().Warnw("auth: delete api key cache entry failed on ownership update",
				"key_id", row.ID, "err", delErr)
		}
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    access.userID,
		ActorType:      actorTypeFor(access),
		Action:         "api_key.ownership_updated",
		ResourceType:   "api_key",
		ResourceID:     row.ID,
		Result:         "success",
		Metadata:       ownershipMetadata(owner, project),
	})
	return &authv1.UpdateAPIKeyOwnershipResponse{
		Response: okResponse(),
		Key:      s.summarizeAPIKeyWithLookup(ctx, row),
	}, nil
}

// summarizeAPIKeyWithLookup fills the display names the console shows
// next to the owner and project ids.
func (s *Service) summarizeAPIKeyWithLookup(ctx context.Context, row *APIKey) *authv1.APIKeySummary {
	summary := summarizeAPIKey(row)
	if row.OwnerUserID != nil {
		if labels, err := s.ResolveUserLabels(ctx, []string{*row.OwnerUserID}); err == nil {
			summary.OwnerDisplayName = labels[*row.OwnerUserID]
		}
	}
	if s.projectLookup != nil && row.ProjectID != nil {
		if name, err := s.projectLookup.ProjectName(ctx, *row.ProjectID); err == nil {
			summary.ProjectDisplayName = name
		}
	}
	return summary
}

// ownershipMetadata renders the audit metadata for a transfer.
func ownershipMetadata(owner, project *string) string {
	parts := make([]string, 0, 2)
	if owner != nil {
		parts = append(parts, `"owner_user_id":"`+*owner+`"`)
	} else {
		parts = append(parts, `"owner_user_id":""`)
	}
	if project != nil {
		parts = append(parts, `"project_id":"`+*project+`"`)
	} else {
		parts = append(parts, `"project_id":""`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// actorTypeFor labels the audit actor: a session user or the
// transitional system path.
func actorTypeFor(access keyAccess) string {
	if access.hasSession {
		return "user"
	}
	return "system"
}

// actorIDFor returns the audit actor id, falling back to "system".
func actorIDFor(access keyAccess) string {
	if access.hasSession && access.userID != "" {
		return access.userID
	}
	return "system"
}
