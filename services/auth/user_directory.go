// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// User directory service RPCs (admin surface): the platform-wide user
// registry with lifecycle state transitions. Users are created by SSO
// JIT provisioning or invitations; this surface gives operators a
// read-only directory plus disable/enable lifecycle control.
package auth

import (
	"context"
	"strings"

	"github.com/google/uuid"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ---- User directory (admin surface) ----

// ListUsers returns the platform user directory.
func (s *Service) ListUsers(ctx context.Context, req *authv1.ListUsersRequest) (*authv1.ListUsersResponse, error) {
	state := strings.TrimSpace(req.GetState())
	if state != "" && state != UserStateActive && state != UserStateDisabled {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListUsers(ctx, UserFilter{State: state}, offset, limit)
	if err != nil {
		return nil, err
	}
	users := make([]*authv1.UserSummary, 0, len(rows))
	for _, row := range rows {
		users = append(users, s.summarizeUser(ctx, row))
	}
	return &authv1.ListUsersResponse{
		Response: okResponse(),
		Users:    users,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// GetUser returns one platform user.
func (s *Service) GetUser(ctx context.Context, req *authv1.GetUserRequest) (*authv1.GetUserResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindUser(ctx, req.GetUserId())
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &authv1.GetUserResponse{
		Response: okResponse(),
		User:     s.summarizeUser(ctx, row),
	}, nil
}

// DisableUser flips a user to the disabled state.
func (s *Service) DisableUser(ctx context.Context, req *authv1.DisableUserRequest) (*authv1.DisableUserResponse, error) {
	user, err := s.setUserState(ctx, req.GetUserId(), UserStateDisabled)
	if err != nil {
		return nil, err
	}
	return &authv1.DisableUserResponse{Response: okResponse(), User: user}, nil
}

// EnableUser flips a user back to the active state.
func (s *Service) EnableUser(ctx context.Context, req *authv1.EnableUserRequest) (*authv1.EnableUserResponse, error) {
	user, err := s.setUserState(ctx, req.GetUserId(), UserStateActive)
	if err != nil {
		return nil, err
	}
	return &authv1.EnableUserResponse{Response: okResponse(), User: user}, nil
}

// setUserState applies a lifecycle transition and returns the updated
// row. Unknown user → CodeUserNotFound.
func (s *Service) setUserState(ctx context.Context, userID, state string) (*authv1.UserSummary, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	row, err := repo.SetUserState(ctx, userID, state)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	if err != nil {
		return nil, err
	}
	return s.summarizeUser(ctx, row), nil
}

// DeleteUser permanently removes a platform user and its identity
// bindings. Unknown user → CodeUserNotFound.
func (s *Service) DeleteUser(ctx context.Context, req *authv1.DeleteUserRequest) (*authv1.DeleteUserResponse, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	if err := repo.DeleteUser(ctx, req.GetUserId()); err != nil {
		if isRecordNotFound(err) {
			return nil, apierrors.New(apierrors.CodeUserNotFound)
		}
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  req.GetUserId(),
		ActorType:    "user",
		Action:       "user.delete",
		ResourceType: "user",
		ResourceID:   req.GetUserId(),
		Result:       "success",
	})
	return &authv1.DeleteUserResponse{Response: okResponse()}, nil
}

// summarizeUser maps a user row to the directory summary, computing the
// organization count from the membership resolver when wired.
func (s *Service) summarizeUser(ctx context.Context, row *User) *authv1.UserSummary {
	// The directory shows where an account belongs, not how many places it
	// has joined: an account belongs to at most one organization and one
	// project. A resolution failure leaves the fields empty, which the
	// console renders as "-".
	var affiliation tenancy.Affiliation
	if s.membershipResolver != nil {
		if found, err := s.membershipResolver.UserAffiliation(ctx, row.ID); err == nil {
			affiliation = found
		}
	}
	return &authv1.UserSummary{
		UserId:           row.ID,
		Username:         row.Username,
		DisplayName:      row.DisplayName,
		Email:            row.Email,
		State:            row.State,
		OrganizationId:   affiliation.OrganizationID,
		OrganizationName: affiliation.OrganizationName,
		ProjectId:        affiliation.ProjectID,
		ProjectName:      affiliation.ProjectName,
		CreatedAt:        row.CreatedAt.Unix(),
		UpdatedAt:        row.UpdatedAt.Unix(),
	}
}

// ResolveUserID resolves a caller-supplied user id or username to the
// canonical user id. It is the tenancy service's user resolver seam
// (the SetRoleGuard injection pattern): member-management RPCs accept
// either a UUID or a username and resolve it here, so a typo or a
// username never reaches the DB as an invalid UUID. Unknown input maps
// to CodeUserNotFound.
func (s *Service) ResolveUserID(ctx context.Context, idOrUsername string) (string, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return "", err
	}
	// A valid UUID is treated as a user id directly.
	if _, err := uuid.Parse(idOrUsername); err == nil {
		row, err := repo.FindUser(ctx, idOrUsername)
		if isRecordNotFound(err) {
			return "", apierrors.New(apierrors.CodeUserNotFound)
		}
		if err != nil {
			return "", err
		}
		return row.ID, nil
	}
	// Otherwise resolve by username.
	row, err := repo.FindUserByUsername(ctx, idOrUsername)
	if isRecordNotFound(err) {
		return "", apierrors.New(apierrors.CodeUserNotFound)
	}
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

// ResolveUserLabels returns a human-readable label for each requested
// user id: the user's display name, falling back to the username. It is
// the tenancy service's display-name seam, so member and request rosters
// show people instead of UUIDs. Unknown ids are omitted from the map
// (the caller keeps a neutral fallback) and never fail the batch.
func (s *Service) ResolveUserLabels(ctx context.Context, userIDs []string) (map[string]string, error) {
	labels := make(map[string]string, len(userIDs))
	if len(userIDs) == 0 {
		return labels, nil
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListUsersByIDs(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.DisplayName != "" {
			labels[row.ID] = row.DisplayName
			continue
		}
		labels[row.ID] = row.Username
	}
	return labels, nil
}

// ListActiveUsers returns every active account, newest first. It is the
// tenancy service's roster-candidate directory seam: the tenancy module owns
// the membership tables and subtracts the accounts that already joined, while
// the account rows stay here.
func (s *Service) ListActiveUsers(ctx context.Context) ([]tenancy.CandidateUser, error) {
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	rows, _, err := repo.ListUsers(ctx, UserFilter{State: UserStateActive}, 0, maxCandidateUsers)
	if err != nil {
		return nil, err
	}
	out := make([]tenancy.CandidateUser, 0, len(rows))
	for _, row := range rows {
		out = append(out, tenancy.CandidateUser{
			UserID:      row.ID,
			Username:    row.Username,
			DisplayName: row.DisplayName,
			Email:       row.Email,
		})
	}
	return out, nil
}

// maxCandidateUsers bounds the candidate pick-list. A roster pick-list is a
// human choice, not a report, so a generous page is enough; beyond it the
// administrator adds the person by user id.
const maxCandidateUsers = 500
