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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVerdictCachePreservesOwnership verifies the cached verdict keeps
// the key's owner and project across a cache round-trip.
func TestVerdictCachePreservesOwnership(t *testing.T) {
	in := &verdict{
		allowed:                   true,
		organizationID:            "org-a",
		keyID:                     "key-1",
		role:                      "agent",
		ownerUserID:               "user-1",
		projectID:                 "proj-1",
		rateLimitRPM:              10,
		rateLimitTPM:              100,
		maxConcurrentRequests:     5,
		maxOutputTokensPerRequest: 2048,
		fetchedAtSeconds:          1234,
	}
	out, ok := decodeVerdict(encodeVerdict(in))
	require.True(t, ok)
	assert.Equal(t, "org-a", out.organizationID)
	assert.Equal(t, "key-1", out.keyID)
	assert.Equal(t, "user-1", out.ownerUserID)
	assert.Equal(t, "proj-1", out.projectID)
	assert.EqualValues(t, 2048, out.maxOutputTokensPerRequest)
	assert.EqualValues(t, 5, out.maxConcurrentRequests)
}

// TestVerdictCacheRejectsOldShape verifies a verdict encoded before the
// ownership fields existed is a cache miss rather than a misread.
func TestVerdictCacheRejectsOldShape(t *testing.T) {
	_, ok := decodeVerdict([]byte("true|0|0|0|0|0|0|org|key"))
	assert.False(t, ok)
}

// TestUsageBodyCarriesOwnership verifies the usage event names the owner
// and project so metering needs no control-plane lookup.
func TestUsageBodyCarriesOwnership(t *testing.T) {
	rep := &usageReport{
		requestID:      "req-1",
		organizationID: "org-a",
		keyID:          "key-1",
		ownerUserID:    "user-1",
		projectID:      "proj-1",
		model:          "m1",
		usage:          tokenUsage{prompt: 3, completion: 4},
		status:         "success",
	}
	body := string(usageBody(rep))
	assert.Contains(t, body, `"owner_user_id":"user-1"`)
	assert.Contains(t, body, `"project_id":"proj-1"`)
	assert.Contains(t, body, `"api_key_id":"key-1"`)
	assert.True(t, strings.HasPrefix(body, `{"request_id":"req-1"`))
}
