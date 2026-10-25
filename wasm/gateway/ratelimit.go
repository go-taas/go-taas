// Copyright 2025 The go-taas Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

// Per-key request-rate limiting.
//
// The platform promises the gateway enforces `rate_limit_rpm` locally (see
// VerifyAPIKeyResponse), so leaving it out would ship the contract half
// implemented. Envoy gives a Wasm filter no memory shared across worker
// threads, so each worker keeps its own bucket: with N workers a key may
// exceed its limit by up to N×. That is an approximation, not a guarantee,
// and it is the best available one without moving the counter to Redis.
//
// The exact form — a lease decremented atomically in Redis, which also
// supports the concurrency cap — is the documented follow-up.

import "strings"

// rateLimitState is one key's token bucket, serialized into shared data.
//
// It is stored as text rather than a struct because proxy-wasm shared data
// is bytes, and the encoding has to survive a worker restart mid-window.
type rateLimitState struct {
	tokens     float64 // whole requests available right now
	lastRefill int64   // coarse-clock seconds
}

// allowRequest decides whether the request fits the key's budget.
//
// rpm <= 0 means unlimited. The bucket holds at most one minute's worth of
// requests, so a burst is absorbed but an idle key cannot bank credit
// indefinitely — that is the difference between rate limiting and a quota.
func allowRequest(state *rateLimitState, rpm int64, nowSeconds int64) (allowed bool, next *rateLimitState) {
	if rpm <= 0 {
		return true, state
	}
	capacity := float64(rpm)
	if state == nil {
		// First request in this window: start with a full bucket, which
		// matches nginx/Envoy burst semantics and avoids penalizing a cold
		// key's very first call.
		return true, &rateLimitState{tokens: capacity - 1, lastRefill: nowSeconds}
	}
	elapsed := nowSeconds - state.lastRefill
	if elapsed < 0 {
		elapsed = 0
	}
	tokens := state.tokens + (float64(elapsed)/60.0)*capacity
	if tokens > capacity {
		tokens = capacity
	}
	if tokens < 1 {
		return false, &rateLimitState{tokens: tokens, lastRefill: nowSeconds}
	}
	return true, &rateLimitState{tokens: tokens - 1, lastRefill: nowSeconds}
}

// encodeRateState serializes a bucket for shared data.
func encodeRateState(s *rateLimitState) []byte {
	return []byte(formatInt(int64(s.tokens*1000)) + ":" + formatInt(s.lastRefill))
}

// decodeRateState parses a bucket, tolerating anything unreadable by
// returning nil (treated as "first request").
func decodeRateState(raw []byte) *rateLimitState {
	s := string(raw)
	idx := strings.IndexByte(s, ':')
	if idx <= 0 {
		return nil
	}
	milli, ok := parseFloat(s[:idx])
	if !ok {
		return nil
	}
	ts, ok := parseFloat(s[idx+1:])
	if !ok {
		return nil
	}
	return &rateLimitState{tokens: milli / 1000.0, lastRefill: int64(ts)}
}
