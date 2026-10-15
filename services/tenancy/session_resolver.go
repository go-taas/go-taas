package tenancy

import "context"

// SessionResolver resolves the authenticated caller's identity from the
// request context (feature #10). It is implemented by the auth module
// (which owns the session store) and injected into the tenancy service
// at wiring time so AcceptInvitation/RejectInvitation can resolve the
// caller. Nil until wired: unit tests skip session resolution.
type SessionResolver interface {
	// SessionUserID returns the authenticated caller's user id, or an
	// error (CodeSessionInvalid) when there is no valid session.
	SessionUserID(ctx context.Context) (string, error)
	// SessionUserEmail returns the authenticated caller's email, or an
	// error when there is no valid session.
	SessionUserEmail(ctx context.Context) (string, error)
}

// SessionOrgResolver resolves the session's active organization
// (feature #51, AD13). It is implemented by the auth module and
// injected into the tenancy service at wiring time so the quota RPCs
// can resolve the caller's org (GetMyQuota) and the admin role gate.
// Nil until wired: the transitional X-Organization-Id header is the
// fallback.
type SessionOrgResolver interface {
	// SessionActiveOrg returns the session's active organization, or an
	// error (CodeSessionInvalid) when there is no valid session.
	SessionActiveOrg(ctx context.Context) (string, error)
}
