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
)

// newQuotaTestDB opens an in-memory database with the tenancy schema.
func newQuotaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Organization{}, &Project{}, &OrgMember{}, &ProjectMember{}, &UserQuota{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestCombineLimitsTakesStrictestNonZero(t *testing.T) {
	org := QuotaLimits{MaxConcurrentRequests: 10, MaxBatchJobsPerDay: 90, MonthlySpendLimitCents: 5000}
	project := QuotaLimits{MaxConcurrentRequests: 30, MaxBatchJobsPerDay: 7}
	user := QuotaLimits{MaxConcurrentRequests: 4, MaxOutputTokensPerRequest: 1024}

	got := CombineLimits(org, project, user)
	assert.EqualValues(t, 4, got.MaxConcurrentRequests, "smallest non-zero wins")
	assert.EqualValues(t, 7, got.MaxBatchJobsPerDay, "the project cap is stricter than the organization cap")
	assert.EqualValues(t, 1024, got.MaxOutputTokensPerRequest)
	assert.EqualValues(t, 5000, got.MonthlySpendLimitCents)
}

func TestCombineLimitsZeroIsUnlimited(t *testing.T) {
	// Every tier unlimited -> unlimited.
	assert.Equal(t, QuotaLimits{}, CombineLimits(QuotaLimits{}, QuotaLimits{}))

	// One tier set, the rest unlimited -> that tier's value.
	got := CombineLimits(QuotaLimits{}, QuotaLimits{MaxConcurrentRequests: 9}, QuotaLimits{})
	assert.EqualValues(t, 9, got.MaxConcurrentRequests)

	// A zero never wins over a positive value.
	got = CombineLimits(QuotaLimits{MaxConcurrentRequests: 9}, QuotaLimits{MaxConcurrentRequests: 0})
	assert.EqualValues(t, 9, got.MaxConcurrentRequests)
}

func TestOrgQuotaLimitsProjection(t *testing.T) {
	q := OrgQuota{MaxConcurrentRequests: 1, MaxBatchJobsPerDay: 3,
		MaxOutputTokensPerRequest: 4, MonthlySpendLimitCents: 5}
	assert.Equal(t, QuotaLimits{1, 3, 4, 5}, q.Limits())
	assert.Equal(t, QuotaLimits{}, (*UserQuota)(nil).Limits())
}

func TestValidateQuotaMonthlySpendLimit(t *testing.T) {
	assert.NoError(t, ValidateQuota("monthly_spend_limit_cents", 0))
	assert.NoError(t, ValidateQuota("monthly_spend_limit_cents", 100))
	assert.Error(t, ValidateQuota("monthly_spend_limit_cents", -1))
}

func TestUserQuotaRepositoryRoundTrip(t *testing.T) {
	db := newQuotaTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// A missing row reads as unlimited, not an error.
	row, err := repo.FindUserQuota(ctx, "org-a", "u1")
	require.NoError(t, err)
	assert.Nil(t, row)
	limits, err := repo.UserQuotaLimits(ctx, "org-a", "u1")
	require.NoError(t, err)
	assert.Equal(t, QuotaLimits{}, limits)

	require.NoError(t, repo.UpsertUserQuota(ctx, &UserQuota{
		OrganizationID: "org-a", UserID: "u1", MaxConcurrentRequests: 3,
	}))

	row, err = repo.FindUserQuota(ctx, "org-a", "u1")
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.EqualValues(t, 3, row.MaxConcurrentRequests)

	// Update patches only the named dimension.
	require.NoError(t, repo.UpdateUserQuota(ctx, "org-a", "u1",
		map[string]int64{"max_output_tokens_per_request": 2048}, "admin", time.Now().UTC()))
	row, err = repo.FindUserQuota(ctx, "org-a", "u1")
	require.NoError(t, err)
	assert.EqualValues(t, 3, row.MaxConcurrentRequests)
	assert.EqualValues(t, 2048, row.MaxOutputTokensPerRequest)

	// The list is scoped and name-filtered.
	require.NoError(t, repo.UpsertUserQuota(ctx, &UserQuota{
		OrganizationID: "org-a", UserID: "u2", MaxConcurrentRequests: 5,
	}))
	rows, total, err := repo.ListUserQuotas(ctx, UserQuotaFilter{OrganizationID: "org-a", Limit: 10})
	require.NoError(t, err)
	assert.Len(t, rows, 2)
	assert.EqualValues(t, 2, total)

	rows, _, err = repo.ListUserQuotas(ctx, UserQuotaFilter{OrganizationID: "org-a", UserIDs: []string{"u2"}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "u2", rows[0].UserID)

	rows, total, err = repo.ListUserQuotas(ctx, UserQuotaFilter{OrganizationID: "org-a", UserIDs: []string{}})
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.EqualValues(t, 0, total)
}

func TestProjectQuotaLimitsMissingProject(t *testing.T) {
	db := newQuotaTestDB(t)
	repo := NewRepository(db)
	limits, err := repo.ProjectQuotaLimits(context.Background(), "missing")
	require.NoError(t, err)
	assert.Equal(t, QuotaLimits{}, limits)

	limits, err = repo.ProjectQuotaLimits(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, QuotaLimits{}, limits)

	require.NoError(t, db.Create(&Project{
		ID: "p1", OrganizationID: "org-a", DisplayName: "P", State: StateActive,
		MaxConcurrentRequests: 12, MonthlySpendLimitCents: 900,
	}).Error)
	limits, err = repo.ProjectQuotaLimits(context.Background(), "p1")
	require.NoError(t, err)
	assert.EqualValues(t, 12, limits.MaxConcurrentRequests)
	assert.EqualValues(t, 900, limits.MonthlySpendLimitCents)
}

func TestServiceEffectiveQuotaCombinesTiers(t *testing.T) {
	db := newQuotaTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&Organization{
		ID: "org-a", DisplayName: "A", State: StateActive, MaxConcurrentRequests: 10,
	}).Error)
	require.NoError(t, db.Create(&Project{
		ID: "p1", OrganizationID: "org-a", DisplayName: "P", State: StateActive, MaxConcurrentRequests: 20,
	}).Error)
	require.NoError(t, db.Create(&UserQuota{
		OrganizationID: "org-a", UserID: "u1", MaxConcurrentRequests: 5,
	}).Error)

	svc := NewForFVT(db)
	got, err := svc.EffectiveQuota(ctx, "org-a", "p1", "u1")
	require.NoError(t, err)
	assert.EqualValues(t, 5, got.MaxConcurrentRequests)

	// Unknown scope reads as unlimited.
	got, err = svc.EffectiveQuota(ctx, "org-missing", "", "")
	require.NoError(t, err)
	assert.Equal(t, QuotaLimits{}, got)
}

