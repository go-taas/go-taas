package auth

// Funds gating on the data plane.
//
// Verifying a key answers "may this credential call this model"; it does
// not answer "may this tenant still afford it". Without the second
// question a drained prepaid balance keeps working: metering settles each
// closed hour, so the money a caller spends in the current hour has not
// been deducted yet, and a negative balance would only ever be discovered
// after the fact.
//
// The billing module owns the truth table (prepaid balance, postpaid
// quota, the three-level spend caps) and nets the open hour's uncharged
// accrual into it, so the answer already accounts for money this tenant
// spent but settlement has not booked yet. This file is the seam that asks
// it per inference request, with a short in-process cache so the control
// plane is not hit once per token.

import (
	"context"
	"strings"
	"sync"
	"time"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// fundsCacheTTL bounds how stale a balance decision may be. It is short on
// purpose: the longer the window, the more a caller can spend after the
// balance runs out. Five seconds matches the design's gateway-side cache.
const fundsCacheTTL = 5 * time.Second

// fundsCacheMaxEntries bounds the in-process cache. The key is an
// organization (plus attribution), so the working set is the tenant count;
// the cap only guards against a pathological caller mix.
const fundsCacheMaxEntries = 4096

// FundsGate answers whether an organization may consume inference right
// now. It is implemented by the billing module and injected at wiring time.
//
// allowed=false denies the request. An error is an infrastructure failure,
// not a verdict: the caller logs it and allows the request (fail-open), so
// a billing outage never takes inference down.
type FundsGate interface {
	// CheckInferenceAllowed evaluates the account rule and the
	// organization, project and personal spend caps.
	//
	// requestID names the admission. When it is set, the implementation
	// reserves this request's cost so two callers cannot both spend the
	// last of a balance; when it is empty (the playground, an internal
	// caller) the answer is a plain read. model and maxOutputTokens size
	// that reservation: the output is the only part of a request whose
	// size is known before it runs.
	CheckInferenceAllowed(ctx context.Context, orgID, ownerUserID, projectID, requestID, model string, maxOutputTokens int64) (allowed bool, reason string, err error)

	// ReservationActive reports whether admissions are reserving cost. When
	// true, every request needs its own verification: a reused verdict
	// would skip its reservation.
	ReservationActive() bool
}

// fundsHint carries the gateway's request identity, which the verification
// RPC has no field for: it is a transport detail between the Wasm plugin and
// this service, not part of the API a caller can set. A missing hint means
// the request is admitted on the accrual gate alone.
type fundsHint struct {
	// RequestID is the idempotency key metering will record.
	RequestID string
	// MaxOutputTokens is the caller's requested output size, after any
	// clamp the gateway applied. 0 means the request did not say.
	MaxOutputTokens int64
}

// fundsHintKey is the context key of the hint. It is unexported so nothing
// outside the auth package can forge one.
type fundsHintKey struct{}

// WithFundsHint attaches the gateway-side request identity to ctx. Only the
// internal gateway API calls it: a key verified through any other path has
// no request to reserve for yet.
func WithFundsHint(ctx context.Context, requestID string, maxOutputTokens int64) context.Context {
	return context.WithValue(ctx, fundsHintKey{}, fundsHint{
		RequestID:       strings.TrimSpace(requestID),
		MaxOutputTokens: maxOutputTokens,
	})
}

// fundsHintFrom returns the hint attached by the gateway path, if any.
func fundsHintFrom(ctx context.Context) fundsHint {
	hint, _ := ctx.Value(fundsHintKey{}).(fundsHint)
	return hint
}

// SetFundsGate injects the billing-side funds check. Nil until wired: the
// data plane then runs ungated, the transitional behaviour.
func (s *Service) SetFundsGate(g FundsGate) { s.fundsGate = g }

// fundsDecision is one cached answer.
type fundsDecision struct {
	allowed   bool
	reason    string
	expiresAt time.Time
}

// fundsGateCache is a tiny TTL map. It is deliberately per process: a
// replica that has not seen a request in the last five seconds re-reads the
// account, which is both cheap and correct.
type fundsGateCache struct {
	mu      sync.Mutex
	entries map[string]fundsDecision
}

func (c *fundsGateCache) get(key string) (fundsDecision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return fundsDecision{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		return fundsDecision{}, false
	}
	return entry, true
}

func (c *fundsGateCache) set(key string, decision fundsDecision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]fundsDecision, 64)
	}
	if len(c.entries) >= fundsCacheMaxEntries {
		// Drop expired entries first; if none are expired, clear the map.
		// A cold cache costs one billing read per key, never correctness.
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= fundsCacheMaxEntries {
			c.entries = make(map[string]fundsDecision, 64)
		}
	}
	c.entries[key] = decision
}

