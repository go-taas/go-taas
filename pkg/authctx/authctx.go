// Package authctx carries the authenticated caller's session roles on the
// request context, so a service can tell a platform administrator from an
// ordinary caller without wiring a resolver of its own.
//
// The role set is resolved lazily: the interceptor only parks the resolver
// on the context, and the first caller that asks pays for one session
// lookup. Most requests never ask, so the extra cost is zero.
package authctx

import (
	"context"
	"sync"
)

// PlatformAdminRole is the session role of a platform administrator. It is
// duplicated here to keep this package dependency-free; the tenancy module
// owns the authoritative constant.
const PlatformAdminRole = "platform-admin"

// RolesResolver resolves the caller's session roles. It is implemented by
// the auth service.
type RolesResolver interface {
	SessionRoles(ctx context.Context) ([]string, error)
}

// holder memoizes one request's role lookup.
type holder struct {
	resolver RolesResolver
	once     sync.Once
	roles    []string
}

type holderKey struct{}

// WithResolver parks the roles resolver on the context. The interceptor
// does this for every call; resolving the roles is deferred until a caller
// asks.
func WithResolver(ctx context.Context, r RolesResolver) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, holderKey{}, &holder{resolver: r})
}

// Roles returns the caller's session roles, or nil when no session could be
// resolved (an unauthenticated or transitional request).
func Roles(ctx context.Context) []string {
	h, ok := ctx.Value(holderKey{}).(*holder)
	if !ok {
		return nil
	}
	h.once.Do(func() {
		if roles, err := h.resolver.SessionRoles(ctx); err == nil {
			h.roles = roles
		}
	})
	return h.roles
}

// IsPlatformAdmin reports whether the caller is a platform administrator.
// Without a resolver on the context it is false, so a request that never
// passed through the interceptor is treated as an ordinary caller.
func IsPlatformAdmin(ctx context.Context) bool {
	for _, r := range Roles(ctx) {
		if r == PlatformAdminRole {
			return true
		}
	}
	return false
}
