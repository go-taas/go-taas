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

// A coarse clock, driven by Envoy's tick callback.
//
// The plugin needs time for three things: a verdict cache TTL, a token bucket
// refill, and an approximate request latency. The proxy-wasm ABI available
// here exposes no clock hostcall, and reaching for the WASI clock would make
// the filter depend on a shim the host may not provide — a filter that cannot
// instantiate is an outage, not a degraded feature.
//
// Envoy's tick is part of the ABI the plugin already relies on, so the clock
// is a counter this plugin bumps itself. The resolution is the tick period
// (one second), which is coarse but sufficient for every use above, and it is
// the same on every host. Anything finer would be invented precision.

import (
	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm"
)

const (
	// tickPeriodMs is the clock resolution. A one-second tick keeps the
	// shared-data write rate negligible even with thousands of requests.
	tickPeriodMs = 1000
	// clockKey holds the counter in shared data. Shared data is per-VM, so
	// each worker keeps its own clock — consistent with the per-worker cache
	// and token bucket it serves.
	clockKey = "go-taas:clock"
	// clockStart is where a fresh VM's clock begins. It is a monotonic
	// counter, not a wall clock: nothing here needs absolute time, and the
	// platform stamps `completed_at` itself.
	clockStart = 1
)

// clockSeconds reads the coarse clock.
//
// A read failure returns the start value rather than an error: a request must
// not be rejected because a counter was unreadable, and starting the clock at
// zero only shortens a cache TTL.
func clockSeconds() int64 {
	raw, _, err := proxywasm.GetSharedData(clockKey)
	if err != nil || len(raw) == 0 {
		return clockStart
	}
	v, ok := parseFloat(string(raw))
	if !ok {
		return clockStart
	}
	return int64(v)
}

// bumpClock advances the coarse clock by one tick.
func bumpClock() {
	if err := setSharedData(clockKey, []byte(formatInt(clockSeconds()+1))); err != nil {
		proxywasm.LogWarnf("go-taas gateway plugin: advance clock: %v", err)
	}
}

// setSharedData writes a key using the compare-and-swap token the host
// handed out on read.
//
// The token is not optional: a write with a stale (or zero) token against an
// existing key can be rejected, and a silently rejected write is a cache or
// token bucket that never updates. Reading first is also what Envoy's own
// documentation recommends for read-modify-write.
func setSharedData(key string, value []byte) error {
	_, cas, err := proxywasm.GetSharedData(key)
	if err != nil {
		// Not found is the normal first write; a create uses cas 0.
		cas = 0
	}
	return proxywasm.SetSharedData(key, value, cas)
}
