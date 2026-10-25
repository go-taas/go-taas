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

package metering

import (
	"context"
	"encoding/json"
	"time"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/model"
)

// RecordGatewayUsage turns the data-plane gateway's usage report into a
// metering event on the queue.
//
// The gateway is the only component that sees both the request and the
// response, so it is where usage originates (architecture Section 3.2.1,
// step 04). It does not write the ledger: publishing keeps inference off the
// database path, and the consumer's idempotency on request_id makes
// at-least-once delivery safe.
//
// The event it publishes is the exact structure `event_consumer.go`
// decodes, so the producer and the consumer cannot drift apart.
//
// Returning an error means "the event did not reach the queue". The caller
// must not fail the inference request for that — a billing gap is cheaper
// than an outage — but it must be visible, which is what the caller's
// dropped counter and warn log are for.
func (s *Service) RecordGatewayUsage(ctx context.Context, req *server.GatewayUsageRequest) (string, error) {
	if req == nil || req.RequestID == "" {
		return "", apierrors.Newf(apierrors.CodeMeteringEventInvalid,
			"metering: gateway usage event needs a request_id")
	}
	if req.Usage.Empty() && req.Status == "" {
		// Nothing was measured and nothing failed: there is no event worth
		// writing. Skipping keeps the ledger free of empty rows that would
		// only make reconciliation noisier.
		return "", nil
	}

	modelID, serviceID := s.resolveGatewayModel(ctx, req)

	event := meteringEvent{
		RequestID:      req.RequestID,
		OrganizationID: req.OrganizationID,
		APIKeyID:       req.APIKeyID,
		ModelID:        modelID,
		ServiceID:      serviceID,
		Usage: tokenUsage{
			PromptTokens:     req.Usage.PromptTokens,
			CompletionTokens: req.Usage.CompletionTokens,
			CachedTokens:     req.Usage.CachedTokens,
			ReasoningTokens:  req.Usage.ReasoningTokens,
		},
		CompletedAt:  req.CompletedAt,
		LatencyMs:    req.LatencyMs,
		Status:       req.Status,
		Error:        req.Error,
		TTFTMs:       req.TTFTMs,
		GenerationMs: req.GenerationMs,
	}
	if event.CompletedAt == 0 {
		event.CompletedAt = nowUnix()
	}

	publisher, err := s.publisherFor()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(&event)
	if err != nil {
		return "", apierrors.Wrap(apierrors.CodeInternal, err, "metering: encode gateway usage event")
	}
	if err := publisher.Publish(ctx, mq.DefaultSubjects().MeteringEvents, body,
		map[string]string{"content-type": "application/json", "x-source": "data-plane-gateway"}); err != nil {
		return "", apierrors.Wrap(apierrors.CodeInternal, err, "metering: publish gateway usage event")
	}
	logger.S().Debugw("metering: gateway usage event published",
		"request_id", event.RequestID, "org", event.OrganizationID, "model", modelID)
	return "", nil
}

// resolveGatewayModel maps the served model name the gateway echoes back to
// the catalog's model id and, when one is running, the inference service
// that serves it.
//
// The gateway only knows what the caller typed (`model` in the request
// body), so the translation belongs here rather than in the plugin: identity
// resolution is a control-plane concern and the plugin must stay dumb
// enough to be replaceable.
//
// An unknown name is not an error. The engines accept aliases and
// fine-tunes the catalog has never seen, and refusing to meter those would
// silently drop real usage. The ledger records the name the gateway saw, so
// the row is still attributable.
func (s *Service) resolveGatewayModel(ctx context.Context, req *server.GatewayUsageRequest) (string, string) {
	if req.Model == "" {
		return req.ServiceID, req.ServiceID
	}
	db, err := s.gormDB()
	if err != nil {
		return req.Model, req.ServiceID
	}
	row, err := model.NewRepository(db).FindByName(ctx, req.Model)
	if err != nil || row == nil {
		logger.S().Debugw("metering: gateway model name not in the catalog",
			"model", req.Model, "request_id", req.RequestID)
		return req.Model, req.ServiceID
	}
	serviceID := req.ServiceID
	if serviceID == "" {
		serviceID = s.serviceForModel(ctx, row.ID)
	}
	return row.ID, serviceID
}

// serviceForModel finds the inference service running a model, so the
// voucher can be attributed to it. Absent is fine: external providers and
// models served outside a managed service have no row.
func (s *Service) serviceForModel(ctx context.Context, modelID string) string {
	if s.serviceLookup == nil {
		return ""
	}
	return s.serviceLookup(ctx, modelID)
}

// publisherFor resolves the queue client, preferring the injection point
// tests and FVT use over the shared components.
func (s *Service) publisherFor() (mq.Client, error) {
	if s.publisher != nil {
		return s.publisher, nil
	}
	if s.components == nil || s.components.MQ() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal,
			"metering: mq component unavailable")
	}
	client, ok := s.components.MQ().Client().(mq.Client)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal,
			"metering: unexpected mq handle type %T", s.components.MQ().Client())
	}
	s.publisher = client
	return client, nil
}

// nowUnix is the completion time used when the gateway did not report one.
func nowUnix() int64 { return time.Now().Unix() }

// ensure the interface is satisfied at compile time.
var _ server.GatewayUsageSink = (*Service)(nil)

// SetServiceLookup injects the model-id -> inference-service resolver.
func (s *Service) SetServiceLookup(fn func(ctx context.Context, modelID string) string) {
	s.serviceLookup = fn
}
