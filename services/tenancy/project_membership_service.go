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

// requireProjectAdminOrOwner gates a project member operation on the
// caller being an admin or owner in the project's owning organization.
// The project must exist (else CodeProjectNotFound).
func (s *Service) requireProjectAdminOrOwner(ctx context.Context, projectID string) error {
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
	return s.requireAdminOrOwner(ctx, project.OrganizationID)
}

// ListProjectMembers returns the project's roster.
func (s *Service) ListProjectMembers(ctx context.Context, req *tenancyv1.ListProjectMembersRequest) (*tenancyv1.ListProjectMembersResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdminOrOwner(ctx, projectID); err != nil {
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
	members := make([]*tenancyv1.ProjectMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, summarizeProjectMember(row))
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
	if err := s.requireProjectAdminOrOwner(ctx, projectID); err != nil {
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
	repo, err := s.projectMemberRepo()
	if err != nil {
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
		Member:   summarizeProjectMember(m),
	}, nil
}

// SetProjectMemberRole changes a project member's role.
func (s *Service) SetProjectMemberRole(ctx context.Context, req *tenancyv1.SetProjectMemberRoleRequest) (*tenancyv1.SetProjectMemberRoleResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdminOrOwner(ctx, projectID); err != nil {
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
		Member:   summarizeProjectMember(m),
	}, nil
}

// RemoveProjectMember removes a project member.
func (s *Service) RemoveProjectMember(ctx context.Context, req *tenancyv1.RemoveProjectMemberRequest) (*tenancyv1.RemoveProjectMemberResponse, error) {
	projectID := req.GetProjectId()
	if err := s.requireProjectAdminOrOwner(ctx, projectID); err != nil {
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
func summarizeProjectMember(m *ProjectMember) *tenancyv1.ProjectMember {
	return &tenancyv1.ProjectMember{
		UserId:      m.UserID,
		DisplayName: m.UserID,
		Role:        m.Role,
		JoinedAt:    m.JoinedAt.Unix(),
	}
}