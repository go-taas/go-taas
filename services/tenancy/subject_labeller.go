package tenancy

import (
	"context"

	"gorm.io/gorm"
)

// SubjectLabeller resolves the ids an access policy names to display
// labels, so a service's policy reads as names rather than UUIDs. It is
// the tenancy half of the infer module's SubjectLabeller seam: users are
// resolved by auth, organizations and projects by this type. The wiring
// passes a composite that dispatches per subject type.
type SubjectLabeller struct {
	db *gorm.DB
}

// NewSubjectLabeller builds the resolver over the shared database.
func NewSubjectLabeller(db *gorm.DB) *SubjectLabeller { return &SubjectLabeller{db: db} }

// LabelOrganizations returns a display name per organization id.
func (l *SubjectLabeller) LabelOrganizations(ctx context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []Organization
	if err := l.db.WithContext(ctx).
		Select("id", "display_name").
		Where("id IN ?", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		label := row.DisplayName
		if label == "" {
			label = row.ID
		}
		out[row.ID] = label
	}
	return out, nil
}

// LabelProjects returns a name per project id.
func (l *SubjectLabeller) LabelProjects(ctx context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []Project
	if err := l.db.WithContext(ctx).
		Select("id", "display_name").
		Where("id IN ?", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		label := row.DisplayName
		if label == "" {
			label = row.ID
		}
		out[row.ID] = label
	}
	return out, nil
}

// ProjectsForUser reports the ids of the projects a user belongs to. It is
// the tenancy half of the infer module's access-policy enforcement: a
// project grant admits every member of that project.
func (l *SubjectLabeller) ProjectsForUser(ctx context.Context, userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	var ids []string
	if err := l.db.WithContext(ctx).Model(&ProjectMember{}).
		Where("user_id = ?", userID).
		Distinct("project_id").
		Pluck("project_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}
