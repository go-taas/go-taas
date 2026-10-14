package routing

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// GroupFilter narrows a routing-group list query.
type GroupFilter struct {
	Name     string
	Strategy string
	Enabled  string // "" | "true" | "false"
	Offset   int
	Limit    int
}

// Repository persists routing groups, their candidates and rules, and
// the group grants. All reads and writes join an open transaction via
// the context.
type Repository struct {
	db *database.Manager
}

// NewRepository constructs a Repository bound to a GORM database.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: database.NewManager(db)}
}

// Create inserts a routing group, generating the id when unset. A
// unique violation on name maps to 13102.
func (r *Repository) Create(ctx context.Context, g *Group) error {
	if g.ID == "" {
		g.ID = uuid.NewString()
	}
	if err := r.db.DB(ctx).Create(g).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeRoutingGroupNameConflict)
		}
		return err
	}
	return nil
}

// GetByID returns the group with the given id (13101 on a miss).
func (r *Repository) GetByID(ctx context.Context, id string) (*Group, error) {
	var row Group
	err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeRoutingGroupNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindByName returns the group with the given name, or nil when none
// exists (used for the uniqueness check and the resolver lookup).
func (r *Repository) FindByName(ctx context.Context, name string) (*Group, error) {
	var row Group
	err := r.db.DB(ctx).Where("name = ?", name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Update persists the mutable columns of a group row. A unique violation
// on name maps to 13102.
func (r *Repository) Update(ctx context.Context, g *Group) error {
	err := r.db.DB(ctx).Model(&Group{}).Where("id = ?", g.ID).
		Updates(map[string]any{
			"name":        g.Name,
			"description": g.Description,
			"strategy":    g.Strategy,
			"enabled":     g.Enabled,
			"updated_by":  g.UpdatedBy,
			"updated_at":  time.Now().UTC(),
		}).Error
	if err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeRoutingGroupNameConflict)
		}
		return err
	}
	return nil
}

// Delete removes the group and its candidates and rules in one
// transaction. Decision-log entries survive (they denormalize the
// names); grants must be gone (the service checks 13111 first).
func (r *Repository) Delete(ctx context.Context, id string) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		if err := r.db.DB(ctx).Where("group_id = ?", id).Delete(&Candidate{}).Error; err != nil {
			return err
		}
		if err := r.db.DB(ctx).Where("group_id = ?", id).Delete(&Rule{}).Error; err != nil {
			return err
		}
		return r.db.DB(ctx).Where("id = ?", id).Delete(&Group{}).Error
	})
}

// List returns one page of groups and the total count.
func (r *Repository) List(ctx context.Context, f GroupFilter) ([]*Group, int64, error) {
	q := r.db.DB(ctx).Model(&Group{})
	if s := strings.TrimSpace(f.Name); s != "" {
		q = q.Where("name LIKE ?", "%"+s+"%")
	}
	if s := strings.TrimSpace(f.Strategy); s != "" {
		q = q.Where("strategy = ?", s)
	}
	switch f.Enabled {
	case "true":
		q = q.Where("enabled = ?", true)
	case "false":
		q = q.Where("enabled = ?", false)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]*Group, 0)
	err := q.Order("created_at DESC, id DESC").Offset(f.Offset).Limit(f.Limit).Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListCandidates returns the group's candidates ordered by position.
