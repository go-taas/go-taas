package notification

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Repository persists notifications, preferences and thresholds on top
// of the generic repository base (feature #26).
type Repository struct {
	*database.BaseRepository[Notification]
	db *database.Manager
}

// NewRepository constructs a Repository bound to a database Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[Notification](mgr),
		db:             mgr,
	}
}

// InsertNotification inserts a notification and returns the generated
// id (AC1).
func (r *Repository) InsertNotification(ctx context.Context, n *Notification) (string, error) {
	if n.NotificationID == "" {
		n.NotificationID = uuid.NewString()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	if err := r.DB(ctx).Create(n).Error; err != nil {
		return "", apierrors.Wrap(apierrors.CodeInternal, err, "notification: insert failed")
	}
	return n.NotificationID, nil
}

// FindNotificationByID returns the notification with the given id scoped
// to the org and user. A miss maps to 11001 (AC2).
func (r *Repository) FindNotificationByID(ctx context.Context, orgID, userID, notificationID string) (*Notification, error) {
	if _, err := uuid.Parse(notificationID); err != nil {
		return nil, apierrors.New(apierrors.CodeNotificationNotFound)
	}
	var row Notification
	err := r.DB(ctx).First(&row,
		"notification_id = ? AND organization_id = ? AND user_id = ?",
		notificationID, orgID, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeNotificationNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListNotifications returns one page of notifications for the org and
// user, newest first, with the read/event filters, and the total count
// (AC1).
func (r *Repository) ListNotifications(ctx context.Context, filter NotificationFilter) ([]*Notification, int64, error) {
	query := r.DB(ctx).Model(&Notification{}).
		Where("organization_id = ? AND user_id = ?", filter.OrganizationID, filter.UserID)
	if filter.ReadFilter {
		query = query.Where("read = ?", filter.Read)
	}
	if filter.EventType != "" {
		query = query.Where("event_type = ?", filter.EventType)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Notification
	if err := query.
		Order("created_at DESC").Order("notification_id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// MarkNotificationRead marks one notification read and returns it. A
// miss maps to 11001 (AC2).
func (r *Repository) MarkNotificationRead(ctx context.Context, orgID, userID, notificationID string) (*Notification, error) {
	n, err := r.FindNotificationByID(ctx, orgID, userID, notificationID)
	if err != nil {
		return nil, err
	}
	if err := r.DB(ctx).Model(&Notification{}).
		Where("notification_id = ? AND organization_id = ? AND user_id = ?", notificationID, orgID, userID).
		Update("read", true).Error; err != nil {
		return nil, err
	}
	n.Read = true
	return n, nil
}

// MarkAllNotificationsRead marks all of the caller's notifications read
// and returns the count marked (AC2).
func (r *Repository) MarkAllNotificationsRead(ctx context.Context, orgID, userID string) (int64, error) {
	res := r.DB(ctx).Model(&Notification{}).
		Where("organization_id = ? AND user_id = ? AND read = ?", orgID, userID, false).
		Update("read", true)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// DeleteNotification deletes one notification. A miss maps to 11001
// (AC3).
func (r *Repository) DeleteNotification(ctx context.Context, orgID, userID, notificationID string) error {
	if _, err := uuid.Parse(notificationID); err != nil {
		return apierrors.New(apierrors.CodeNotificationNotFound)
	}
	res := r.DB(ctx).
		Where("notification_id = ? AND organization_id = ? AND user_id = ?", notificationID, orgID, userID).
		Delete(&Notification{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apierrors.New(apierrors.CodeNotificationNotFound)
	}
	return nil
}

// CountUnread returns the caller's unread count (AC1).
func (r *Repository) CountUnread(ctx context.Context, orgID, userID string) (int64, error) {
	var count int64
	err := r.DB(ctx).Model(&Notification{}).
		Where("organization_id = ? AND user_id = ? AND read = ?", orgID, userID, false).
		Count(&count).Error
	return count, err
}

// CountUnreadForUser returns the caller's unread count across the
// organizations they belong to. It is the fallback for a caller with no
// active organization - a platform administrator in the admin console, or
// an account that has not joined an organization yet - so the badge still
// reports something instead of failing the request.
func (r *Repository) CountUnreadForUser(ctx context.Context, userID string) (int64, error) {
	var count int64
	err := r.DB(ctx).Model(&Notification{}).
		Where("user_id = ? AND read = ?", userID, false).
		Count(&count).Error
	return count, err
}

// GetPreferences returns the caller's preference row, or nil when absent
// (default all enabled, AC4).
func (r *Repository) GetPreferences(ctx context.Context, orgID, userID string) (*NotificationPreference, error) {
	var row NotificationPreference
	err := r.DB(ctx).First(&row, "user_id = ? AND organization_id = ?", userID, orgID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpsertPreferences upserts the caller's preference row (AC4).
func (r *Repository) UpsertPreferences(ctx context.Context, orgID, userID string, enabledEventTypes []string) error {
	raw, err := json.Marshal(enabledEventTypes)
	if err != nil {
		return apierrors.Wrap(apierrors.CodeInternal, err, "notification: marshal preferences failed")
	}
	now := time.Now().UTC()
	row := &NotificationPreference{
		UserID:            userID,
		OrganizationID:    orgID,
		Surface:           surfaceForEventTypes(enabledEventTypes),
		EnabledEventTypes: string(raw),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	return r.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"organization_id", "surface", "enabled_event_types", "updated_at"}),
	}).Create(row).Error
}

// InsertThreshold inserts a threshold and returns the generated id
// (AC5).
func (r *Repository) InsertThreshold(ctx context.Context, t *NotificationThreshold) (string, error) {
	if t.ThresholdID == "" {
		t.ThresholdID = uuid.NewString()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	t.UpdatedAt = t.CreatedAt
	if err := r.DB(ctx).Create(t).Error; err != nil {
		return "", apierrors.Wrap(apierrors.CodeInternal, err, "notification: threshold insert failed")
	}
	return t.ThresholdID, nil
}

// FindThresholdByID returns the threshold with the given id scoped to
// the org and user. A miss maps to 11003 (AC5).
func (r *Repository) FindThresholdByID(ctx context.Context, orgID, userID, thresholdID string) (*NotificationThreshold, error) {
	if _, err := uuid.Parse(thresholdID); err != nil {
		return nil, apierrors.New(apierrors.CodeNotificationThresholdNotFound)
	}
	var row NotificationThreshold
	err := r.DB(ctx).First(&row,
		"threshold_id = ? AND organization_id = ? AND user_id = ?",
		thresholdID, orgID, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeNotificationThresholdNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListThresholds returns one page of thresholds for the org and user,
// with the enabled filter, and the total count (AC5).
func (r *Repository) ListThresholds(ctx context.Context, filter ThresholdFilter) ([]*NotificationThreshold, int64, error) {
	query := r.DB(ctx).Model(&NotificationThreshold{}).
		Where("organization_id = ? AND user_id = ?", filter.OrganizationID, filter.UserID)
	if filter.EnabledFilter {
		query = query.Where("enabled = ?", filter.Enabled)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*NotificationThreshold
	if err := query.
		Order("created_at DESC").Order("threshold_id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// UpdateThreshold persists the mutable fields of a threshold (AC5).
func (r *Repository) UpdateThreshold(ctx context.Context, t *NotificationThreshold) error {
	t.UpdatedAt = time.Now().UTC()
	return r.DB(ctx).Model(&NotificationThreshold{}).
		Where("threshold_id = ? AND organization_id = ? AND user_id = ?", t.ThresholdID, t.OrganizationID, t.UserID).
		Updates(map[string]any{
			"name":       t.Name,
			"operator":   t.Operator,
			"value":      t.Value,
			"enabled":    t.Enabled,
			"updated_at": t.UpdatedAt,
		}).Error
}

// DeleteThreshold deletes a threshold. A miss maps to 11003 (AC5).
func (r *Repository) DeleteThreshold(ctx context.Context, orgID, userID, thresholdID string) error {
	if _, err := uuid.Parse(thresholdID); err != nil {
		return apierrors.New(apierrors.CodeNotificationThresholdNotFound)
	}
	res := r.DB(ctx).
		Where("threshold_id = ? AND organization_id = ? AND user_id = ?", thresholdID, orgID, userID).
		Delete(&NotificationThreshold{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apierrors.New(apierrors.CodeNotificationThresholdNotFound)
	}
	return nil
}

// FindEnabledUsersForEvent returns the users of an org on a surface
// whose preferences enable the event type (AC6). A user with no
// preference row defaults to all enabled (AD4).
func (r *Repository) FindEnabledUsersForEvent(ctx context.Context, orgID, surface, eventType string) ([]string, error) {
	var prefs []NotificationPreference
	if err := r.DB(ctx).
		Where("organization_id = ? AND surface = ?", orgID, surface).
		Find(&prefs).Error; err != nil {
		return nil, err
	}
	// Users with a preference row that disables the event type are
	// excluded; users with no row default to enabled. The notification
	// module only knows users who have a preference row or who have
	// received a notification; for the fan-out we consider users with a
	// preference row that enables the event type, plus users who have
	// received a notification for this org/surface but have no
	// preference row (default all enabled).
	enabled := make(map[string]struct{})
	disabled := make(map[string]struct{})
	for _, p := range prefs {
		types, err := p.EventTypes()
		if err != nil {
			continue
		}
		if containsString(types, eventType) {
			enabled[p.UserID] = struct{}{}
		} else {
			disabled[p.UserID] = struct{}{}
		}
	}
	// Users with a notification but no preference row default to enabled.
	var notified []string
	if err := r.DB(ctx).Model(&Notification{}).
		Distinct("user_id").
		Where("organization_id = ? AND surface = ?", orgID, surface).
		Pluck("user_id", &notified).Error; err != nil {
		return nil, err
	}
	for _, uid := range notified {
		if _, isDisabled := disabled[uid]; !isDisabled {
			enabled[uid] = struct{}{}
		}
	}
	out := make([]string, 0, len(enabled))
	for uid := range enabled {
		out = append(out, uid)
	}
	return out, nil
}

// FindEnabledThresholds returns the enabled thresholds of an org on a
// surface for threshold evaluation (AC7).
func (r *Repository) FindEnabledThresholds(ctx context.Context, orgID, surface string) ([]*NotificationThreshold, error) {
	var rows []*NotificationThreshold
	if err := r.DB(ctx).
		Where("organization_id = ? AND surface = ? AND enabled = ?", orgID, surface, true).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// DeleteNotificationsBefore deletes up to batch notifications older
// than cutoff and returns the number deleted (AC6, retention). The
// portable form (select ids then delete by ids) bounds each statement on
// both SQLite and Postgres.
func (r *Repository) DeleteNotificationsBefore(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = 1
	}
	var ids []string
	if err := r.DB(ctx).Model(&Notification{}).
		Where("created_at < ?", cutoff.UTC()).
		Limit(batch).
		Pluck("notification_id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.DB(ctx).Where("notification_id IN ?", ids).Delete(&Notification{})
	return result.RowsAffected, result.Error
}

// EventTypes decodes the JSON enabled_event_types column.
func (p *NotificationPreference) EventTypes() ([]string, error) {
	var types []string
	if err := json.Unmarshal([]byte(p.EnabledEventTypes), &types); err != nil {
		return nil, err
	}
	return types, nil
}

// surfaceForEventTypes derives the surface from the enabled event types
// (used at upsert time; the surface is a property of the binding).
func surfaceForEventTypes(types []string) string {
	for _, t := range types {
		if containsString(AdminEventTypes, t) {
			return SurfaceAdmin
		}
	}
	return SurfaceUser
}
