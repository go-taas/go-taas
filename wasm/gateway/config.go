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

// config is the JSON the EnvoyExtensionPolicy passes to the filter.
//
// Every field has a safe zero value: a policy that omits `config` entirely
// still produces a plugin that rejects unverified traffic rather than one
// that lets it through. That direction is deliberate — a misconfiguration
// must not open the inference surface.
type config struct {
	// Cluster is the Envoy upstream cluster the platform's internal API is
	// reachable on.
	//
	// Envoy requires a configured cluster, not a URL: a Wasm HTTP call is an
	// upstream request, so the filter cannot dial an arbitrary host.
	Cluster string `json:"cluster"`
	// Host is the :authority to send, and VerifyPath/UsagePath the paths.
	Host       string `json:"host"`
	VerifyPath string `json:"verify_path"`
	UsagePath  string `json:"usage_path"`
	// Token is the shared secret presented as X-Gateway-Token.
	Token string `json:"token"`
	// CacheTTLMs bounds how long a verdict is reused locally. A
	// revocation becomes effective within this window.
	CacheTTLMs int `json:"cache_ttl_ms"`
	// VerifyTimeoutMs bounds one verification call; exceeding it fails
	// closed.
	VerifyTimeoutMs int `json:"verify_timeout_ms"`
	// ProtectedPrefixes are the paths that require a verified key.
	ProtectedPrefixes []string `json:"protected_prefixes"`
	// MaxBodyBytes is how much of a request body the plugin will buffer to
	// find the model name and max_tokens. Larger bodies skip inspection and
	// are still authenticated.
	MaxBodyBytes int `json:"max_body_bytes"`
	// RateLimitStateTTLMs bounds how long an idle token bucket is kept.
	RateLimitStateTTLMs int `json:"rate_limit_state_ttl_ms"`
}

// defaultProtectedPrefixes are the paths that require a verified key when the
// configuration does not say otherwise.
var defaultProtectedPrefixes = []string{"/v1/"}

const (
	defaultVerifyPath          = "/internal/gateway/verify"
	defaultUsagePath           = "/internal/gateway/usage"
	defaultCacheTTLMs          = 30_000
	defaultVerifyTimeoutMs     = 500
	defaultMaxBodyBytes        = 1 << 20
	defaultRateLimitStateTTLMs = 600_000
)

// parseConfig decodes the policy's config, falling back to the defaults for
// anything absent. A malformed config is reported so the operator sees it in
// the Envoy log rather than silently getting default behaviour.
func parseConfig(raw []byte) (*config, error) {
	cfg := &config{
		CacheTTLMs:          defaultCacheTTLMs,
		VerifyTimeoutMs:     defaultVerifyTimeoutMs,
		MaxBodyBytes:        defaultMaxBodyBytes,
		RateLimitStateTTLMs: defaultRateLimitStateTTLMs,
		ProtectedPrefixes:   defaultProtectedPrefixes,
		VerifyPath:          defaultVerifyPath,
		UsagePath:           defaultUsagePath,
	}
	if len(raw) == 0 {
		return cfg, nil
	}
	root, ok := parseJSON(string(raw))
	if !ok || root.kind != jsonObject {
		return cfg, errMalformedConfig
	}
	if v := root.stringOr("cluster"); v != "" {
		cfg.Cluster = v
	}
	if v := root.stringOr("host"); v != "" {
		cfg.Host = v
	}
	if v := root.stringOr("verify_path"); v != "" {
		cfg.VerifyPath = v
	}
	if v := root.stringOr("usage_path"); v != "" {
		cfg.UsagePath = v
	}
	if v := root.stringOr("token"); v != "" {
		cfg.Token = v
	}
	if v := root.intOr("cache_ttl_ms"); v > 0 {
		cfg.CacheTTLMs = int(v)
	}
	if v := root.intOr("verify_timeout_ms"); v > 0 {
		cfg.VerifyTimeoutMs = int(v)
	}
	if v := root.intOr("max_body_bytes"); v > 0 {
		cfg.MaxBodyBytes = int(v)
	}
	if v := root.intOr("rate_limit_state_ttl_ms"); v > 0 {
		cfg.RateLimitStateTTLMs = int(v)
	}
	if prefixes := root.get("protected_prefixes"); prefixes != nil && prefixes.kind == jsonArray {
		cfg.ProtectedPrefixes = nil
		for _, item := range prefixes.array {
			if item.kind == jsonString && item.str != "" {
				cfg.ProtectedPrefixes = append(cfg.ProtectedPrefixes, item.str)
			}
		}
	}
	return cfg, nil
}

// valid reports whether the plugin can do its job. Without a cluster and a
// token there is nothing to call, so the filter refuses to serve traffic
// rather than admitting it unverified.
func (c *config) valid() bool {
	return c != nil && c.Cluster != "" && c.Host != "" && c.Token != ""
}

// protects reports whether path requires a verified key.
func (c *config) protects(path string) bool {
	if c == nil {
		return false
	}
	for _, prefix := range c.ProtectedPrefixes {
		if prefix == "/" {
			return true
		}
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
