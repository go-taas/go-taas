package fvt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/tenancy"
)

// TestFVTSeedCreatesNoDefaultOrganization verifies the first-boot seed
// leaves the platform with no default organization: the seeded admin is a
// platform administrator by account flag, which is what carries the role,
// and no organization or roster row is created for it. An account that
// belongs to nothing simply has no organization, which is the state the
// directory renders as "-".
func TestFVTSeedCreatesNoDefaultOrganization(t *testing.T) {
	env := newFVTEnv(t)
	ctx := context.Background()

	var before int64
	require.NoError(t, env.db.Model(&tenancy.Organization{}).Count(&before).Error)

	require.NoError(t, env.authSvc.EnsureComposeSeed(ctx))

	var after int64
	require.NoError(t, env.db.Model(&tenancy.Organization{}).Count(&after).Error)
	assert.Equal(t, before, after, "the seed creates no organization")

	var admin auth.User
	require.NoError(t, env.db.First(&admin, "username = ?", "admin").Error)
	assert.True(t, admin.PlatformAdmin, "the seeded admin is the platform administrator")

	var memberCount int64
	require.NoError(t, env.db.Model(&tenancy.OrgMember{}).
		Where("user_id = ?", admin.ID).Count(&memberCount).Error)
	assert.EqualValues(t, 0, memberCount, "the seed creates no membership")
}
