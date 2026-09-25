package tenancy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// emailRegex is a light email shape check (feature #10, FR2.1).
var emailRegex = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// invitationTokenLength is the raw invitation token length in bytes.
const invitationTokenLength = 32

// defaultInvitationTTL is the 7-day default invitation expiry (AD5).
const defaultInvitationTTL = 7 * 24 * time.Hour

// invitationTokenAlphabet is the base62 alphabet for invitation tokens.
const invitationTokenAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// generateInvitationToken returns a fresh random base62 token.
func generateInvitationToken() (string, error) {
	buf := make([]byte, invitationTokenLength)
	for i := range buf {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		buf[i] = invitationTokenAlphabet[int(b[0])%len(invitationTokenAlphabet)]
	}
	return string(buf), nil
}

// hashInvitationToken returns the SHA-256 hex digest of a raw token
// (AD4). The token is high-entropy and single-use, so a plain digest is
// sufficient for the unique lookup key; the raw token is never stored.
func hashInvitationToken(raw string) (string, error) {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}

// membershipRepo lazily wires and returns the membership repository.
func (s *Service) membershipRepo() (*MembershipRepository, error) {
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	return NewMembershipRepository(db), nil
}

// ListOrgMembers returns the org's roster (FR1.1).
func (s *Service) ListOrgMembers(ctx context.Context, req *tenancyv1.ListOrgMembersRequest) (*tenancyv1.ListOrgMembersResponse, error) {
	orgID := req.GetOrganizationId()
	if err := s.requireAdminOrOwner(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListMembers(ctx, orgID, offset, limit)
	if err != nil {
		return nil, err
	}
	members := make([]*tenancyv1.OrgMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, summarizeMember(row))
	}
	return &tenancyv1.ListOrgMembersResponse{
		Response: okResponse(),
		Members:  members,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// AddOrgMember adds a member with a role (FR1.2).
func (s *Service) AddOrgMember(ctx context.Context, req *tenancyv1.AddOrgMemberRequest) (*tenancyv1.AddOrgMemberResponse, error) {
	orgID := req.GetOrganizationId()
	if err := s.requireAdminOrOwner(ctx, orgID); err != nil {
		return nil, err
	}
	userID := strings.TrimSpace(req.GetUserId())
	role := strings.TrimSpace(req.GetRole())
	if userID == "" {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	if !validAssignableRole(role) {
		return nil, apierrors.New(apierrors.CodeRoleInvalid)
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	m := &OrgMember{
		OrganizationID: orgID,
		UserID:         userID,
		Role:           role,
		JoinedAt:       time.Now().UTC(),
	}
	if err := repo.AddMember(ctx, m); err != nil {
		return nil, err
	}
	// Feature #15: record the successful member add best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    userID,
		ActorType:      "user",
		Action:         "member.add",
		ResourceType:   "member",
		ResourceID:     userID,
		Result:         "success",
	})
	return &tenancyv1.AddOrgMemberResponse{
		Response: okResponse(),
		Member:   summarizeMember(m),
	}, nil
}

// SetOrgMemberRole changes a member's role (FR1.3).
func (s *Service) SetOrgMemberRole(ctx context.Context, req *tenancyv1.SetOrgMemberRoleRequest) (*tenancyv1.SetOrgMemberRoleResponse, error) {
	orgID := req.GetOrganizationId()
	if err := s.requireAdminOrOwner(ctx, orgID); err != nil {
		return nil, err
	}
	userID := req.GetUserId()
	role := strings.TrimSpace(req.GetRole())
	if !validAssignableRole(role) {
		return nil, apierrors.New(apierrors.CodeRoleInvalid)
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	m, err := repo.FindMember(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, apierrors.New(apierrors.CodeMemberNotFound)
	}
	if m.Role == RoleOwner {
		return nil, apierrors.New(apierrors.CodeOwnerProtected)
	}
	if err := repo.SetMemberRole(ctx, orgID, userID, role); err != nil {
		return nil, err
	}
	m.Role = role
	// Feature #15: record the successful role change best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    userID,
		ActorType:      "user",
		Action:         "member.role_change",
		ResourceType:   "member",
		ResourceID:     userID,
		Result:         "success",
	})
	return &tenancyv1.SetOrgMemberRoleResponse{
		Response: okResponse(),
		Member:   summarizeMember(m),
	}, nil
}

// RemoveOrgMember removes a member (FR1.4).
func (s *Service) RemoveOrgMember(ctx context.Context, req *tenancyv1.RemoveOrgMemberRequest) (*tenancyv1.RemoveOrgMemberResponse, error) {
	orgID := req.GetOrganizationId()
	if err := s.requireAdminOrOwner(ctx, orgID); err != nil {
		return nil, err
	}
	userID := req.GetUserId()
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	m, err := repo.FindMember(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, apierrors.New(apierrors.CodeMemberNotFound)
	}
	if m.Role == RoleOwner {
		return nil, apierrors.New(apierrors.CodeOwnerProtected)
	}
	if err := repo.RemoveMember(ctx, orgID, userID); err != nil {
		return nil, err
	}
	// Feature #15: record the successful member removal best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    userID,
		ActorType:      "user",
		Action:         "member.remove",
		ResourceType:   "member",
		ResourceID:     userID,
		Result:         "success",
	})
	return &tenancyv1.RemoveOrgMemberResponse{Response: okResponse()}, nil
}

