package audit

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	auditv1 "github.com/go-taas/go-taas/proto/taas/audit/v1"

	"github.com/go-taas/go-taas/pkg/server"
)

// displayTestDB is an in-memory database carrying the audit table plus the
// label-source tables the resolver reads.
func displayTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AuditEvent{}))
	for _, stmt := range []string{
		`CREATE TABLE organizations (id TEXT PRIMARY KEY, display_name TEXT)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT, display_name TEXT)`,
		`CREATE TABLE models (id TEXT PRIMARY KEY, name TEXT)`,
		`CREATE TABLE api_keys (id TEXT PRIMARY KEY, name TEXT)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

// Org and resource names are resolved so the console never shows raw ids.
func TestDecorateEventsResolvesNames(t *testing.T) {
	db := displayTestDB(t)
	require.NoError(t, db.Exec(
		`INSERT INTO organizations (id, display_name) VALUES ('org-1', 'Acme Labs')`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO models (id, name) VALUES ('model-1', 'llama-3-8b')`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO api_keys (id, name) VALUES ('key-1', 'CI pipeline')`).Error)

	svc := NewForFVT(db)
	repo, err := svc.repository()
	require.NoError(t, err)

	ctx := context.Background()
	rows := []*AuditEvent{
		{ID: "e1", OrganizationID: "org-1", ActorUserID: "u1", ActorType: "user",
			Action: "model.create", ResourceType: "model", ResourceID: "model-1", Result: "success"},
		{ID: "e2", OrganizationID: "org-1", ActorUserID: "u1", ActorType: "user",
			Action: "api_key.create", ResourceType: "api_key", ResourceID: "key-1", Result: "success"},
		// Unknown type and id keep the id as the label source (empty name).
		{ID: "e3", OrganizationID: "org-1", ResourceType: "session", ResourceID: "sess-1", Result: "success"},
	}

	events := svc.decorateEvents(ctx, repo, rows)
	require.Len(t, events, 3)
	assert.Equal(t, "Acme Labs", events[0].GetOrganizationDisplayName())
	assert.Equal(t, "llama-3-8b", events[0].GetResourceDisplayName())
	assert.Equal(t, "CI pipeline", events[1].GetResourceDisplayName())
	assert.Empty(t, events[2].GetResourceDisplayName())
	assert.Equal(t, "Acme Labs", events[2].GetOrganizationDisplayName())
}

// The recorder stamps the caller IP and user agent from the gateway
// metadata, which is what filled an empty ip_address column.
func TestRecorderStampsClientIPFromMetadata(t *testing.T) {
	db := displayTestDB(t)
	repo := NewRepository(db)
	rec := NewRecorder(repo)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		clientIPMetadataKey, "203.0.113.9",
		clientUserAgentMetadataKey, "Mozilla/5.0 test",
	))
	rec.Record(ctx, &AuditEvent{
		OrganizationID: "org-1", ActorUserID: "u1", ActorType: "user",
		Action: "model.create", ResourceType: "model", ResourceID: "model-1", Result: "success",
	})

	var row AuditEvent
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "203.0.113.9", row.IPAddress)
	assert.Equal(t, "Mozilla/5.0 test", row.UserAgent)
}

// An explicit IP on the event is never overwritten.
func TestRecorderKeepsExplicitIP(t *testing.T) {
	db := displayTestDB(t)
	rec := NewRecorder(NewRepository(db))
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(clientIPMetadataKey, "203.0.113.9"))

	rec.Record(ctx, &AuditEvent{
		ActorUserID: "u1", ActorType: "user", Action: "model.create",
		ResourceType: "model", ResourceID: "model-1", Result: "success",
		IPAddress: "10.0.0.7",
	})

	var row AuditEvent
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "10.0.0.7", row.IPAddress)
}

// The gateway annotator prefers X-Forwarded-For, then X-Real-IP, then the
// transport peer, and splits the port off.
func TestClientMetadataAnnotator(t *testing.T) {
	tests := []struct {
		name       string
		forwarded  string
		realIP     string
		remoteAddr string
		userAgent  string
		wantIP     string
	}{
		{name: "forwarded beats real-ip", forwarded: "198.51.100.4, 10.0.0.1",
			realIP: "198.51.100.5", remoteAddr: "10.0.0.9:1234", wantIP: "198.51.100.4"},
		{name: "real-ip when no forwarded", realIP: "198.51.100.6",
			remoteAddr: "10.0.0.9:1234", wantIP: "198.51.100.6"},
		{name: "peer when no headers", remoteAddr: "10.0.0.9:1234", wantIP: "10.0.0.9"},
		{name: "ipv6 peer", remoteAddr: "[2001:db8::1]:443", wantIP: "2001:db8::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tt.forwarded)
			}
			if tt.realIP != "" {
				r.Header.Set("X-Real-IP", tt.realIP)
			}
			if tt.userAgent != "" {
				r.Header.Set("User-Agent", tt.userAgent)
			}
			assert.Equal(t, tt.wantIP, server.ClientIPFromRequest(r))
		})
	}
}

var _ = auditv1.AuditEvent{}

// A membership event stores "project/user"; the user name is resolved and
// an account is labelled by its organization.
func TestResourceLabelsCompositeAndLinked(t *testing.T) {
	db := displayTestDB(t)
	require.NoError(t, db.Exec(
		`INSERT INTO organizations (id, display_name) VALUES ('org-1', 'Acme Labs')`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO users (id, username, display_name) VALUES ('u-9', 'alice', 'Alice Chen')`).Error)
	require.NoError(t, db.Exec(
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, organization_id TEXT)`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO accounts (id, organization_id) VALUES ('acc-1', 'org-1')`).Error)

	svc := NewForFVT(db)
	repo, err := svc.repository()
	require.NoError(t, err)

	rows := []*AuditEvent{
		{ID: "e1", OrganizationID: "org-1", ResourceType: "project_member",
			ResourceID: "proj-1/u-9", Result: "success"},
		{ID: "e2", OrganizationID: "org-1", ResourceType: "account",
			ResourceID: "acc-1", Result: "success"},
	}
	events := svc.decorateEvents(context.Background(), repo, rows)
	require.Len(t, events, 2)
	assert.Equal(t, "Alice Chen", events[0].GetResourceDisplayName())
	assert.Equal(t, "Acme Labs", events[1].GetResourceDisplayName())
}

// A composite membership id must never reach the lookup: id columns are a
// mix of uuid and text, and PostgreSQL rejects a whole IN list when one
// value cannot cast to uuid.
func TestCandidateIDsAndPlausibility(t *testing.T) {
	got := candidateIDs("project_member", "proj-1/u-9")
	assert.Equal(t, []string{"u-9"}, got)
	assert.Equal(t, []string{"u-9"}, plausibleIDs(got))

	assert.Equal(t, []string{"proj-1/u-9"}, candidateIDs("model", "proj-1/u-9"))
	assert.Empty(t, plausibleIDs([]string{"proj-1/u-9"}))
	assert.Equal(t, []string{"id-1"}, plausibleIDs([]string{"id-1", "with space"}))
}
