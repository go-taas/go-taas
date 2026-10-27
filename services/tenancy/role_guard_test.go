package tenancy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)


// fakeSessionRoles is a SessionRolesResolver returning a fixed role set.
type fakeSessionRoles struct{ roles []string }

func (f fakeSessionRoles) SessionRoles(context.Context) ([]string, error) { return f.roles, nil }

// TestRoleGuardPlatformAdminWithoutMembership verifies a platform
// administrator satisfies an organization gate with no membership row -
// the account carries the role, and the platform owns no tenant - while a
// plain caller stays confined to the organizations they joined.
func TestRoleGuardPlatformAdminWithoutMembership(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Create(&Organization{ID: "org-a", DisplayName: "A", State: StateActive}).Error)

	platformGuard := NewRoleGuardWithSessions(db, fakeSessionRoles{roles: []string{RolePlatformAdmin}})
	require.NoError(t, platformGuard.RequireRole(context.Background(), "org-a", "u-1", RoleAdmin))

	// The same guard leaves a non-platform caller confined.
	plainGuard := NewRoleGuardWithSessions(db, fakeSessionRoles{roles: []string{RoleMember}})
	require.Error(t, plainGuard.RequireRole(context.Background(), "org-a", "u-1", RoleAdmin))

	// Project management answers to the membership row alone, so the
	// platform gate does not satisfy it.
	err := platformGuard.RequireMemberRole(context.Background(), "org-a", "u-1", RoleAdmin)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// With a real membership row it does.
	repo := NewMembershipRepository(db)
	require.NoError(t, repo.AddMember(context.Background(), &OrgMember{
		OrganizationID: "org-a", UserID: "u-1", Role: RoleOwner,
	}))
	require.NoError(t, platformGuard.RequireMemberRole(context.Background(), "org-a", "u-1", RoleAdmin))
}
