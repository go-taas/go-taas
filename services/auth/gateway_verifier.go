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

package auth

import (
	"context"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// VerifyGatewayKey answers the data-plane gateway plugin's verification
// call.
//
// It exists so the plugin has no business logic of its own (architecture
// Section 3.3): this method delegates to VerifyAPIKey, which owns the hash
// comparison, the revocation and expiry checks, the per-key model scope,
// the per-tenant model authorization and the two cache layers. There is
// deliberately no second implementation of any of those rules — a gateway
// that reimplemented them would drift from the control plane, and the drift
// would be a security bug rather than a cosmetic one.
//
// The translation is only about shape: a business error becomes a verdict
// with allowed=false, so the plugin can build an HTTP response without
// understanding gRPC statuses.
func (s *Service) VerifyGatewayKey(ctx context.Context, req *server.GatewayVerifyRequest) (*server.GatewayVerifyResponse, error) {
	if req == nil || req.KeyDigest == "" {
		// The plugin only calls this after it found a credential, so an
		// empty digest means the caller presented something unusable.
		// That is a rejection, not a platform fault: answering 401 is
		// both correct and cheaper than making the plugin fail closed.
		return rejectWith(apierrors.CodeAPIKeyInvalid), nil
	}

	verdict, err := s.VerifyAPIKey(ctx, &authv1.VerifyAPIKeyRequest{
		KeyDigest: req.KeyDigest,
		Model:     req.Model,
		SourceIp:  req.SourceIP,
	})
	if err != nil {
		// An infrastructure failure (Redis, database) is not a verdict:
		// the plugin must fail closed on it, which it can only tell apart
		// from a rejection if this returns an error.
		if code := apierrors.CodeOf(err); isVerdictCode(code) {
			return rejectWith(code), nil
		}
		return nil, err
	}
	return &server.GatewayVerifyResponse{
		Allowed:                   true,
		OrganizationID:            verdict.GetOrganizationId(),
		KeyID:                     verdict.GetKeyId(),
		Role:                      verdict.GetRole(),
		RateLimitRPM:              verdict.GetRateLimitRpm(),
		RateLimitTPM:              verdict.GetRateLimitTpm(),
		MaxConcurrentRequests:     verdict.GetMaxConcurrentRequests(),
		MaxOutputTokensPerRequest: verdict.GetMaxOutputTokensPerRequest(),
	}, nil
}

// rejectWith builds a "bad key" verdict carrying the business code and the
// canonical message.
func rejectWith(code apierrors.Code) *server.GatewayVerifyResponse {
	return &server.GatewayVerifyResponse{
		Allowed: false,
		Code:    int(code),
		Message: apierrors.Message(code),
	}
}

// isVerdictCode reports whether a business code describes the caller's key
// or request rather than the platform's health. Only these become
// allowed=false; every other failure must surface as an error so the
// gateway fails closed instead of telling the caller their key is bad when
// the platform simply could not check it.
func isVerdictCode(code apierrors.Code) bool {
	switch code {
	case apierrors.CodeAPIKeyNotFound,
		apierrors.CodeAPIKeyInvalid,
		apierrors.CodeAPIKeyRevoked,
		apierrors.CodeAPIKeyExpired,
		apierrors.CodeAPIKeyModelNotAllowed,
		apierrors.CodeModelNotFound,
		apierrors.CodeModelUnauthorized:
		return true
	default:
		// CodeForbidden (10036) is deliberately absent: on this path it
		// means the platform could not resolve the caller's organization
		// state, which is a platform fault the gateway must fail closed on,
		// not something the caller's key can fix.
		return false
	}
}

// ensure the interface is satisfied at compile time.
var _ server.GatewayVerifier = (*Service)(nil)