func (r *Repository) ListCandidates(ctx context.Context, groupID string) ([]*Candidate, error) {
	rows := make([]*Candidate, 0)
	err := r.db.DB(ctx).Where("group_id = ?", groupID).Order("position ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ReplaceCandidates atomically replaces the group's candidate list
// (transactional delete + insert, 0-based positions).
func (r *Repository) ReplaceCandidates(ctx context.Context, groupID string, rows []*Candidate) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		if err := r.db.DB(ctx).Where("group_id = ?", groupID).Delete(&Candidate{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		now := time.Now().UTC()
		for pos, row := range rows {
			row.ID = uuid.NewString()
			row.GroupID = groupID
			row.Position = pos
			row.CreatedAt = now
			row.UpdatedAt = now
			if err := r.db.DB(ctx).Create(row).Error; err != nil {
				if isUniqueViolation(err) {
					return apierrors.New(apierrors.CodeRoutingGroupCandidateExists)
				}
				return err
			}
		}
		return nil
	})
}

// ListRules returns the group's rules ordered by position.
func (r *Repository) ListRules(ctx context.Context, groupID string) ([]*Rule, error) {
	rows := make([]*Rule, 0)
	err := r.db.DB(ctx).Where("group_id = ?", groupID).Order("position ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ReplaceRules atomically replaces the group's rule list (transactional
// delete + insert, 0-based positions).
func (r *Repository) ReplaceRules(ctx context.Context, groupID string, rows []*Rule) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		if err := r.db.DB(ctx).Where("group_id = ?", groupID).Delete(&Rule{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		now := time.Now().UTC()
		for pos, row := range rows {
			row.ID = uuid.NewString()
			row.GroupID = groupID
			row.Position = pos
			row.CreatedAt = now
			row.UpdatedAt = now
			if err := r.db.DB(ctx).Create(row).Error; err != nil {
				if isUniqueViolation(err) {
					return apierrors.New(apierrors.CodeRoutingGroupRuleExists)
				}
				return err
			}
		}
		return nil
	})
}

// GrantAccess adds an organization to the group's grant list (AD12),
// recording the granting user and the grant time. Idempotent: the
// composite unique key (group_id, organization_id) makes a repeated
// grant a no-op write.
func (r *Repository) GrantAccess(ctx context.Context, groupID, organizationID, grantedBy string) error {
	row := &GroupGrant{
		ID:             uuid.NewString(),
		GroupID:        groupID,
		OrganizationID: organizationID,
		GrantedBy:      grantedBy,
		CreatedAt:      time.Now().UTC(),
	}
	return r.db.DB(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "group_id"}, {Name: "organization_id"}},
			DoNothing: true,
		}).
		Create(row).Error
}

// RevokeAccess removes the organization from the group's grant list.
// Idempotent: revoking an organization that holds no grant deletes zero
// rows and reports success.
func (r *Repository) RevokeAccess(ctx context.Context, groupID, organizationID string) error {
	return r.db.DB(ctx).
		Where("group_id = ? AND organization_id = ?", groupID, organizationID).
		Delete(&GroupGrant{}).Error
}

// ListGrants returns one page of the group's grant rows, newest first,
// with the total count.
func (r *Repository) ListGrants(ctx context.Context, groupID string, offset, limit int) ([]*GroupGrant, int64, error) {
	var total int64
	q := r.db.DB(ctx).Model(&GroupGrant{}).Where("group_id = ?", groupID)
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]*GroupGrant, 0)
	err := q.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// CountGrants returns the number of grant rows of the group (the 13111
// delete guard).
func (r *Repository) CountGrants(ctx context.Context, groupID string) (int64, error) {
	var total int64
	err := r.db.DB(ctx).Model(&GroupGrant{}).Where("group_id = ?", groupID).Count(&total).Error
	return total, err
}

// HasGrant reports whether the organization holds a grant on the group,
// ignoring the default-allow rule.
func (r *Repository) HasGrant(ctx context.Context, groupID, organizationID string) (bool, error) {
	var total int64
	err := r.db.DB(ctx).Model(&GroupGrant{}).
		Where("group_id = ? AND organization_id = ?", groupID, organizationID).
		Count(&total).Error
	if err != nil {
		return false, err
	}
	return total > 0, nil
}

// IsGroupAuthorized implements the default-allow check mirrored from
// model_authorizations (AD12): a group with zero grants is authorized
// for every organization, a restricted group only for the granted set.
func (r *Repository) IsGroupAuthorized(ctx context.Context, groupID, organizationID string) (bool, error) {
	total, err := r.CountGrants(ctx, groupID)
	if err != nil {
		return false, err
	}
	if total == 0 {
		return true, nil
	}
	return r.HasGrant(ctx, groupID, organizationID)
}

// ListGrantedGroups returns the groups the organization may use,
// ordered by name: every group with zero grants, plus the restricted
// groups the organization is granted (the VirtualModelLister seam's
// query).
func (r *Repository) ListGrantedGroups(ctx context.Context, organizationID string) ([]*Group, error) {
	const cond = `NOT EXISTS (SELECT 1 FROM routing_group_grants g WHERE g.group_id = routing_groups.id)` +
		` OR EXISTS (SELECT 1 FROM routing_group_grants g WHERE g.group_id = routing_groups.id AND g.organization_id = ?)`
	rows := make([]*Group, 0)
	err := r.db.DB(ctx).Where(cond, organizationID).Order("name ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// isUniqueViolation reports whether err is a unique-constraint violation
// across the supported dialects (PostgreSQL 23505, SQLite generic message).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}
