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

func newLookupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Organization{}, &Project{}, &OrgMember{}, &ProjectMember{}, &UserQuota{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	require.NoError(t, db.Create(&Project{ID: "p1", OrganizationID: "org-a", DisplayName: "Project One", State: StateActive}).Error)
	require.NoError(t, db.Create(&OrgMember{OrganizationID: "org-a", UserID: "u1", Role: RoleMember, JoinedAt: time.Now().UTC()}).Error)
	require.NoError(t, db.Create(&ProjectMember{ProjectID: "p1", UserID: "u1", Role: RoleMember, JoinedAt: time.Now().UTC()}).Error)
	return db
}

func TestProjectLookups(t *testing.T) {
	svc := NewForFVT(newLookupTestDB(t))
	ctx := context.Background()

	name, ok, err := svc.ProjectInOrg(ctx, "p1", "org-a")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Project One", name)

	_, ok, err = svc.ProjectInOrg(ctx, "p1", "org-b")
	require.NoError(t, err)
	assert.False(t, ok)

	_, ok, err = svc.ProjectInOrg(ctx, "", "org-a")
	require.NoError(t, err)
	assert.False(t, ok)

	_, ok, err = svc.ProjectInOrg(ctx, "missing", "org-a")
	require.NoError(t, err)
	assert.False(t, ok)

	name, err = svc.ProjectName(ctx, "p1")
	require.NoError(t, err)
	assert.Equal(t, "Project One", name)
	name, err = svc.ProjectName(ctx, "missing")
	require.NoError(t, err)
	assert.Empty(t, name)
}

func TestUserMembershipLookups(t *testing.T) {
	svc := NewForFVT(newLookupTestDB(t))
	ctx := context.Background()

	inProject, err := svc.UserInProject(ctx, "p1", "u1")
	require.NoError(t, err)
	assert.True(t, inProject)
	inProject, err = svc.UserInProject(ctx, "p1", "u2")
	require.NoError(t, err)
	assert.False(t, inProject)
	inProject, err = svc.UserInProject(ctx, "", "")
	require.NoError(t, err)
	assert.False(t, inProject)

	inOrg, err := svc.UserInOrg(ctx, "org-a", "u1")
	require.NoError(t, err)
	assert.True(t, inOrg)
	inOrg, err = svc.UserInOrg(ctx, "org-a", "u2")
	require.NoError(t, err)
	assert.False(t, inOrg)
	inOrg, err = svc.UserInOrg(ctx, "", "u1")
	require.NoError(t, err)
	assert.False(t, inOrg)
}

func TestMonthlySpendLimitLookups(t *testing.T) {
	db := newLookupTestDB(t)
	require.NoError(t, db.Model(&Project{}).Where("id = ?", "p1").
		Update("monthly_spend_limit_cents", 7500).Error)
	require.NoError(t, db.Create(&UserQuota{
		OrganizationID: "org-a", UserID: "u1", MonthlySpendLimitCents: 2500,
	}).Error)
	svc := NewForFVT(db)
	ctx := context.Background()

	limit, err := svc.ProjectMonthlySpendLimitCents(ctx, "p1")
	require.NoError(t, err)
	assert.EqualValues(t, 7500, limit)
	limit, err = svc.ProjectMonthlySpendLimitCents(ctx, "missing")
	require.NoError(t, err)
	assert.EqualValues(t, 0, limit)

	limit, err = svc.UserMonthlySpendLimitCents(ctx, "org-a", "u1")
	require.NoError(t, err)
	assert.EqualValues(t, 2500, limit)
	limit, err = svc.UserMonthlySpendLimitCents(ctx, "org-a", "nobody")
	require.NoError(t, err)
	assert.EqualValues(t, 0, limit)
	limit, err = svc.UserMonthlySpendLimitCents(ctx, "", "u1")
	require.NoError(t, err)
	assert.EqualValues(t, 0, limit)
}
