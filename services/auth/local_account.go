// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// Local account registration and login (CreateUser/Login): a
// username/password account whose password is stored as an Argon2id
// hash, alongside the SSO federation flow.
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// usernameMaxLen bounds a local account username.
const usernameMaxLen = 128

// passwordMinLen is the minimum local account password length.
const passwordMinLen = 8

// passwordMaxLen bounds a local account password.
const passwordMaxLen = 256

// CreateUser registers a local account. The password is hashed with
// Argon2id and never stored in plaintext. The username must be unique.
// When email verification is enabled, the email and its verification
// code are required.
func (s *Service) CreateUser(ctx context.Context, req *authv1.CreateUserRequest) (*authv1.CreateUserResponse, error) {
	username := strings.TrimSpace(req.GetUsername())
	password := req.GetPassword()
	email := strings.TrimSpace(req.GetEmail())
	if username == "" || len(username) > usernameMaxLen {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	if len(password) < passwordMinLen || len(password) > passwordMaxLen {
		return nil, apierrors.New(apierrors.CodePasswordPolicy)
	}
	// When email verification is enabled, the email and its code are
	// required.
	if emailVerificationEnabled() {
		if email == "" {
			return nil, apierrors.New(apierrors.CodeUserNotFound)
		}
		if err := s.verifyEmailCode(ctx, email, emailCodePurposeRegister, req.GetEmailCode()); err != nil {
			return nil, err
		}
	}
	hash, err := HashPassword(password, s.hashParamsFor())
	if err != nil {
		return nil, err
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	user := &User{
		ID:           uuid.NewString(),
		Username:     username,
		Email:        email,
		PasswordHash: hash,
		State:        UserStateActive,
	}
	if err := repo.CreateUser(ctx, user); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  user.ID,
		ActorType:    "user",
		Action:       "user.create",
		ResourceType: "user",
		ResourceID:   user.ID,
		Result:       "success",
	})
	return &authv1.CreateUserResponse{
		Response: okResponse(),
		UserId:   user.ID,
	}, nil
}

// Login authenticates a local account and issues a session token. The
// password is verified against the stored Argon2id hash; on success a
// session is minted with roles and orgs derived from org_members.
func (s *Service) Login(ctx context.Context, req *authv1.LoginRequest) (*authv1.LoginResponse, error) {
	username := strings.TrimSpace(req.GetUsername())
	password := req.GetPassword()
	if username == "" || password == "" {
		return nil, apierrors.New(apierrors.CodeInvalidCredentials)
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return nil, err
	}
	user, err := repo.FindUserByUsername(ctx, username)
	if isRecordNotFound(err) {
		return nil, apierrors.New(apierrors.CodeInvalidCredentials)
	}
	if err != nil {
		return nil, err
	}
	if user.State == UserStateDisabled {
		return nil, apierrors.New(apierrors.CodeUserDisabled)
	}
	if user.PasswordHash == "" || !VerifyPassword(password, user.PasswordHash) {
		return nil, apierrors.New(apierrors.CodeInvalidCredentials)
	}
	// When email verification is enabled, the user's email and its code
	// are required.
	if emailVerificationEnabled() {
		if user.Email == "" {
			return nil, apierrors.New(apierrors.CodeInvalidCredentials)
		}
		if err := s.verifyEmailCode(ctx, user.Email, emailCodePurposeLogin, req.GetEmailCode()); err != nil {
			return nil, err
		}
	}

	// Derive roles and orgs from org_members (authoritative for local
	// accounts; there are no IdP claims).
	roles, accessibleOrgs, activeOrg, err := s.localMembership(ctx, user)
	if err != nil {
		return nil, err
	}
	realm := RealmUser
	if s.isAdminRole(roles) {
		realm = RealmAdmin
	}
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
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: activeOrg,
		ActorUserID:    user.ID,
		ActorType:      "user",
		Action:         "auth.login",
		ResourceType:   "session",
		ResourceID:     sessionID,
		Result:         "success",
	})
	return &authv1.LoginResponse{
		Response:     okResponse(),
		SessionToken: sessionID,
		ExpiresAt:    expiresAt,
	}, nil
}

// localMembership derives a local account's roles and accessible orgs
// from org_members. When no membership resolver is wired the user has
// no orgs (roles empty).
func (s *Service) localMembership(ctx context.Context, user *User) ([]string, []string, string, error) {
	if s.membershipResolver == nil {
		return nil, nil, "", nil
	}
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
	activeOrg := ""
	if len(accessibleOrgs) > 0 {
		activeOrg = accessibleOrgs[0]
	}
	return roles, accessibleOrgs, activeOrg, nil
}