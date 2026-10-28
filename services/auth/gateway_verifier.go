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
	"strings"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
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
		code := apierrors.CodeOf(err)
		if isVerdictCode(code) {
			return rejectWith(code), nil
		}
		// A platform-side failure becomes a 503 at the gateway, which fails
		// closed — correct, but unattributable without this line. Log it with
		// the code and the model, never with the digest or the key.
		logger.S().Warnw("auth: gateway key verification failed",
			"code", code, "model", req.Model, "err", err)
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

// VerifyPlaygroundKey verifies the plaintext API key a playground call carries
// and checks that it belongs to the caller's organization.
//
// The playground forwards to the in-cluster engine service, which has no
// gateway plugin in front of it, so this is the only places the key's hash,
// revocation, expiry, model scope and tenant authorization are enforced for
// that path. It reuses VerifyAPIKey — there is deliberately no second
// implementation of those rules.
func (s *Service) VerifyPlaygroundKey(ctx context.Context, plaintext, model, orgID string) error {
	key := strings.TrimSpace(plaintext)
	if key == "" {
		return apierrors.New(apierrors.CodeAPIKeyInvalid)
	}
	verdict, err := s.VerifyAPIKey(ctx, &authv1.VerifyAPIKeyRequest{
		KeyDigest: KeyDigest(key),
		Model:     strings.TrimSpace(model),
	})
	if err != nil {
		return err
	}
	// A key from another organization is not a key the caller may spend: the
	// playground tests the caller's own traffic.
	if orgID != "" && verdict.GetOrganizationId() != "" && verdict.GetOrganizationId() != orgID {
		return apierrors.Newf(apierrors.CodeAPIKeyInvalid,
			"auth: the API key belongs to another organization")
	}
	return nil
}
