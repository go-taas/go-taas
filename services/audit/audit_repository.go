package audit

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Repository persists audit events on top of the generic repository
// base. All reads and writes join an open transaction via the context.
type Repository struct {
	*database.BaseRepository[AuditEvent]
	db *database.Manager
}

// NewRepository constructs a Repository bound to a database Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[AuditEvent](mgr),
		db:             mgr,
	}
}

// InsertAuditEvent inserts an audit event and returns the generated id
// (AC1). Infrastructure failures map to 10600 (internal).
func (r *Repository) InsertAuditEvent(ctx context.Context, ev *AuditEvent) (string, error) {
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	if err := r.DB(ctx).Create(ev).Error; err != nil {
		return "", apierrors.Wrap(apierrors.CodeInternal, err, "audit: event write failed")
	}
	return ev.ID, nil
}

// FindAuditEventByID returns the audit event with the given id. A miss
// maps to 10601. Malformed (non-UUID) ids also map to 10601: they can
// never match a stored event (AC5).
func (r *Repository) FindAuditEventByID(ctx context.Context, id string) (*AuditEvent, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, apierrors.New(apierrors.CodeAuditEventNotFound)
	}
	var row AuditEvent
	err := r.DB(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeAuditEventNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListAuditEvents returns one page of audit events, newest first, and
// the total count. Empty filter values match everything (FR3.1).
func (r *Repository) ListAuditEvents(ctx context.Context, filter AuditEventFilter) ([]*AuditEvent, int64, error) {
	query := r.applyFilters(r.DB(ctx).Model(&AuditEvent{}), filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*AuditEvent
	if err := query.
		Order("created_at DESC").Order("id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListMyActivity returns one page of the caller's own audit events,
// newest first, and the total count. It is hard-scoped to the caller
// (actor_user_id = the caller) and never sees other users' events
// (FR4.1, AC7).
func (r *Repository) ListMyActivity(ctx context.Context, actorUserID string, filter AuditEventFilter) ([]*AuditEvent, int64, error) {
	query := r.applyFilters(r.DB(ctx).Model(&AuditEvent{}), filter).
		Where("actor_user_id = ?", actorUserID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*AuditEvent
	if err := query.
		Order("created_at DESC").Order("id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// applyFilters adds the org/actor/action/resource/result/range
// conditions shared by count and page queries.
func (r *Repository) applyFilters(query *gorm.DB, filter AuditEventFilter) *gorm.DB {
	if filter.OrganizationID != "" {
		query = query.Where("organization_id = ?", filter.OrganizationID)
	}
	if filter.ActorUserID != "" {
		query = query.Where("actor_user_id = ?", filter.ActorUserID)
	}
	if filter.Action != "" {
		query = query.Where("action = ?", filter.Action)
	}
	if filter.ResourceType != "" {
		query = query.Where("resource_type = ?", filter.ResourceType)
	}
	if filter.Result != "" {
		query = query.Where("result = ?", filter.Result)
	}
	if filter.Since > 0 {
		query = query.Where("created_at >= ?", time.Unix(filter.Since, 0).UTC())
	}
	if filter.Until > 0 {
		query = query.Where("created_at < ?", time.Unix(filter.Until, 0).UTC())
	}
	return query
}

// DeleteAuditEventsBefore deletes up to batch audit events older than
// the cutoff and returns the number deleted (AC9). It is the only
// deleter of audit events (AD6).
func (r *Repository) DeleteAuditEventsBefore(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = 1
	}
	var ids []string
	if err := r.DB(ctx).Model(&AuditEvent{}).
		Where("created_at < ?", cutoff.UTC()).
		Limit(batch).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.DB(ctx).Where("id IN ?", ids).Delete(&AuditEvent{})
	return result.RowsAffected, result.Error
}
