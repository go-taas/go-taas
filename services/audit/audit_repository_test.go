package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AuditEvent{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func newAuditTestRepo(t *testing.T) *Repository {
	t.Helper()
	return NewRepository(newAuditTestDB(t))
}

// mustInsert inserts an audit event directly and fails the test on error.
func mustInsert(ctx context.Context, t *testing.T, repo *Repository, ev *AuditEvent) string {
	t.Helper()
	id, err := repo.InsertAuditEvent(ctx, ev)
	require.NoError(t, err)
	return id
}

// AC1: InsertAuditEvent stores a row with actor, action, resource,
// result, IP and timestamp.
func TestRepositoryInsertAuditEvent(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	id := mustInsert(ctx, t, repo, &AuditEvent{
		OrganizationID: "org-1",
		ActorUserID:    "u-1",
		ActorType:      "user",
		Action:         "api_key.revoke",
		ResourceType:   "api_key",
		ResourceID:     "key-1",
		Result:         "success",
		IPAddress:      "10.0.0.1",
		UserAgent:      "test-agent",
		Metadata:       `{"old":true}`,
	})
	assert.NotEmpty(t, id)

	var row AuditEvent
	require.NoError(t, repo.DB(ctx).First(&row, "id = ?", id).Error)
	assert.Equal(t, "org-1", row.OrganizationID)
	assert.Equal(t, "u-1", row.ActorUserID)
	assert.Equal(t, "api_key.revoke", row.Action)
	assert.Equal(t, "success", row.Result)
	assert.Equal(t, "10.0.0.1", row.IPAddress)
	assert.False(t, row.CreatedAt.IsZero(), "created_at is set")
}

// AC5: FindAuditEventByID returns the row; unknown id -> 10601.
func TestRepositoryFindAuditEventByID(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	id := mustInsert(ctx, t, repo, &AuditEvent{
		OrganizationID: "org-1",
		ActorUserID:    "u-1",
		ActorType:      "user",
		Action:         "auth.login",
		ResourceType:   "session",
		ResourceID:     "sess-1",
		Result:         "success",
	})
	row, err := repo.FindAuditEventByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "auth.login", row.Action)

	// Unknown id.
	_, err = repo.FindAuditEventByID(ctx, "00000000-0000-0000-0000-000000000000")
	assertCode(t, err, apierrors.CodeAuditEventNotFound)

	// Malformed id.
	_, err = repo.FindAuditEventByID(ctx, "not-a-uuid")
	assertCode(t, err, apierrors.CodeAuditEventNotFound)
}

// AC4: ListAuditEvents filters by org/actor/action/resource/result/
// range, newest first, paginated.
func TestRepositoryListAuditEvents(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	for i, action := range []string{"auth.login", "api_key.revoke", "member.add"} {
		mustInsert(ctx, t, repo, &AuditEvent{
			OrganizationID: "org-1",
			ActorUserID:    "u-1",
			ActorType:      "user",
			Action:         action,
			ResourceType:   "api_key",
			ResourceID:     fmt.Sprintf("key-%d", i),
			Result:         "success",
			CreatedAt:      now.Add(time.Duration(i) * time.Minute),
		})
	}
	// A second org's event.
	mustInsert(ctx, t, repo, &AuditEvent{
		OrganizationID: "org-2",
		ActorUserID:    "u-2",
		ActorType:      "user",
		Action:         "auth.login",
		ResourceType:   "session",
		ResourceID:     "sess-2",
		Result:         "failure",
		CreatedAt:      now,
	})

	// Filter by org.
	rows, total, err := repo.ListAuditEvents(ctx, AuditEventFilter{
		OrganizationID: "org-1", Offset: 0, Limit: 20,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, rows, 3)
	// Newest first.
	assert.Equal(t, "member.add", rows[0].Action)

	// Filter by action.
	rows, total, err = repo.ListAuditEvents(ctx, AuditEventFilter{
		OrganizationID: "org-1", Action: "auth.login", Offset: 0, Limit: 20,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, "auth.login", rows[0].Action)

	// Filter by result.
	rows, total, err = repo.ListAuditEvents(ctx, AuditEventFilter{
		Result: "failure", Offset: 0, Limit: 20,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, "org-2", rows[0].OrganizationID)

	// Filter by range.
	rows, total, err = repo.ListAuditEvents(ctx, AuditEventFilter{
		OrganizationID: "org-1",
		Since:          now.Add(-time.Minute).Unix(),
		Until:          now.Add(2 * time.Minute).Unix(),
		Offset:         0, Limit: 20,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)

	// Pagination.
	rows, total, err = repo.ListAuditEvents(ctx, AuditEventFilter{
		OrganizationID: "org-1", Offset: 0, Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, rows, 2)
	assert.Equal(t, "org-1", rows[0].OrganizationID)
}

// AC7: ListMyActivity is hard-scoped to the caller.
func TestRepositoryListMyActivity(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	mustInsert(ctx, t, repo, &AuditEvent{
		OrganizationID: "org-1", ActorUserID: "u-1", ActorType: "user",
		Action: "auth.login", ResourceType: "session", ResourceID: "s1",
		Result: "success", CreatedAt: now,
	})
	mustInsert(ctx, t, repo, &AuditEvent{
		OrganizationID: "org-1", ActorUserID: "u-1", ActorType: "user",
		Action: "api_key.revoke", ResourceType: "api_key", ResourceID: "k1",
		Result: "success", CreatedAt: now.Add(time.Minute),
	})
	// Another user's event.
	mustInsert(ctx, t, repo, &AuditEvent{
		OrganizationID: "org-1", ActorUserID: "u-2", ActorType: "user",
		Action: "auth.login", ResourceType: "session", ResourceID: "s2",
		Result: "success", CreatedAt: now,
	})

	rows, total, err := repo.ListMyActivity(ctx, "u-1", AuditEventFilter{Offset: 0, Limit: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)
	for _, r := range rows {
		assert.Equal(t, "u-1", r.ActorUserID, "only the caller's events")
	}
}

// AC9: DeleteAuditEventsBefore deletes old events in batches and leaves
// newer events untouched.
func TestRepositoryDeleteAuditEventsBefore(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.Add(-400 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		mustInsert(ctx, t, repo, &AuditEvent{
			OrganizationID: "org-1", ActorUserID: "u-1", ActorType: "user",
			Action: "auth.login", ResourceType: "session", ResourceID: fmt.Sprintf("s%d", i),
			Result: "success", CreatedAt: old.Add(time.Duration(i) * time.Minute),
		})
	}
	// A recent event must survive.
	mustInsert(ctx, t, repo, &AuditEvent{
		OrganizationID: "org-1", ActorUserID: "u-1", ActorType: "user",
		Action: "auth.login", ResourceType: "session", ResourceID: "recent",
		Result: "success", CreatedAt: now,
	})

	// Batch of 2: first pass deletes 2.
	deleted, err := repo.DeleteAuditEventsBefore(ctx, now.Add(-365*24*time.Hour), 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	// Drain the rest.
	deleted, err = repo.DeleteAuditEventsBefore(ctx, now.Add(-365*24*time.Hour), 10)
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)

	var count int64
	require.NoError(t, repo.DB(ctx).Model(&AuditEvent{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "only the recent event survives")
}