// CreateInvitation invites a person by email with a role (FR2.1).
func (s *Service) CreateInvitation(ctx context.Context, req *tenancyv1.CreateInvitationRequest) (*tenancyv1.CreateInvitationResponse, error) {
	orgID := req.GetOrganizationId()
	if err := s.requireAdminOrOwner(ctx, orgID); err != nil {
		return nil, err
	}
	email := strings.TrimSpace(req.GetEmail())
	role := strings.TrimSpace(req.GetRole())
	if !emailRegex.MatchString(email) || !validAssignableRole(role) {
		return nil, apierrors.New(apierrors.CodeRoleInvalid)
	}
	if req.GetExpiresIn() < 0 {
		return nil, apierrors.New(apierrors.CodeRoleInvalid)
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	pending, err := repo.PendingInvitationExists(ctx, orgID, email)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, apierrors.New(apierrors.CodeInvitationExists)
	}
	raw, err := generateInvitationToken()
	if err != nil {
		return nil, err
	}
	hash, err := hashInvitationToken(raw)
	if err != nil {
		return nil, err
	}
	ttl := defaultInvitationTTL
	if cfg := config.GetConfig(); cfg != nil && cfg.Tenancy.InvitationTTL > 0 {
		ttl = cfg.Tenancy.InvitationTTL
	}
	if req.GetExpiresIn() > 0 {
		ttl = time.Duration(req.GetExpiresIn()) * time.Second
	}
	now := time.Now().UTC()
	inv := &Invitation{
		ID:             uuid.NewString(),
		OrganizationID: orgID,
		Email:          email,
		Role:           role,
		TokenHash:      hash,
		Status:         InvitationPending,
		ExpiresAt:      now.Add(ttl).Unix(),
		CreatedBy:      req.GetOrganizationId(), // placeholder; caller id resolved by the guard
		CreatedAt:      now,
	}
	if err := repo.CreateInvitation(ctx, inv); err != nil {
		return nil, err
	}
	return &tenancyv1.CreateInvitationResponse{
		Response:   okResponse(),
		Invitation: summarizeInvitation(inv),
		Token:      raw,
	}, nil
}