func TestMigrateProjectMemberQuotasKeepsStrictest(t *testing.T) {
	db := newQuotaTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&Project{ID: "p1", OrganizationID: "org-a", DisplayName: "P", State: StateActive}).Error)
	require.NoError(t, db.Create(&Project{ID: "p2", OrganizationID: "org-a", DisplayName: "Q", State: StateActive}).Error)
	require.NoError(t, db.Create(&ProjectMember{ProjectID: "p1", UserID: "u1", Role: RoleMember,
		JoinedAt: time.Now().UTC(), MaxConcurrentRequests: 30}).Error)
	require.NoError(t, db.Create(&ProjectMember{ProjectID: "p2", UserID: "u1", Role: RoleMember,
		JoinedAt: time.Now().UTC(), MaxConcurrentRequests: 20}).Error)
	// u2 already has a personal budget; it must not be overwritten.
	require.NoError(t, db.Create(&UserQuota{OrganizationID: "org-a", UserID: "u2", MaxConcurrentRequests: 99}).Error)
	require.NoError(t, db.Create(&ProjectMember{ProjectID: "p1", UserID: "u2", Role: RoleMember,
		JoinedAt: time.Now().UTC(), MaxConcurrentRequests: 1}).Error)

	repo := NewRepository(db)
	require.NoError(t, repo.MigrateProjectMemberQuotas(ctx))

	var u1 UserQuota
	require.NoError(t, db.Where("organization_id = ? AND user_id = ?", "org-a", "u1").First(&u1).Error)
	assert.EqualValues(t, 20, u1.MaxConcurrentRequests)

	var u2 UserQuota
	require.NoError(t, db.Where("organization_id = ? AND user_id = ?", "org-a", "u2").First(&u2).Error)
	assert.EqualValues(t, 99, u2.MaxConcurrentRequests)
}
