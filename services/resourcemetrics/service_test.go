package resourcemetrics

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	resourcemetricsv1 "github.com/go-taas/go-taas/proto/taas/resourcemetrics/v1"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

type fakeSessionOrgResolver struct{ org string }

func (f fakeSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) {
	return f.org, nil
}

type fakeSessionUserResolver struct{ user string }

func (f fakeSessionUserResolver) SessionUserID(context.Context) (string, error) {
	return f.user, nil
}

type fakeRoleGuard struct{ allowed bool }

func (f fakeRoleGuard) RequireRole(_ context.Context, _, _, _ string) error {
	if !f.allowed {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

type fakeServiceExists struct{ exists bool }

func (f fakeServiceExists) ServiceExists(context.Context, string) (bool, error) {
	return f.exists, nil
}

func newServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&serviceResourceMetricRow{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func newService(t *testing.T, db *gorm.DB, org string, roleAllowed bool) *Service {
	t.Helper()
	svc := NewForFVT(db)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: org})
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetRoleGuard(fakeRoleGuard{allowed: roleAllowed})
	svc.SetServiceExists(fakeServiceExists{exists: true})
	return svc
}

func ctxWithOrg(ctx context.Context, org string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs("x-organization-id", org))
}

func TestGetServiceResourceMetrics(t *testing.T) {
	db := newServiceTestDB(t)
	seedRows(t, db, "svc-1")
	svc := newService(t, db, "org-1", true)

	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()
	until := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC).Unix()

	// AC1: valid range returns cards, series, replicas.
	resp, err := svc.GetServiceResourceMetrics(ctxWithOrg(context.Background(), "org-1"), &resourcemetricsv1.GetServiceResourceMetricsRequest{
		ServiceId: "svc-1", Since: since, Until: until,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Cards)
	assert.Equal(t, int64(2), resp.Cards.ReplicaCount)
	assert.Equal(t, since, resp.Cards.DataThrough)
	require.Len(t, resp.Series, 2)
	require.Len(t, resp.Replicas, 2)
	assert.Equal(t, "replica-1", resp.Replicas[0].ReplicaIndex)

	// AC1: since > until returns 10404.
	_, err = svc.GetServiceResourceMetrics(ctxWithOrg(context.Background(), "org-1"), &resourcemetricsv1.GetServiceResourceMetricsRequest{
		ServiceId: "svc-1", Since: until, Until: since,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, apierrors.CodeOf(err))

	// AC1: range > 92 days returns 10404.
	_, err = svc.GetServiceResourceMetrics(ctxWithOrg(context.Background(), "org-1"), &resourcemetricsv1.GetServiceResourceMetricsRequest{
		ServiceId: "svc-1", Since: since, Until: since + 93*24*3600,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, apierrors.CodeOf(err))

	// AC1: unknown service returns 10301.
	svc.SetServiceExists(fakeServiceExists{exists: false})
	_, err = svc.GetServiceResourceMetrics(ctxWithOrg(context.Background(), "org-1"), &resourcemetricsv1.GetServiceResourceMetricsRequest{
		ServiceId: "missing", Since: since, Until: until,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, apierrors.CodeOf(err))
	svc.SetServiceExists(fakeServiceExists{exists: true})

	// AC8: role denied returns 10036.
	denied := newService(t, db, "org-1", false)
	_, err = denied.GetServiceResourceMetrics(ctxWithOrg(context.Background(), "org-1"), &resourcemetricsv1.GetServiceResourceMetricsRequest{
		ServiceId: "svc-1", Since: since, Until: until,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// AD9: invalid metric returns 12101.
	_, err = svc.GetServiceResourceMetrics(ctxWithOrg(context.Background(), "org-1"), &resourcemetricsv1.GetServiceResourceMetricsRequest{
		ServiceId: "svc-1", Since: since, Until: until, Metric: "bogus",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeServiceMetricsInvalid, apierrors.CodeOf(err))
}

func TestGetServiceResourceMetricsReplicaFilter(t *testing.T) {
	db := newServiceTestDB(t)
	seedRows(t, db, "svc-1")
	svc := newService(t, db, "org-1", true)

	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()
	until := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC).Unix()

	// AC5: replica filter narrows the series.
	resp, err := svc.GetServiceResourceMetrics(ctxWithOrg(context.Background(), "org-1"), &resourcemetricsv1.GetServiceResourceMetricsRequest{
		ServiceId: "svc-1", Since: since, Until: until, Replica: "replica-1",
	})
	require.NoError(t, err)
	require.Len(t, resp.Replicas, 1)
	assert.Equal(t, "replica-1", resp.Replicas[0].ReplicaIndex)
	assert.Equal(t, int64(1), resp.Cards.ReplicaCount)
}

func TestValidMetric(t *testing.T) {
	assert.True(t, validMetric(""))
	assert.True(t, validMetric("cpu"))
	assert.True(t, validMetric("memory"))
	assert.True(t, validMetric("gpu"))
	assert.False(t, validMetric("bogus"))
}