package tenancy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newUserQuotaServiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Organization{}, &Project{}, &OrgMember{}, &ProjectMember{}, &UserQuota{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	require.NoError(t, db.Create(&Organization{ID: "org-a", DisplayName: "A", State: StateActive}).Error)
	for _, u := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		require.NoError(t, db.Create(&OrgMember{
			OrganizationID: "org-a", UserID: u, Role: RoleMember, JoinedAt: time.Now().UTC(),
		}).Error)
	}
	return db
}

func TestListUserQuotasIsRosterDriven(t *testing.T) {
	svc := NewForFVT(newUserQuotaServiceDB(t))
	resp, err := svc.ListUserQuotas(context.Background(), &tenancyv1.ListUserQuotasRequest{
		OrganizationId: "org-a",
	})
	require.NoError(t, err)
	require.Len(t, resp.GetQuotas(), 2)
	for _, q := range resp.GetQuotas() {
		assert.Equal(t, "member", q.GetRole())
		assert.EqualValues(t, 0, q.GetMaxConcurrentRequests())
	}

	// A name filter narrows the roster.
	resp, err = svc.ListUserQuotas(context.Background(), &tenancyv1.ListUserQuotasRequest{
		OrganizationId: "org-a", Name: "22222222-2222-4222-8222-222222222222",
	})
	require.NoError(t, err)
	require.Len(t, resp.GetQuotas(), 1)
	assert.Equal(t, "22222222-2222-4222-8222-222222222222", resp.GetQuotas()[0].GetUserId())

	// An unknown organization is rejected.
	_, err = svc.ListUserQuotas(context.Background(), &tenancyv1.ListUserQuotasRequest{
		OrganizationId: "missing",
	})
	assert.Equal(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))
}

func TestUpdateUserQuotaValidatesAndCombines(t *testing.T) {
	db := newUserQuotaServiceDB(t)
	require.NoError(t, db.Model(&Organization{}).Where("id = ?", "org-a").
		Update("max_concurrent_requests", 10).Error)
	svc := NewForFVT(db)

	resp, err := svc.UpdateUserQuota(context.Background(), &tenancyv1.UpdateUserQuotaRequest{
		UserId:                "11111111-1111-4111-8111-111111111111",
		OrganizationId:        "org-a",
		MaxConcurrentRequests: i64ptr(4),
		MonthlySpendLimitCents: i64ptr(1500),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 4, resp.GetQuota().GetMaxConcurrentRequests())
	assert.EqualValues(t, 4, resp.GetEffectiveQuota().GetMaxConcurrentRequests(), "the org cap of 10 does not override 4")
	assert.EqualValues(t, 1500, resp.GetQuota().GetMonthlySpendLimitCents())

	// The effective value reports the organization cap when the personal
	// one is cleared.
	resp, err = svc.UpdateUserQuota(context.Background(), &tenancyv1.UpdateUserQuotaRequest{
		UserId:                "11111111-1111-4111-8111-111111111111",
		OrganizationId:        "org-a",
		MaxConcurrentRequests: i64ptr(0),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 10, resp.GetEffectiveQuota().GetMaxConcurrentRequests())

	// Out-of-range values are rejected.
	_, err = svc.UpdateUserQuota(context.Background(), &tenancyv1.UpdateUserQuotaRequest{
		UserId: "11111111-1111-4111-8111-111111111111", OrganizationId: "org-a", MaxConcurrentRequests: i64ptr(10001),
	})
	assert.Equal(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(err))
	_, err = svc.UpdateUserQuota(context.Background(), &tenancyv1.UpdateUserQuotaRequest{
		UserId: "11111111-1111-4111-8111-111111111111", OrganizationId: "org-a", MonthlySpendLimitCents: i64ptr(-1),
	})
	assert.Equal(t, apierrors.CodeQuotaInvalid, apierrors.CodeOf(err))

	// A user outside the organization is rejected.
	_, err = svc.UpdateUserQuota(context.Background(), &tenancyv1.UpdateUserQuotaRequest{
		UserId: "stranger", OrganizationId: "org-a", MaxConcurrentRequests: i64ptr(1),
	})
	assert.Equal(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}

func TestGetUserQuota(t *testing.T) {
	db := newUserQuotaServiceDB(t)
	svc := NewForFVT(db)
	ctx := context.Background()

	resp, err := svc.GetUserQuota(ctx, &tenancyv1.GetUserQuotaRequest{UserId: "11111111-1111-4111-8111-111111111111", OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.Equal(t, "11111111-1111-4111-8111-111111111111", resp.GetQuota().GetUserId())
	assert.EqualValues(t, 0, resp.GetQuota().GetMaxConcurrentRequests())

	require.NoError(t, db.Create(&UserQuota{
		OrganizationID: "org-a", UserID: "11111111-1111-4111-8111-111111111111", MaxBatchJobsPerDay: 3,
	}).Error)
	resp, err = svc.GetUserQuota(ctx, &tenancyv1.GetUserQuotaRequest{UserId: "11111111-1111-4111-8111-111111111111", OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.EqualValues(t, 3, resp.GetQuota().GetMaxBatchJobsPerDay())

	_, err = svc.GetUserQuota(ctx, &tenancyv1.GetUserQuotaRequest{UserId: "", OrganizationId: "org-a"})
	assert.Equal(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}

func i64ptr(v int64) *int64 { return &v }
