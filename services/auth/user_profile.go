// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// User profile (user surface): the caller's own profile with mutable
// display name and avatar. The email is mutable too, but only after the
// new address proves ownership with a verification code. The username is
// immutable.
package auth

import (
	"context"
	"strings"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// maxDisplayNameLen bounds a profile display name.
const maxDisplayNameLen = 128

// maxEmailLen bounds a profile email address.
const maxEmailLen = 256

// maxAvatarLen bounds a profile avatar: a base64 data URL of up to
// ~512 KiB decoded image (base64 expands by ~4/3).
const maxAvatarLen = 700_000

// GetUserProfile returns the caller's own profile.
func (s *Service) GetUserProfile(ctx context.Context, _ *authv1.GetUserProfileRequest) (*authv1.GetUserProfileResponse, error) {
	userID, err := s.SessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	user, err := repo.FindUser(ctx, userID)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &authv1.GetUserProfileResponse{
		Response: okResponse(),
		Profile:  summarizeProfile(user),
	}, nil
}

// UpdateUserProfile updates the caller's mutable profile fields
// (display name, avatar, and email after verification). Username is
// immutable.
func (s *Service) UpdateUserProfile(ctx context.Context, req *authv1.UpdateUserProfileRequest) (*authv1.UpdateUserProfileResponse, error) {
	userID, err := s.SessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(req.GetDisplayName())
	avatar := strings.TrimSpace(req.GetAvatar())
	if len(displayName) > maxDisplayNameLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	if len(avatar) > maxAvatarLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	// An avatar is either empty or an inline base64 image data URL; it is
	// stored in the database, never fetched from a remote URL.
	if avatar != "" && !strings.HasPrefix(avatar, "data:image/") {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	// Email change: the new address must be proven with a code sent to
	// it (purpose "change_email") when email verification is enabled, and
	// must not already belong to another account.
	email := strings.TrimSpace(req.GetEmail())
	if email != "" && len(email) > maxEmailLen {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	if email != "" {
		if err := s.applyEmailChange(ctx, repo, userID, email, req.GetEmailCode()); err != nil {
			return nil, err
		}
	}
	user, err := repo.UpdateUserProfile(ctx, userID, displayName, avatar)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &authv1.UpdateUserProfileResponse{
		Response: okResponse(),
		Profile:  summarizeProfile(user),
	}, nil
}

// applyEmailChange replaces the caller's email after the new address
// proves ownership. Verification (when enabled) uses the "change_email"
// code sent to the NEW address; the address must not belong to another
// account. Re-submitting the current address is a no-op.
func (s *Service) applyEmailChange(ctx context.Context, repo *SSORepository, userID, email, code string) error {
	current, err := repo.FindUser(ctx, userID)
	if isRecordNotFound(err) {
		return apierrors.New(apierrors.CodeUserNotFound)
	}
	if err != nil {
		return err
	}
	if strings.EqualFold(current.Email, email) {
		return nil
	}
	if err := s.verifyEmailCode(ctx, email, emailCodePurposeChangeEmail, strings.TrimSpace(code)); err != nil {
		return err
	}
	// The email column is unique, so the constraint is the authoritative
	// duplicate guard (no read-then-write race). A collision maps to
	// USER_EXISTS for a stable client contract.
	if err := repo.UpdateUserEmail(ctx, userID, email); err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeUserExists)
		}
		return err
	}
	return nil
}

// summarizeProfile maps a user row to the profile message.
func summarizeProfile(u *User) *authv1.UserProfile {
	return &authv1.UserProfile{
		UserId:      u.ID,
		Username:    u.Username,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Avatar:      u.Avatar,
		CreatedAt:   u.CreatedAt.Unix(),
	}
}