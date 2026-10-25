package tenancy

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// CreateJoinRequest submits a request to join an organization or a
// project. A user belongs to exactly one organization, so an
// organization request is rejected when the user is already a member of
// any organization. A project request requires membership of the
// project's owning organization.
func (s *Service) CreateJoinRequest(ctx context.Context, req *tenancyv1.CreateJoinRequestRequest) (*tenancyv1.CreateJoinRequestResponse, error) {
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	targetType := strings.TrimSpace(req.GetTargetType())
	targetID := strings.TrimSpace(req.GetTargetId())
	if targetID == "" || len(req.GetMessage()) > 512 {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	switch targetType {
	case JoinTargetOrganization:
		// One-org rule: a user may belong to exactly one organization.
		orgs, err := repo.ListMemberOrgs(ctx, userID)
		if err != nil {
			return nil, err
		}
		if len(orgs) > 0 {
			return nil, apierrors.New(apierrors.CodeMemberExists)
		}
		orgRepo, err := s.repository()
		if err != nil {
			return nil, err
		}
		if _, err := orgRepo.FindOrganization(ctx, targetID); isRecordNotFound(err) {
			return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
		} else if err != nil {
			return nil, err
		}
	case JoinTargetProject:
		if err := s.requireOrgMembershipOfProject(ctx, targetID, userID); err != nil {
			return nil, err
		}
	default:
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	j := &JoinRequest{
		ID:         uuid.NewString(),
		UserID:     userID,
		TargetType: targetType,
		TargetID:   targetID,
		Status:     JoinStatusPending,
		Message:    strings.TrimSpace(req.GetMessage()),
	}
	if err := repo.CreateJoinRequest(ctx, j); err != nil {
		return nil, err
	}
	return &tenancyv1.CreateJoinRequestResponse{
		Response: okResponse(),
		Request:  summarizeJoinRequest(j, s.userLabel(ctx, j.UserID)),
	}, nil
}

// ListMyJoinRequests returns the caller's own join requests.
func (s *Service) ListMyJoinRequests(ctx context.Context, _ *tenancyv1.ListMyJoinRequestsRequest) (*tenancyv1.ListMyJoinRequestsResponse, error) {
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListJoinRequestsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	labels := s.joinRequestLabels(ctx, rows)
	out := make([]*tenancyv1.JoinRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, summarizeJoinRequest(row, labels[row.UserID]))
	}
	return &tenancyv1.ListMyJoinRequestsResponse{Response: okResponse(), Requests: out}, nil
}

// ListJoinRequests returns join requests for a target the caller
// administers.
func (s *Service) ListJoinRequests(ctx context.Context, req *tenancyv1.ListJoinRequestsRequest) (*tenancyv1.ListJoinRequestsResponse, error) {
	targetType := strings.TrimSpace(req.GetTargetType())
	targetID := strings.TrimSpace(req.GetTargetId())
	if targetID == "" || (targetType != JoinTargetOrganization && targetType != JoinTargetProject) {
		return nil, apierrors.New(apierrors.CodeTenancyInvalid)
	}
	if err := s.requireTargetAdmin(ctx, targetType, targetID); err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListJoinRequests(ctx, targetType, targetID, strings.TrimSpace(req.GetStatus()))
	if err != nil {
		return nil, err
	}
	labels := s.joinRequestLabels(ctx, rows)
	out := make([]*tenancyv1.JoinRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, summarizeJoinRequest(row, labels[row.UserID]))
	}
	return &tenancyv1.ListJoinRequestsResponse{Response: okResponse(), Requests: out}, nil
}

