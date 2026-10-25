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

// The metering half of the plugin's contract.
//
// Usage comes from the engine's own `usage` object, never from a gateway
// tokenizer (architecture Section 3.4): a gateway that tokenizes on its own
// is affected by chat templates, tool-call framing and multimodal input, so
// its count drifts from what the engine actually computed. The gateway's job
// is to *find* the number and forward it.

import "strings"

// tokenUsage is the four-class breakdown the ledger prices separately.
type tokenUsage struct {
	prompt     int64
	completion int64
	cached     int64
	reasoning  int64
}

// empty reports whether nothing was measured.
func (u tokenUsage) empty() bool {
	return u.prompt == 0 && u.completion == 0 && u.cached == 0 && u.reasoning == 0
}

// usageReport is one request's completed measurement.
type usageReport struct {
	requestID      string
	organizationID string
	keyID          string
	model          string
	usage          tokenUsage
	status         string
	errMessage     string
	completedAt    int64
	latencyMs      int64
	ttftMs         int64
	generationMs   int64
}

// extractUsage pulls the engine's usage out of a response body.
//
// Two shapes matter:
//
//   - a non-streaming JSON response, where `usage` is a top-level member;
//   - a streaming (SSE) response, which is a sequence of `data:` lines and
//     is not JSON at all.
//
// For the streaming case the final chunk carries `usage` (vLLM emits it on
// the last chunk before `[DONE]`), so scanning for the last balanced object
// reaches it. Some engines send usage in a separate final event; both land in
// the same place.
func extractUsage(body string) (tokenUsage, bool) {
	root, ok := lastJSONObject(body)
	if !ok {
		return tokenUsage{}, false
	}
	node := root.get("usage")
	if node == nil || node.kind != jsonObject {
		return tokenUsage{}, false
	}
	u := tokenUsage{
		prompt:     node.intOr("prompt_tokens"),
		completion: node.intOr("completion_tokens"),
		cached:     node.intOr("cached_tokens"),
		reasoning:  node.intOr("reasoning_tokens"),
	}
	if node.get("cached_tokens") == nil {
		// OpenAI-compatible engines report cache hits under details.
		if details := node.get("prompt_tokens_details"); details != nil {
			u.cached = details.intOr("cached_tokens")
		}
	}
	if u.empty() {
		return tokenUsage{}, false
	}
	return u, true
}

// requestIdentity pulls the model name and the requested max_tokens out of a
// request body.
//
// The model names what the platform authorizes; max_tokens is what it may
// have to clamp. Both are read from the same parse so the request is walked
// once.
func requestIdentity(body string) (model string, maxTokens int64, ok bool) {
	root, ok := parseJSON(body)
	if !ok || root.kind != jsonObject {
		return "", 0, false
	}
	return root.stringOr("model"), root.intOr("max_tokens"), true
}

// clampMaxTokens rewrites max_tokens down to cap, returning the new body.
//
// The cap is the organization's per-request output limit. Clamping rather
// than rejecting keeps a caller whose only mistake was asking for too much
// served, which is what the contract in VerifyAPIKeyResponse promises
// ("the gateway validates or clamps"). When the body cannot be rewritten
// safely, the caller is told instead of being silently ignored.
func clampMaxTokens(body string, capTokens int64) (string, bool, bool) {
	model, maxTokens, ok := requestIdentity(body)
	_ = model
	if !ok || maxTokens <= 0 || capTokens <= 0 || maxTokens <= capTokens {
		return body, false, ok
	}
	rewritten, ok := replaceJSONInt(body, "max_tokens", capTokens)
	if !ok {
		return body, false, false
	}
	return rewritten, true, true
}

