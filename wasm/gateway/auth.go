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

// The authentication half of the plugin's contract.
//
// The plugin makes no authorization decision of its own (architecture
// Section 3.3): it parses the credential, hashes it, and asks the platform.
// Everything it does here is transport — read the header, keep a short-lived
// local cache, map a business code to an HTTP status.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	// The API key is `sk-` + 43 base62 characters (services/auth). Only its
	// SHA-256 ever leaves the gateway.
	apiKeyPrefix = "sk-"
	apiKeyLength = 46
)

var (
	errMalformedConfig = errors.New("go-taas gateway plugin: malformed plugin config")
	errNoCredential    = errors.New("go-taas gateway plugin: no bearer credential")
)

// bearerToken extracts the token from an Authorization header value.
//
// The scheme comparison is case-insensitive because HTTP specifies it that
// way, and a caller that sends "bearer" is not presenting a bad key.
func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	const scheme = "bearer "
	if len(header) <= len(scheme) {
		return ""
	}
	if !strings.EqualFold(header[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(header[len(scheme):])
}

// keyDigest returns the lowercase hex SHA-256 of the plaintext key — the
// contract shared with auth.KeyDigest. The plaintext is never sent, logged
// or stored.
func keyDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// looksLikeAPIKey reports whether key has the shape of a platform key.
//
// A key that does not is answered locally: calling the platform to be told
// "that is not even a key" wastes a round trip and turns a trivial bad
// request into load on the control plane.
func looksLikeAPIKey(key string) bool {
	if len(key) != apiKeyLength || !strings.HasPrefix(key, apiKeyPrefix) {
		return false
	}
	for i := len(apiKeyPrefix); i < len(key); i++ {
		c := key[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return true
}

// business codes the platform returns for a caller-attributable failure.
// They are hardcoded rather than imported so the plugin stays a leaf module
// with no dependency on the platform's packages.
const (
	codeAPIKeyNotFound         = 10007
	codeAPIKeyInvalid          = 10008
	codeAPIKeyRevoked          = 10009
	codeAPIKeyExpired          = 10010
	codeForbidden              = 10036
	codeRateLimitExceeded      = 10037
	codeAPIKeyModelNotAllowed  = 10039
	codeModelNotFound          = 10101
	codeModelUnauthorized      = 10105
	codeQuotaOutputTokensLimit = 13304
)

// rejection maps a business code to the HTTP status and OpenAI-compatible
// error fields the caller sees.
//
// The status is what an SDK's retry logic reads and the type/message are
// what its error handling shows, so both have to be right: a 500 where a 401
// belongs makes a client retry a request that never can succeed.
func rejection(code int, message string) (status int, errType, errCode string) {
	switch code {
	case codeAPIKeyNotFound, codeAPIKeyInvalid:
		return 401, "invalid_request_error", "invalid_api_key"
	case codeAPIKeyRevoked:
		return 401, "invalid_request_error", "api_key_revoked"
	case codeAPIKeyExpired:
		return 401, "invalid_request_error", "api_key_expired"
	case codeRateLimitExceeded:
		return 429, "rate_limit_error", "rate_limit_exceeded"
	case codeForbidden, codeAPIKeyModelNotAllowed, codeModelNotFound, codeModelUnauthorized:
		return 403, "permission_error", "model_not_allowed"
	case codeQuotaOutputTokensLimit:
		return 400, "invalid_request_error", "max_tokens_exceeded"
	default:
		// Anything unmapped is a platform-side condition the gateway should
		// have failed closed on; 503 tells the caller to retry later instead
		// of fixing their key.
		return 503, "server_error", "service_unavailable"
	}
}

// errorBody renders the OpenAI-compatible error envelope. Callers are OpenAI
// SDKs and agents, so matching that shape means their existing error
// handling works unchanged.
func errorBody(message, errType, errCode string) []byte {
	var b strings.Builder
	b.WriteString(`{"error":{"message":"`)
	b.WriteString(jsonEscape(message))
	b.WriteString(`","type":"`)
	b.WriteString(jsonEscape(errType))
	b.WriteString(`","code":"`)
	b.WriteString(jsonEscape(errCode))
	b.WriteString(`"}}`)
	return []byte(b.String())
}

// verdict is what the platform answered, decoded.
type verdict struct {
	allowed                   bool
	code                      int
	message                   string
	organizationID            string
	keyID                     string
	role                      string
	rateLimitRPM              int64
	rateLimitTPM              int64
	maxConcurrentRequests     int64
	maxOutputTokensPerRequest int64
	// fetchedAtSeconds is the coarse clock when the answer arrived, for the
	// local cache TTL.
	fetchedAtSeconds int64
}

// parseVerdict decodes the /internal/gateway/verify response.
func parseVerdict(body []byte) (*verdict, bool) {
	root, ok := parseJSON(string(body))
	if !ok || root.kind != jsonObject {
		return nil, false
	}
	v := &verdict{
		allowed:                   root.boolOr("allowed"),
		code:                      int(root.intOr("code")),
		message:                   root.stringOr("message"),
		organizationID:            root.stringOr("organization_id"),
		keyID:                     root.stringOr("key_id"),
		role:                      root.stringOr("role"),
		rateLimitRPM:              root.intOr("rate_limit_rpm"),
		rateLimitTPM:              root.intOr("rate_limit_tpm"),
		maxConcurrentRequests:     root.intOr("max_concurrent_requests"),
		maxOutputTokensPerRequest: root.intOr("max_output_tokens_per_request"),
	}
	return v, true
}

// cacheKey scopes a verdict entry to the digest and the model, because the
// model is part of what the platform authorized.
func cacheKey(digest, model string) string {
	if model == "" {
		return digest
	}
	return digest + "\x00" + model
}
