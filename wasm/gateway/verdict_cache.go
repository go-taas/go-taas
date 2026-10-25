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

// Verdict serialization for the local cache.
//
// The platform's answer is what gets cached, not a boolean: the rate limit,
// the token cap and the model scope have to survive a cache hit, or a cached
// request would bypass them.
//
// The encoding is a fixed field order with a `|` separator, which is compact
// and cannot be confused with the values it stores (none of them contain a
// pipe: they are identifiers, numbers and a role name).

import "strings"

const verdictFieldCount = 9

// encodeVerdict serializes a verdict for shared data.
func encodeVerdict(v *verdict) []byte {
	if v == nil {
		return nil
	}
	parts := []string{
		boolField(v.allowed),
		formatInt(int64(v.code)),
		formatInt(v.rateLimitRPM),
		formatInt(v.rateLimitTPM),
		formatInt(v.maxConcurrentRequests),
		formatInt(v.maxOutputTokensPerRequest),
		formatInt(v.fetchedAtSeconds),
		sanitizeField(v.organizationID),
		sanitizeField(v.keyID),
	}
	return []byte(strings.Join(parts, "|"))
}

// decodeVerdict parses what encodeVerdict wrote. Anything unreadable is a
// cache miss, never a partial verdict: a half-decoded verdict could admit a
// request with no rate limit.
func decodeVerdict(raw []byte) (*verdict, bool) {
	parts := strings.Split(string(raw), "|")
	if len(parts) != verdictFieldCount {
		return nil, false
	}
	nums := make([]int64, verdictFieldCount)
	for i, part := range parts {
		if i == 0 || i == 7 || i == 8 {
			continue
		}
		n, ok := parseFloat(part)
		if !ok {
			return nil, false
		}
		nums[i] = int64(n)
	}
	allowed := parts[0] == "true" || parts[0] == "1"
	if parts[0] != "true" && parts[0] != "1" && parts[0] != "false" && parts[0] != "0" {
		return nil, false
	}
	return &verdict{
		allowed:                   allowed,
		code:                      int(nums[1]),
		rateLimitRPM:              nums[2],
		rateLimitTPM:              nums[3],
		maxConcurrentRequests:     nums[4],
		maxOutputTokensPerRequest: nums[5],
		fetchedAtSeconds:          nums[6],
		organizationID:            parts[7],
		keyID:                     parts[8],
	}, true
}

// boolField renders a bool as the field encoding expects it.
func boolField(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// sanitizeField removes the separator so a value cannot shift the fields
// around it.
func sanitizeField(s string) string {
	return strings.ReplaceAll(s, "|", "")
}