// ListInvitations returns the org's invitation pipeline (FR2.2).
func (s *Service) ListInvitations(ctx context.Context, req *tenancyv1.ListInvitationsRequest) (*tenancyv1.ListInvitationsResponse, error) {
	orgID := req.GetOrganizationId()
	if err := s.requireAdminOrOwner(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListInvitations(ctx, orgID, req.GetStatus(), offset, limit)
	if err != nil {
		return nil, err
	}
	invitations := make([]*tenancyv1.Invitation, 0, len(rows))
	for _, row := range rows {
		invitations = append(invitations, summarizeInvitation(row))
	}
	return &tenancyv1.ListInvitationsResponse{
		Response:    okResponse(),
		Invitations: invitations,
		PageMeta:    &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// RevokeInvitation cancels a pending invitation (FR2.3).
func (s *Service) RevokeInvitation(ctx context.Context, req *tenancyv1.RevokeInvitationRequest) (*tenancyv1.RevokeInvitationResponse, error) {
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	inv, err := repo.FindInvitationByID(ctx, req.GetInvitationId())
	if err != nil {
		return nil, err
	}
	if inv == nil || inv.Status != InvitationPending {
		return nil, apierrors.New(apierrors.CodeInvitationNotFound)
	}
	if err := s.requireAdminOrOwner(ctx, inv.OrganizationID); err != nil {
		return nil, err
	}
	if err := repo.SetInvitationStatus(ctx, inv.ID, InvitationRevoked); err != nil {
		return nil, err
	}
	inv.Status = InvitationRevoked
	return &tenancyv1.RevokeInvitationResponse{
		Response:   okResponse(),
		Invitation: summarizeInvitation(inv),
	}, nil
}

// ResendInvitation issues a new token and refreshes expiry (FR2.4).
func (s *Service) ResendInvitation(ctx context.Context, req *tenancyv1.ResendInvitationRequest) (*tenancyv1.ResendInvitationResponse, error) {
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	inv, err := repo.FindInvitationByID(ctx, req.GetInvitationId())
	if err != nil {
		return nil, err
	}
	if inv == nil || inv.Status != InvitationPending {
		return nil, apierrors.New(apierrors.CodeInvitationNotFound)
	}
	if err := s.requireAdminOrOwner(ctx, inv.OrganizationID); err != nil {
		return nil, err
	}
	raw, err := generateInvitationToken()
	if err != nil {
		return nil, err
	}
	hash, err := hashInvitationToken(raw)
	if err != nil {
		return nil, err
	}
	ttl := defaultInvitationTTL
	if cfg := config.GetConfig(); cfg != nil && cfg.Tenancy.InvitationTTL > 0 {
		ttl = cfg.Tenancy.InvitationTTL
	}
	expiresAt := time.Now().UTC().Add(ttl).Unix()
	if err := repo.UpdateInvitationToken(ctx, inv.ID, hash, expiresAt); err != nil {
		return nil, err
	}
	inv.TokenHash = hash
	inv.ExpiresAt = expiresAt
	return &tenancyv1.ResendInvitationResponse{
		Response:   okResponse(),
		Invitation: summarizeInvitation(inv),
		Token:      raw,
	}, nil
}

// AcceptInvitation joins the org for the authenticated caller (FR2.5).
func (s *Service) AcceptInvitation(ctx context.Context, req *tenancyv1.AcceptInvitationRequest) (*tenancyv1.AcceptInvitationResponse, error) {
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	hash, err := hashInvitationToken(req.GetToken())
	if err != nil {
		return nil, err
	}
	inv, err := repo.FindInvitationByTokenHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if inv == nil || inv.Status != InvitationPending {
		return nil, apierrors.New(apierrors.CodeInvitationNotFound)
	}
	if time.Now().Unix() > inv.ExpiresAt {
		return nil, apierrors.New(apierrors.CodeInvitationExpired)
	}
	// The caller must be the invitee (email match).
	email, err := s.sessionUserEmail(ctx)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(email, inv.Email) {
		return nil, apierrors.New(apierrors.CodeForbidden)
	}
	m := &OrgMember{
		OrganizationID: inv.OrganizationID,
		UserID:         userID,
		Role:           inv.Role,
		JoinedAt:       time.Now().UTC(),
	}
	if err := repo.AddMember(ctx, m); err != nil {
		// A duplicate membership is tolerated (already a member).
		if ae, ok := apierrors.As(err); !ok || ae.Code != apierrors.CodeMemberExists {
			return nil, err
		}
	}
	if err := repo.SetInvitationStatus(ctx, inv.ID, InvitationAccepted); err != nil {
		return nil, err
	}
	return &tenancyv1.AcceptInvitationResponse{
		Response:       okResponse(),
		OrganizationId: inv.OrganizationID,
		Role:           inv.Role,
	}, nil
}

// RejectInvitation declines the org for the authenticated caller
// (FR2.6).
func (s *Service) RejectInvitation(ctx context.Context, req *tenancyv1.RejectInvitationRequest) (*tenancyv1.RejectInvitationResponse, error) {
	if _, err := s.sessionUserID(ctx); err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	hash, err := hashInvitationToken(req.GetToken())
	if err != nil {
		return nil, err
	}
	inv, err := repo.FindInvitationByTokenHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if inv == nil || inv.Status != InvitationPending {
		return nil, apierrors.New(apierrors.CodeInvitationNotFound)
	}
	if time.Now().Unix() > inv.ExpiresAt {
		return nil, apierrors.New(apierrors.CodeInvitationExpired)
	}
	if err := repo.SetInvitationStatus(ctx, inv.ID, InvitationRejected); err != nil {
		return nil, err
	}
	return &tenancyv1.RejectInvitationResponse{Response: okResponse()}, nil
}

// requireAdminOrOwner enforces the admin/owner gate for member and
// invitation management (AD7). It resolves the caller from the session
// and the org from the request.
func (s *Service) requireAdminOrOwner(ctx context.Context, orgID string) error {
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return err
	}
	if s.roleGuard == nil {
		return nil
	}
	return s.roleGuard.RequireAdminOrOwner(ctx, orgID, userID)
}

// sessionUserID resolves the authenticated caller's user id.
func (s *Service) sessionUserID(ctx context.Context) (string, error) {
	if s.sessionResolver == nil {
		return "", apierrors.New(apierrors.CodeSessionInvalid)
	}
	return s.sessionResolver.SessionUserID(ctx)
}

// sessionUserEmail resolves the authenticated caller's email.
func (s *Service) sessionUserEmail(ctx context.Context) (string, error) {
	if s.sessionResolver == nil {
		return "", apierrors.New(apierrors.CodeSessionInvalid)
	}
	return s.sessionResolver.SessionUserEmail(ctx)
}

// validAssignableRole reports whether role is assignable via the member
// and invitation APIs (admin/member/viewer; owner is never assignable).
func validAssignableRole(role string) bool {
	switch role {
	case RoleAdmin, RoleMember, RoleViewer:
		return true
	default:
		return false
	}
}

// summarizeMember maps a member row to the proto message.
func summarizeMember(m *OrgMember) *tenancyv1.OrgMember {
	return &tenancyv1.OrgMember{
		UserId:      m.UserID,
		DisplayName: m.UserID,
		Role:        m.Role,
		JoinedAt:    m.JoinedAt.Unix(),
	}
}

// summarizeInvitation maps an invitation row to the proto message.
func summarizeInvitation(inv *Invitation) *tenancyv1.Invitation {
	return &tenancyv1.Invitation{
		InvitationId: inv.ID,
		Email:        inv.Email,
		Role:         inv.Role,
		Status:       inv.Status,
		ExpiresAt:    inv.ExpiresAt,
		CreatedBy:    inv.CreatedBy,
		CreatedAt:    inv.CreatedAt.Unix(),
	}
}
