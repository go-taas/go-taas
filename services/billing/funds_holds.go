package billing

// In-flight funds reservation (the Redis half of the admission gate).
//
// The accrual gate (usage_lines.accrual_micros plus the periodic release in
// reconciliation) bounds a tenant's overdraft to "usage that is written but
// not yet charged". It cannot bound a burst: N concurrent requests all read
// the same balance, all see money left, and all are admitted — the check and
// the spend are not atomic.
//
// A reservation closes that race. Admission atomically adds the request's
// worst-case cost to one hold set per *scope* and is refused when any scope no
// longer fits; metering removes the holds when the request is recorded, at
// which point the line's own accrual takes over. A hold that is never released
// (a crashed engine, a lost event) is swept after its TTL, so a leak costs a
// bounded delay rather than locked funds.
//
// Scope matters because an account is not the only ceiling: a project cap and a
// personal cap constrain the same request. Holding only at the organization
// level would leave those tiers overshootable by a burst, so one admission
// takes a hold in every scope that constrains it, all inside one script.
//
// Redis is an optimisation, never a dependency: when it is unavailable the
// gate falls back to accrual only, which is what shipped before this file
// existed.

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// fundsHoldKeyPrefix namespaces one organization's holds: a hash whose field is
// scope<sep>requestID and whose value is the reserved micros.
//
// All of an organization's holds live under one key pair on purpose: a Lua
// script is atomic over the keys it names, and Redis Cluster refuses a script
// that spans hash slots. One organization is one slot, so a multi-scope hold
// stays a single atomic step.
const fundsHoldKeyPrefix = "taas:billing:funds:hold:"

// fundsExpiryKeyPrefix namespaces the expiry index: a sorted set of the same
// fields, scored by the unix millisecond at which the hold dies.
const fundsExpiryKeyPrefix = "taas:billing:funds:expiry:"

// holdFieldSeparator separates the scope from the request id inside a hash
// field. It is ASCII SOH, which no scope or request id contains.
const holdFieldSeparator = "\x01"

// Scope constructors. The organization scope is the empty string, so a
// balance-level hold reads naturally.
const (
	scopeOrganization = ""
)

// userScope is one person's cap inside the organization.
func userScope(userID string) string { return "u:" + userID }

// projectScope is one project's cap.
func projectScope(projectID string) string { return "p:" + projectID }

// projectMemberScope is one person's cap inside one project.
func projectMemberScope(projectID, userID string) string {
	return "pm:" + projectID + ":" + userID
}

// FundsBound is one scope's ceiling for the request being admitted.
type FundsBound struct {
	// Scope names the ceiling: the organization (""), a person, a project, or
	// one member inside one project.
	Scope string
	// AvailableMicros is how much this scope may still spend before this
	// request, with the recorded accrual already subtracted. Holds are
	// deliberately *not* subtracted: the script adds them itself, and doing
	// both would refuse requests the tenant can still afford.
	AvailableMicros int64
}

// FundsReserver tracks admissions in flight. Every method is safe to call
// concurrently: the Redis implementation does its arithmetic inside a Lua
// script, so two admissions for the same organization are serialized by the
// server rather than by a client-side lock.
type FundsReserver interface {
	// Reserve atomically admits one request when its cost still fits in every
	// bound. It either takes all the holds or none of them, so a request
	// refused by its project cap leaves no organization hold behind.
	Reserve(ctx context.Context, orgID, requestID string, needMicros int64, bounds []FundsBound) (bool, error)
	// Release removes every hold of one request, returning the micros freed
	// (0 when the request held none: a redelivered usage event, or a request
	// admitted before reservations were enabled).
	Release(ctx context.Context, orgID, requestID string) (int64, error)
	// Outstanding returns the micros currently held for one scope, sweeping
	// holds whose TTL elapsed.
	Outstanding(ctx context.Context, orgID, scope string) (int64, error)
}