// replaceJSONInt replaces the numeric value of a top-level "key" member,
// preserving every other byte of the body.
//
// Re-serializing the whole body would need a full JSON writer and would
// reorder or reformat the caller's request; a targeted byte splice leaves
// everything the engine needs intact.
func replaceJSONInt(body, key string, value int64) (string, bool) {
	needle := `"` + key + `"`
	idx := strings.Index(body, needle)
	if idx < 0 {
		return body, false
	}
	i := idx + len(needle)
	for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
		i++
	}
	if i >= len(body) || body[i] != ':' {
		return body, false
	}
	i++
	for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
		i++
	}
	start := i
	for i < len(body) && (body[i] == '-' || (body[i] >= '0' && body[i] <= '9')) {
		i++
	}
	if start == i {
		return body, false
	}
	tail := body[i:]
	head := body[:start]
	return head + formatInt(value) + tail, true
}

// formatInt renders a non-negative int64 without strconv.
func formatInt(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// usageBody renders the /internal/gateway/usage payload.
func usageBody(rep *usageReport) []byte {
	var b strings.Builder
	b.WriteString(`{"request_id":"`)
	b.WriteString(jsonEscape(rep.requestID))
	b.WriteString(`","organization_id":"`)
	b.WriteString(jsonEscape(rep.organizationID))
	b.WriteString(`","api_key_id":"`)
	b.WriteString(jsonEscape(rep.keyID))
	b.WriteString(`","model":"`)
	b.WriteString(jsonEscape(rep.model))
	b.WriteString(`","usage":{"prompt_tokens":`)
	b.WriteString(formatInt(rep.usage.prompt))
	b.WriteString(`,"completion_tokens":`)
	b.WriteString(formatInt(rep.usage.completion))
	b.WriteString(`,"cached_tokens":`)
	b.WriteString(formatInt(rep.usage.cached))
	b.WriteString(`,"reasoning_tokens":`)
	b.WriteString(formatInt(rep.usage.reasoning))
	b.WriteString(`},"completed_at":`)
	b.WriteString(formatInt(rep.completedAt))
	b.WriteString(`,"latency_ms":`)
	b.WriteString(formatInt(rep.latencyMs))
	b.WriteString(`,"ttft_ms":`)
	b.WriteString(formatInt(rep.ttftMs))
	b.WriteString(`,"generation_ms":`)
	b.WriteString(formatInt(rep.generationMs))
	b.WriteString(`,"status":"`)
	b.WriteString(jsonEscape(rep.status))
	b.WriteString(`","error":"`)
	b.WriteString(jsonEscape(rep.errMessage))
	b.WriteString(`"}`)
	return []byte(b.String())
}

// ensureStreamUsage adds `stream_options.include_usage` to a streaming
// request that does not already ask for it.
//
// This is not cosmetic. An OpenAI-compatible engine omits `usage` from a
// streamed response unless the caller requests it, so without this the
// primary billing path — every `stream: true` request — would never be
// metered, and the ledger would silently undercount exactly the traffic that
// matters most. vLLM emits a final chunk carrying `usage` when the flag is
// present.
//
// A caller that already set the field keeps their value, including an explicit
// false: overriding it would be changing the caller's contract rather than
// completing it.
func ensureStreamUsage(body string) (string, bool) {
	root, ok := parseJSON(body)
	if !ok || root.kind != jsonObject {
		return body, false
	}
	if !root.boolOr("stream") {
		return body, false
	}
	if root.get("stream_options") != nil {
		return body, false
	}
	trimmed := strings.TrimRight(body, " \t\r\n")
	if len(trimmed) < 2 || trimmed[len(trimmed)-1] != '}' {
		return body, false
	}
	inner := strings.TrimRight(trimmed[:len(trimmed)-1], " \t\r\n")
	var b strings.Builder
	b.WriteString(inner)
	if !strings.HasSuffix(inner, "{") {
		b.WriteString(",")
	}
	b.WriteString(`"stream_options":{"include_usage":true}}`)
	return b.String(), true
}

// wantsStream reports whether the request asked for a streamed response.
func wantsStream(body string) bool {
	root, ok := parseJSON(body)
	if !ok || root.kind != jsonObject {
		return false
	}
	return root.boolOr("stream")
}
