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

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A cached verdict skips re-verification, including the funds gate, so the
// default reuse window has to stay as short as the platform's own funds
// cache: a longer default would keep serving a drained balance.
func TestParseConfigDefaultVerdictCacheIsShort(t *testing.T) {
	cfg, err := parseConfig(nil)
	require.NoError(t, err)
	assert.Equal(t, 5_000, cfg.CacheTTLMs)
	assert.Equal(t, 5_000, defaultCacheTTLMs)

	// An explicit value still wins.
	custom, err := parseConfig([]byte(`{"cache_ttl_ms": 1500}`))
	require.NoError(t, err)
	assert.Equal(t, 1_500, custom.CacheTTLMs)
}