// fundsCacheKey scopes a decision to the attribution tiers the check
// evaluates: the same organization can be allowed for one project and
// blocked for another.
func fundsCacheKey(orgID, ownerUserID, projectID string) string {
	return orgID + "\x00" + ownerUserID + "\x00" + projectID
}

// checkInferenceFunds applies the funds gate to a verified key. A nil gate
// (not wired), a platform-system credential, or a billing failure all allow
// the request: the gate must never be the reason inference stops.
//
// A request that names itself (the gateway path) is never answered from the
// cache. The cache exists to save a control-plane read, but a cached "yes"
// also skips the reservation that makes the decision atomic, so it would
// reopen exactly the race the reservation closes.
func (s *Service) checkInferenceFunds(ctx context.Context, verdict *keyVerdict, model string) error {
	if s.fundsGate == nil || verdict == nil || verdict.IsSystem {
		return nil
	}
	orgID := strings.TrimSpace(verdict.OrganizationID)
	if orgID == "" {
		return nil
	}
	hint := fundsHintFrom(ctx)
	// Only a reservation forces a fresh answer: without one the cached
	// verdict is still exactly true, and the cache saves the read.
	reserving := hint.RequestID != "" && s.fundsGate.ReservationActive()
	key := fundsCacheKey(orgID, verdict.OwnerUserID, verdict.ProjectID)
	if !reserving {
		if cached, ok := s.fundsCache.get(key); ok {
			if cached.allowed {
				return nil
			}
			return insufficientFundsError(cached.reason)
		}
	}

	// Reserve what the request will actually be allowed to produce: the
	// gateway clamps max_tokens to the organization's cap, so an unclamped
	// figure would hold far more than the request can spend and refuse
	// callers who can afford the call.
	maxTokens := hint.MaxOutputTokens
	if cap := verdict.MaxOutputTokensPerRequest; cap > 0 && (maxTokens <= 0 || maxTokens > cap) {
		maxTokens = cap
	}
	allowed, reason, err := s.fundsGate.CheckInferenceAllowed(ctx,
		orgID, verdict.OwnerUserID, verdict.ProjectID,
		hint.RequestID, model, maxTokens)
	if err != nil {
		// Fail open: the account rule was not readable, which is a platform
		// problem. Inference keeps working and money is still accounted at
		// settlement; the alternative is an outage caused by a slow query.
		logger.S().Warnw("auth: funds check failed, allowing the request",
			"org", orgID, "key", verdict.KeyID, "err", err)
		return nil
	}
	if !reserving {
		s.fundsCache.set(key, fundsDecision{
			allowed:   allowed,
			reason:    reason,
			expiresAt: time.Now().Add(fundsCacheTTL),
		})
	}
	if allowed {
		return nil
	}
	return insufficientFundsError(reason)
}

// insufficientFundsError renders the denial. The message names the reason so
// an operator reading a gateway log can tell a drained prepaid balance from
// a reached spend cap without querying the account.
func insufficientFundsError(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return apierrors.New(apierrors.CodeInsufficientFunds)
	}
	return apierrors.Newf(apierrors.CodeInsufficientFunds,
		"insufficient funds: %s; top up the balance or raise the spend limit", reason)
}
