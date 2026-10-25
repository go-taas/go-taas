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
// (one second), which is coarse but sufficient for every use above and is the
// same on every host. Anything finer would be invented precision.
//
// The counter lives in the plugin context rather than in shared data: a
// plugin context belongs to one VM, which is exactly one Envoy worker thread,
// so a plain field is already per-worker and needs no hostcall — and no
// compare-and-swap, which ten ticking workers would otherwise fight over.

import "github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm"

const (
	// tickPeriodMs is the clock resolution. A one-second tick keeps the work
	// negligible even with thousands of requests per second.
	tickPeriodMs = 1000
	// clockStart is where a fresh worker's clock begins. It is a monotonic
	// counter, not a wall clock: nothing here needs absolute time, and the
	// platform stamps `completed_at` itself.
	clockStart = 1
)

// nowSeconds reads the coarse clock.
func (p *pluginContext) nowSeconds() int64 {
	if p == nil || p.clock < clockStart {
		return clockStart
	}
	return p.clock
}

// bumpClock advances the coarse clock by one tick.
func (p *pluginContext) bumpClock() {
	if p == nil {
		return
	}
	p.clock++
}

// setSharedData writes a key using the compare-and-swap token the host hands
// out on read.
//
// The token is not optional: a write with a stale (or zero) token against an
// existing key is rejected, and a silently rejected write is a cache or token
// bucket that never updates. Reading first is also Envoy's own recommendation
// for read-modify-write.
func setSharedData(key string, value []byte) error {
	_, cas, err := proxywasm.GetSharedData(key)
	if err != nil {
		// Not found is the normal first write; a create uses cas 0.
		cas = 0
	}
	return proxywasm.SetSharedData(key, value, cas)
}
