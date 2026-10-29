package tenancy

import (
	"context"
	"strings"
	"time"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// projectMemberRepo lazily wires and returns the membership repository.
func (s *Service) projectMemberRepo() (*MembershipRepository, error) {
	return s.membershipRepo()
}

// requireProjectAdmin gates a project member or quota operation on the
// caller being a project admin, or an admin/owner of the owning
// organization. The project must exist (else CodeProjectNotFound).
func (s *Service) requireProjectAdmin(ctx context.Context, projectID string) error {
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
	userID, err := s.sessionUserID(ctx)
	if err != nil {
		return err
	}
	if s.roleGuard == nil {
		return nil
	}
	// An org admin/owner may always administer the org's projects. Project
	// management answers to the organization, never to the platform
	// administrator role, so the membership row alone decides.
	if err := s.roleGuard.RequireMemberRole(ctx, project.OrganizationID, userID, RoleAdmin); err == nil {
		return nil
	}
	// Otherwise the caller must be a project admin.
	mrepo, err := s.membershipRepo()
	if err != nil {
		return err
	}
	m, err := mrepo.FindProjectMember(ctx, projectID, userID)
	if err != nil {
		return err
	}
	if m != nil && m.Role == RoleAdmin {
		return nil
	}
	return apierrors.New(apierrors.CodeForbidden)
}

// ListProjectMembers returns the project's roster.
func (s *Service) ListProjectMembers(ctx context.Context, req *tenancyv1.ListProjectMembersRequest) (*tenancyv1.ListProjectMembersResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdmin(ctx, projectID); err != nil {
		return nil, err
	}
	repo, err := s.projectMemberRepo()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListProjectMembers(ctx, projectID, offset, limit)
	if err != nil {
		return nil, err
	}
	labels := s.projectMemberLabels(ctx, rows)
	members := make([]*tenancyv1.ProjectMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, summarizeProjectMember(row, labels[row.UserID]))
	}
	return &tenancyv1.ListProjectMembersResponse{
		Response: okResponse(),
		Members:  members,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// AddProjectMember adds a member with a role.
func (s *Service) AddProjectMember(ctx context.Context, req *tenancyv1.AddProjectMemberRequest) (*tenancyv1.AddProjectMemberResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdmin(ctx, projectID); err != nil {
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
	// Resolve a username or id to the canonical user id so a username
	// never reaches the DB as an invalid UUID.
	userID, err := s.resolveUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	repo, err := s.projectMemberRepo()
	if err != nil {
		return nil, err
	}
	// Symmetric with the organization roster: an account belongs to at most
	// one project, so joining one moves it rather than adding a second row.
	if err := repo.MoveUserToProject(ctx, projectID, userID); err != nil {
		return nil, err
	}
	m := &ProjectMember{
		ProjectID: projectID,
		UserID:    userID,
		Role:      role,
		JoinedAt:  time.Now().UTC(),
	}
	if err := repo.AddProjectMember(ctx, m); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  userID,
		ActorType:    "user",
		Action:       "project_member.add",
		ResourceType: "project_member",
		ResourceID:   userID,
		Result:       "success",
	})
	return &tenancyv1.AddProjectMemberResponse{
		Response: okResponse(),
		Member:   summarizeProjectMember(m, s.userLabel(ctx, m.UserID)),
	}, nil
}

// SetProjectMemberRole changes a project member's role.
func (s *Service) SetProjectMemberRole(ctx context.Context, req *tenancyv1.SetProjectMemberRoleRequest) (*tenancyv1.SetProjectMemberRoleResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdmin(ctx, projectID); err != nil {
		return nil, err
	}
	userID := req.GetUserId()
	role := strings.TrimSpace(req.GetRole())
	if !validAssignableRole(role) {
		return nil, apierrors.New(apierrors.CodeRoleInvalid)
	}
	repo, err := s.projectMemberRepo()
	if err != nil {
		return nil, err
	}
	m, err := repo.FindProjectMember(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, apierrors.New(apierrors.CodeMemberNotFound)
	}
	if err := repo.SetProjectMemberRole(ctx, projectID, userID, role); err != nil {
		return nil, err
	}
	m.Role = role
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  userID,
		ActorType:    "user",
		Action:       "project_member.role_change",
		ResourceType: "project_member",
		ResourceID:   userID,
		Result:       "success",
	})
	return &tenancyv1.SetProjectMemberRoleResponse{
		Response: okResponse(),
		Member:   summarizeProjectMember(m, s.userLabel(ctx, m.UserID)),
	}, nil
}

// RemoveProjectMember removes a project member.
func (s *Service) RemoveProjectMember(ctx context.Context, req *tenancyv1.RemoveProjectMemberRequest) (*tenancyv1.RemoveProjectMemberResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdmin(ctx, projectID); err != nil {
		return nil, err
	}
	userID := req.GetUserId()
	repo, err := s.projectMemberRepo()
	if err != nil {
		return nil, err
	}
	m, err := repo.FindProjectMember(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, apierrors.New(apierrors.CodeMemberNotFound)
	}
	if err := repo.RemoveProjectMember(ctx, projectID, userID); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  userID,
		ActorType:    "user",
		Action:       "project_member.remove",
		ResourceType: "project_member",
		ResourceID:   userID,
		Result:       "success",
	})
	return &tenancyv1.RemoveProjectMemberResponse{Response: okResponse()}, nil
}

// summarizeProjectMember maps a project member row to the proto message.
// displayName is the resolved user label; empty falls back to the user id.
func summarizeProjectMember(m *ProjectMember, displayName string) *tenancyv1.ProjectMember {
	if displayName == "" {
		displayName = m.UserID
	}
	return &tenancyv1.ProjectMember{
		UserId:      m.UserID,
		DisplayName: displayName,
		Role:        m.Role,
		JoinedAt:    m.JoinedAt.Unix(),
	}
}

// projectMemberLabels resolves the display labels of the given project
// member rows in one batch. A missing resolver or a resolution error
// yields an empty map so the roster falls back to user ids.
func (s *Service) projectMemberLabels(ctx context.Context, rows []*ProjectMember) map[string]string {
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
