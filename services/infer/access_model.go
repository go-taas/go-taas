package infer

import (
	"time"
)

// Inference service access-policy subject types. A service carries no
// organization: it is a platform asset, like an image. Who may use it is a
// separate question, answered by this policy.
const (
	// AccessSubjectOrg grants every member of an organization.
	AccessSubjectOrg = "org"
	// AccessSubjectProject grants every member of a project.
	AccessSubjectProject = "project"
	// AccessSubjectUser grants one user.
	AccessSubjectUser = "user"
)

// validAccessSubject reports whether a subject type is one we know.
func validAccessSubject(subject string) bool {
	switch subject {
	case AccessSubjectOrg, AccessSubjectProject, AccessSubjectUser:
		return true
	default:
		return false
	}
}

// InferenceServiceAccess is one row of a service's access policy.
//
// No rows means "every user on the platform may use it", which is the
// default and the reason an empty table is a valid configuration rather
// than an oversight: a brand-new service must be usable without anyone
// remembering to grant it.
type InferenceServiceAccess struct {
	// ID is the server-generated UUID v4.
	ID string `gorm:"primaryKey;type:uuid"`
	// ServiceID is the service the grant belongs to.
	ServiceID string `gorm:"type:uuid;not null;uniqueIndex:idx_infer_service_access,priority:1"`
	// SubjectType is org, project or user.
	SubjectType string `gorm:"size:16;not null;uniqueIndex:idx_infer_service_access,priority:2"`
	// SubjectID is the organization id, project id or user id.
	SubjectID string `gorm:"size:64;not null;uniqueIndex:idx_infer_service_access,priority:3"`
	// CreatedBy is the acting admin's user id (audit trail).
	CreatedBy string `gorm:"size:64;not null;default:''"`
	CreatedAt time.Time
}

// TableName overrides the default GORM table name.
func (InferenceServiceAccess) TableName() string { return "inference_service_access" }
