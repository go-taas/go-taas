package auth

// Platform administrator role assignment.
//
// The role lives on the account (`users.platform_admin`), not on an
// organization membership: the platform owns no tenant, so no membership row
// can carry it. Handing it out is the most powerful action an operator can
// take — a platform administrator sees every organization, every user and every
// billing account — so the rules are deliberate:
//
//   - only a platform administrator may grant or revoke it (the admin surface
//     already rejects a user-realm session; this checks the role itself);
//   - the target must be active: granting it to a disabled account would arm
//     the role for the moment the account is re-enabled, which is a decision
//     nobody made;
//   - nobody revokes their own role: a mistaken click would otherwise lock the
//     operator out of the very console they are using, and there is no
//     "contact support" path inside the product;
//   - the last remaining platform administrator cannot be revoked by anyone:
//     an installation with zero administrators cannot be administered at all,
//     not even to fix the mistake;
//   - the target's sessions are revoked. A session caches the roles it was
//     minted with, so without this a demoted administrator keeps full access
//     until the token expires, and a promoted one would have to sign in twice
//     (once to get the role, once to get the admin realm).
//
// Both directions are idempotent: granting an existing administrator, or
// revoking a plain account, succeeds and changes nothing.

import (
	"context"
	"strings"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/services/audit"
)

// SessionReaper revokes every session an account holds. It is implemented by
// the session store and injected at wiring time (tests inject a fake).
type SessionReaper interface {
	// RevokeUserSessions deletes the account's sessions and returns how many
	// were removed.
	RevokeUserSessions(ctx context.Context, userID string) (int, error)
}

// SetSessionReaper injects the session reaper used when an account's roles
// change. Nil until wired: the role still changes, but existing sessions keep
// the roles they were minted with until they expire.
func (s *Service) SetSessionReaper(r SessionReaper) { s.sessionReaper = r }

// GrantPlatformAdmin makes another active account a platform administrator.
func (s *Service) GrantPlatformAdmin(ctx context.Context, req *authv1.GrantPlatformAdminRequest) (*authv1.GrantPlatformAdminResponse, error) {
	actor, err := s.requirePlatformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	target, err := repo.FindUser(ctx, strings.TrimSpace(req.GetUserId()))
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	if err != nil {
		return nil, err
	}
	if target.State != UserStateActive {
		return nil, apierrors.Newf(apierrors.CodeForbidden,
			"auth: only an active account can become a platform administrator")
	}
	if !target.PlatformAdmin {
		if err := repo.SetUserPlatformAdmin(ctx, target.ID, true); err != nil {
			return nil, err
		}
		target.PlatformAdmin = true
		s.reapSessions(ctx, target.ID)
		s.recordAudit(ctx, &audit.AuditEvent{
			ActorUserID:  actor,
			ActorType:    "user",
			Action:       "user.platform_admin_granted",
			ResourceType: "user",
			ResourceID:   target.ID,
			Result:       "success",
		})
	}
	return &authv1.GrantPlatformAdminResponse{
		Response: okResponse(),
		User:     s.summarizeUser(ctx, target),
	}, nil
}

// RevokePlatformAdmin removes the role from another account.
func (s *Service) RevokePlatformAdmin(ctx context.Context, req *authv1.RevokePlatformAdminRequest) (*authv1.RevokePlatformAdminResponse, error) {
	actor, err := s.requirePlatformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	target, err := repo.FindUser(ctx, strings.TrimSpace(req.GetUserId()))
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	if err != nil {
		return nil, err
	}
	if target.ID == actor {
		return nil, apierrors.Newf(apierrors.CodeForbidden,
			"auth: a platform administrator cannot revoke its own role")
	}
	if target.PlatformAdmin {
		count, countErr := repo.CountPlatformAdmins(ctx)
		if countErr != nil {
			return nil, countErr
		}
		if count <= 1 {
			return nil, apierrors.Newf(apierrors.CodeForbidden,
				"auth: the last platform administrator cannot be revoked")
		}
		if err := repo.SetUserPlatformAdmin(ctx, target.ID, false); err != nil {
			return nil, err
		}
		target.PlatformAdmin = false
		s.reapSessions(ctx, target.ID)
		s.recordAudit(ctx, &audit.AuditEvent{
			ActorUserID:  actor,
			ActorType:    "user",
			Action:       "user.platform_admin_revoked",
			ResourceType: "user",
			ResourceID:   target.ID,
			Result:       "success",
		})
	}
	return &authv1.RevokePlatformAdminResponse{
		Response: okResponse(),
		User:     s.summarizeUser(ctx, target),
	}, nil
}

// requirePlatformAdmin resolves the caller from the session and checks the
// account carries the role. A caller without the role gets 10036, never a
// silent no-op: an authorization failure must be visible to the console.
func (s *Service) requirePlatformAdmin(ctx context.Context) (string, error) {
	sess, err := s.sessionFromContext(ctx)
	if err != nil {
		return "", err
	}
	if sess.UserID == "" {
		return "", apierrors.New(apierrors.CodeSessionInvalid)
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return "", err
	}
	caller, err := repo.FindUser(ctx, sess.UserID)
	if isRecordNotFound(err) {
		return "", apierrors.New(apierrors.CodeUserNotFound)
	}
	if err != nil {
		return "", err
	}
	if !caller.PlatformAdmin {
		return "", apierrors.Newf(apierrors.CodeForbidden,
			"auth: only a platform administrator can change platform administrator roles")
	}
	return caller.ID, nil
}

// reapSessions revokes the account's sessions after a role change. A failure is
// logged, never fatal: the role is already stored, and the next login reflects
// it — failing the RPC would leave the caller unsure whether the change applied.
func (s *Service) reapSessions(ctx context.Context, userID string) {
	if s.sessionReaper == nil {
		return
	}
	if _, err := s.sessionReaper.RevokeUserSessions(ctx, userID); err != nil {
		logger.S().Warnw("auth: revoking sessions after a role change failed",
			"user", userID, "err", err)
	}
}
