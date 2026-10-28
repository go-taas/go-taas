package authctx

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// countingResolver records how many times the roles were resolved.
type countingResolver struct {
	roles []string
	err   error
	calls int
}

func (r *countingResolver) SessionRoles(context.Context) ([]string, error) {
	r.calls++
	return r.roles, r.err
}

// TestIsPlatformAdmin verifies the caller's platform-administrator flag is
// readable from the context.
func TestIsPlatformAdmin(t *testing.T) {
	ctx := WithResolver(context.Background(), &countingResolver{roles: []string{"platform-admin", "admin"}})
	assert.True(t, IsPlatformAdmin(ctx))

	ctx = WithResolver(context.Background(), &countingResolver{roles: []string{"admin"}})
	assert.False(t, IsPlatformAdmin(ctx))
}

// TestIsPlatformAdminWithoutResolver verifies a request that never passed
// through the interceptor is treated as an ordinary caller.
func TestIsPlatformAdminWithoutResolver(t *testing.T) {
	assert.False(t, IsPlatformAdmin(context.Background()))
	assert.Nil(t, Roles(context.Background()))
}

// TestRolesResolvedOnce verifies the resolver runs at most once per
// request, so the session lookup is not repeated.
func TestRolesResolvedOnce(t *testing.T) {
	r := &countingResolver{roles: []string{"member"}}
	ctx := WithResolver(context.Background(), r)
	for i := 0; i < 3; i++ {
		assert.Equal(t, []string{"member"}, Roles(ctx))
	}
	assert.Equal(t, 1, r.calls)
}

// TestRolesResolutionFailure verifies a failed lookup leaves the caller
// without roles rather than failing the request.
func TestRolesResolutionFailure(t *testing.T) {
	ctx := WithResolver(context.Background(), &countingResolver{err: errors.New("no session")})
	assert.Nil(t, Roles(ctx))
	assert.False(t, IsPlatformAdmin(ctx))
}

// TestWithResolverNil verifies the helper tolerates a nil resolver.
func TestWithResolverNil(t *testing.T) {
	assert.False(t, IsPlatformAdmin(WithResolver(context.Background(), nil)))
}