// ApproveJoinRequest approves a pending request and adds the member.
func (s *Service) ApproveJoinRequest(ctx context.Context, req *tenancyv1.ApproveJoinRequestRequest) (*tenancyv1.ApproveJoinRequestResponse, error) {
	j, err := s.pendingJoinRequest(ctx, req.GetRequestId())
	if err != nil {
		return nil, err
	}
	adminID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	switch j.TargetType {
	case JoinTargetOrganization:
		// Re-check the one-org rule: the user may have joined another
		// organization while the request was pending.
		orgs, err := repo.ListMemberOrgs(ctx, j.UserID)
		if err != nil {
			return nil, err
		}
		if len(orgs) > 0 {
			return nil, apierrors.New(apierrors.CodeMemberExists)
		}
		if err := repo.AddMember(ctx, &OrgMember{
			OrganizationID: j.TargetID,
			UserID:         j.UserID,
			Role:           RoleMember,
			JoinedAt:       time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
	case JoinTargetProject:
		if err := repo.AddProjectMember(ctx, &ProjectMember{
			ProjectID: j.TargetID,
			UserID:    j.UserID,
			Role:      RoleMember,
			JoinedAt:  time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
	}
	if err := repo.SetJoinRequestStatus(ctx, j.ID, JoinStatusApproved, adminID, time.Now().Unix()); err != nil {
		return nil, err
	}
	j.Status = JoinStatusApproved
	j.DecidedBy = adminID
	j.DecidedAt = time.Now().Unix()
	return &tenancyv1.ApproveJoinRequestResponse{Response: okResponse(), Request: summarizeJoinRequest(j, s.userLabel(ctx, j.UserID))}, nil
}

// RejectJoinRequest rejects a pending request.
func (s *Service) RejectJoinRequest(ctx context.Context, req *tenancyv1.RejectJoinRequestRequest) (*tenancyv1.RejectJoinRequestResponse, error) {
	j, err := s.pendingJoinRequest(ctx, req.GetRequestId())
	if err != nil {
		return nil, err
	}
	adminID, err := s.sessionUserID(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	if err := repo.SetJoinRequestStatus(ctx, j.ID, JoinStatusRejected, adminID, time.Now().Unix()); err != nil {
		return nil, err
	}
	j.Status = JoinStatusRejected
	j.DecidedBy = adminID
	j.DecidedAt = time.Now().Unix()
	return &tenancyv1.RejectJoinRequestResponse{Response: okResponse(), Request: summarizeJoinRequest(j, s.userLabel(ctx, j.UserID))}, nil
}

// pendingJoinRequest loads a pending request and validates that the
// caller administers its target.
func (s *Service) pendingJoinRequest(ctx context.Context, requestID string) (*JoinRequest, error) {
	repo, err := s.membershipRepo()
	if err != nil {
		return nil, err
	}
	j, err := repo.FindJoinRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, apierrors.New(apierrors.CodeInvitationNotFound)
	}
	if j.Status != JoinStatusPending {
		return nil, apierrors.New(apierrors.CodeInvitationNotFound)
	}
	if err := s.requireTargetAdmin(ctx, j.TargetType, j.TargetID); err != nil {
		return nil, err
	}
	return j, nil
}

// requireTargetAdmin enforces org admin/owner for an organization target,
// or org admin/owner / project admin for a project target.
func (s *Service) requireTargetAdmin(ctx context.Context, targetType, targetID string) error {
	switch targetType {
	case JoinTargetOrganization:
		return s.requireAdminOrOwner(ctx, targetID)
	case JoinTargetProject:
		return s.requireProjectAdmin(ctx, targetID)
	default:
		return apierrors.New(apierrors.CodeTenancyInvalid)
	}
}

// requireOrgMembershipOfProject validates that the user belongs to the
// project's owning organization.
func (s *Service) requireOrgMembershipOfProject(ctx context.Context, projectID, userID string) error {
	repo, err := s.repository()
	if err != nil {
		return err
	}
	project, err := repo.FindProject(ctx, projectID)
	if isRecordNotFound(err) {
		return apierrors.New(apierrors.CodeProjectNotFound)
	}
	if err != nil {
		return err
	}
	mrepo, err := s.membershipRepo()
	if err != nil {
		return err
	}
	m, err := mrepo.FindMember(ctx, project.OrganizationID, userID)
	if err != nil {
		return err
	}
	if m == nil {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

// summarizeJoinRequest maps a join request row to the proto message.
// displayName is the requester's resolved label; empty falls back to the
// user id.
func summarizeJoinRequest(j *JoinRequest, displayName string) *tenancyv1.JoinRequest {
	return &tenancyv1.JoinRequest{
		RequestId:   j.ID,
		UserId:      j.UserID,
		DisplayName: displayName,
		TargetType:  j.TargetType,
		TargetId:    j.TargetID,
		Status:      j.Status,
		Message:     j.Message,
		DecidedBy:   j.DecidedBy,
		DecidedAt:   j.DecidedAt,
		CreatedAt:   j.CreatedAt.Unix(),
	}
}

// joinRequestLabels resolves the display labels of the given join request
// rows in one batch. A missing resolver or a resolution error yields an
// empty map so the roster falls back to user ids.
func (s *Service) joinRequestLabels(ctx context.Context, rows []*JoinRequest) map[string]string {
	if s.userLabelResolver == nil || len(rows) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	labels, err := s.userLabelResolver.ResolveUserLabels(ctx, ids)
	if err != nil {
		return nil
	}
	return labels
}
