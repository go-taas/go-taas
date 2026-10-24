package tenancy

import "time"

// Roles are the fixed, closed set of organization roles (feature #10,
// AD1). Roles are a closed enum, not free text.
const (
	// RoleOwner is the unique, protected top role. Exactly one owner
	// per org; cannot be removed or demoted (AD3).
	RoleOwner = "owner"
	// RoleAdmin manages members and invitations.
	RoleAdmin = "admin"
	// RoleMember uses org resources.
	RoleMember = "member"
	// RoleViewer is read-only.
	RoleViewer = "viewer"
	// RolePlatformAdmin marks a platform administrator. It is carried on
	// a dedicated platform organization membership so the session
	// carries the role; it is not assignable through member APIs.
	RolePlatformAdmin = "platform-admin"
)

// InvitationStatus values (feature #10, AD4/AD5).
const (
	// InvitationPending is an open, not-yet-consumed invitation.
	InvitationPending = "pending"
	// InvitationAccepted means the invitee joined the org.
	InvitationAccepted = "accepted"
	// InvitationRejected means the invitee declined.
	InvitationRejected = "rejected"
	// InvitationRevoked means an admin cancelled it.
	InvitationRevoked = "revoked"
	// InvitationExpired means the window closed without accept.
	InvitationExpired = "expired"
)

// OrgMember is one row of an organization's roster: the authoritative
// source of who belongs to an org and with what role (feature #10,
// AD2). The composite (organization_id, user_id) is the natural key.
type OrgMember struct {
	// OrganizationID is the owning organization.
	OrganizationID string `gorm:"size:64;not null;uniqueIndex:idx_org_members_org_user,priority:1"`
	// UserID is the member platform user.
	UserID string `gorm:"type:uuid;not null;uniqueIndex:idx_org_members_org_user,priority:2;index:idx_org_members_user"`
	// Role is owner / admin / member / viewer (AD1).
	Role string `gorm:"size:16;not null"`
	// JoinedAt is when the membership was created (add or accept).
	JoinedAt  time.Time `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName returns the org_members table name.
func (OrgMember) TableName() string { return "org_members" }

// ProjectMember is one row of a project's roster (feature #10 project
// refinement). Project roles are admin / member / viewer; there is no
// owner at the project level — the owning org's owner governs the
// project. The composite (project_id, user_id) is the natural key.
type ProjectMember struct {
	// ProjectID is the owning project.
	ProjectID string `gorm:"size:64;not null;uniqueIndex:idx_project_members_project_user,priority:1"`
	// UserID is the member platform user.
	UserID string `gorm:"type:uuid;not null;uniqueIndex:idx_project_members_project_user,priority:2;index:idx_project_members_user"`
	// Role is admin / member / viewer.
	Role string `gorm:"size:16;not null"`
	// JoinedAt is when the membership was created.
	JoinedAt  time.Time `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName returns the project_members table name.
func (ProjectMember) TableName() string { return "project_members" }

// Invitation is one row of the org's invitation pipeline (feature #10,
// AD4/AD5). The token is stored hashed; the raw token is returned once
// at creation/resend and never stored.
type Invitation struct {
	// ID is the generated invitation id.
	ID string `gorm:"primaryKey;type:uuid"`
	// OrganizationID is the inviting organization.
	OrganizationID string `gorm:"size:64;not null;index:idx_invitations_org_status,priority:1"`
	// Email is the invitee's email.
	Email string `gorm:"size:256;not null"`
	// Role is admin / member / viewer (owner cannot be invited).
	Role string `gorm:"size:16;not null"`
	// TokenHash is the salted hash of the raw token (AD4).
	TokenHash string `gorm:"size:256;not null;uniqueIndex"`
	// Status is pending / accepted / rejected / revoked / expired.
	Status string `gorm:"size:16;not null;default:'pending';index:idx_invitations_org_status,priority:2"`
	// ExpiresAt is the accept deadline, unix seconds (AD5).
	ExpiresAt int64 `gorm:"not null"`
	// CreatedBy is the inviting user id.
	CreatedBy string `gorm:"type:uuid;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName returns the invitations table name.
func (Invitation) TableName() string { return "invitations" }

// JoinRequest is a user's request to join an organization or a project
// (self-service membership). A user belongs to exactly one organization
// but may belong to many projects; the approval flow enforces both.
type JoinRequest struct {
	// ID is the generated request id.
	ID string `gorm:"primaryKey;type:uuid"`
	// UserID is the requesting platform user.
	UserID string `gorm:"type:uuid;not null;index:idx_join_requests_user,priority:1"`
	// TargetType is "organization" or "project".
	TargetType string `gorm:"size:16;not null;index:idx_join_requests_target,priority:1"`
	// TargetID is the organization or project id.
	TargetID string `gorm:"size:64;not null;index:idx_join_requests_target,priority:2"`
	// Status is pending / approved / rejected.
	Status string `gorm:"size:16;not null;default:'pending';index:idx_join_requests_target,priority:3"`
	// Message is the applicant's optional note.
	Message string `gorm:"size:512;not null;default:''"`
	// DecidedBy is the admin user who approved/rejected the request.
	DecidedBy string `gorm:"size:64;not null;default:''"`
	// DecidedAt is the decision time (unix seconds); 0 when pending.
	DecidedAt int64 `gorm:"not null;default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName returns the join_requests table name.
func (JoinRequest) TableName() string { return "join_requests" }

// Join request target types.
const (
	// JoinTargetOrganization is an organization join request.
	JoinTargetOrganization = "organization"
	// JoinTargetProject is a project join request.
	JoinTargetProject = "project"
)

// Join request statuses.
const (
	// JoinStatusPending is an open request.
	JoinStatusPending = "pending"
	// JoinStatusApproved means the user was added as a member.
	JoinStatusApproved = "approved"
	// JoinStatusRejected means the admin declined the request.
	JoinStatusRejected = "rejected"
)