// reserveScript admits a request only when its cost fits in every bound, then
// takes one hold per bound.
//
// Sweeping first keeps a request that never reported usage from holding money
// forever. Re-reserving the same request id replaces its existing holds
// instead of adding a second set, because a gateway retry must not charge the
// tenant twice for the same request.
const reserveScript = `
local holds = KEYS[1]
local expiry = KEYS[2]
local requestID = ARGV[1]
local need = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])

-- Redis is the clock: every app replica must agree on when a hold dies, and
-- a replica's own clock is the one thing they do not share.
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

local expired = redis.call('ZRANGEBYSCORE', expiry, '-inf', now)
for _, field in ipairs(expired) do
  redis.call('HDEL', holds, field)
  redis.call('ZREM', expiry, field)
end

local sep = '\1'
local used = {}
local own = {}
for _, field in ipairs(redis.call('HKEYS', holds)) do
  local at = string.find(field, sep, 1, true)
  if at then
    local scope = string.sub(field, 1, at - 1)
    local rid = string.sub(field, at + 1)
    local amount = tonumber(redis.call('HGET', holds, field)) or 0
    used[scope] = (used[scope] or 0) + amount
    if rid == requestID then
      own[scope] = (own[scope] or 0) + amount
    end
  end
end

-- ARGV[4..] are scope,available pairs. Every one must fit, or nothing is held.
local bounds = {}
for i = 4, #ARGV, 2 do
  local scope = ARGV[i]
  local available = tonumber(ARGV[i + 1])
  local spent = (used[scope] or 0) - (own[scope] or 0)
  if spent + need > available then
    return 0
  end
  bounds[#bounds + 1] = scope
end

for _, scope in ipairs(bounds) do
  local field = scope .. sep .. requestID
  redis.call('HSET', holds, field, need)
  redis.call('ZADD', expiry, now + ttl, field)
end

-- The keys outlive their entries: an idle organization's keys expire on their
-- own, while a live one keeps its swept by the scripts.
redis.call('PEXPIRE', holds, ttl * 10)
redis.call('PEXPIRE', expiry, ttl * 10)
return 1
`

// releaseScript frees every hold of one request. Lua keeps the hash and the
// expiry index in step, which a pair of client commands cannot guarantee.
const releaseScript = `
local released = 0
local sep = '\1'
for _, field in ipairs(redis.call('HKEYS', KEYS[1])) do
  local at = string.find(field, sep, 1, true)
  if at and string.sub(field, at + 1) == ARGV[1] then
    released = released + (tonumber(redis.call('HGET', KEYS[1], field)) or 0)
    redis.call('HDEL', KEYS[1], field)
    redis.call('ZREM', KEYS[2], field)
  end
end
return released
`

// outstandingScript returns one scope's live holds, sweeping the expired ones.
const outstandingScript = `
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now)
for _, field in ipairs(expired) do
  redis.call('HDEL', KEYS[1], field)
  redis.call('ZREM', KEYS[2], field)
end

local prefix = ARGV[1] .. '\1'
local sum = 0
for _, field in ipairs(redis.call('HKEYS', KEYS[1])) do
  if string.sub(field, 1, #prefix) == prefix then
    sum = sum + (tonumber(redis.call('HGET', KEYS[1], field)) or 0)
  end
end
return sum
`

// redisFundsReserver implements FundsReserver on Redis.
type redisFundsReserver struct {
	client      *goredis.Client
	ttl         time.Duration
	reserve     *goredis.Script
	release     *goredis.Script
	outstanding *goredis.Script
}

// newRedisFundsReserver wraps the shared Redis client. A non-positive TTL
// falls back to ten minutes rather than to "no expiry": a hold must always be
// able to die.
func newRedisFundsReserver(client *goredis.Client, ttl time.Duration) *redisFundsReserver {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &redisFundsReserver{
		client:      client,
		ttl:         ttl,
		reserve:     goredis.NewScript(reserveScript),
		release:     goredis.NewScript(releaseScript),
		outstanding: goredis.NewScript(outstandingScript),
	}
}

// holdKeys returns one organization's hold keys.
func holdKeys(orgID string) []string {
	return []string{fundsHoldKeyPrefix + orgID, fundsExpiryKeyPrefix + orgID}
}

// Reserve implements FundsReserver.
func (r *redisFundsReserver) Reserve(ctx context.Context, orgID, requestID string, needMicros int64, bounds []FundsBound) (bool, error) {
	args := make([]interface{}, 0, 3+2*len(bounds))
	args = append(args, requestID, needMicros, r.ttl.Milliseconds())
	for _, b := range bounds {
		args = append(args, b.Scope, b.AvailableMicros)
	}
	ok, err := r.reserve.Run(ctx, r.client, holdKeys(orgID), args...).Int64()
	if err != nil {
		return false, err
	}
	return ok == 1, nil
}

// Release implements FundsReserver.
func (r *redisFundsReserver) Release(ctx context.Context, orgID, requestID string) (int64, error) {
	return r.release.Run(ctx, r.client, holdKeys(orgID), requestID).Int64()
}

// Outstanding implements FundsReserver.
func (r *redisFundsReserver) Outstanding(ctx context.Context, orgID, scope string) (int64, error) {
	return r.outstanding.Run(ctx, r.client, holdKeys(orgID), scope).Int64()
}
