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

package server

// The data plane's provider-backed models.
//
// A self-hosted model is reachable because an inference service publishes a
// route for it; an external model has no workload behind it, so the serving
// router cannot pick a backend for it and the caller sees "model does not
// exist" even though the platform sells the model.
//
// The data-plane model router closes that gap: it asks these endpoints which
// models are provider-backed and forwards a matching request here, where it
// becomes a provider call. Both endpoints live on the monitor mux behind the
// gateway plugin's shared secret, because they spend the platform's provider
// credentials: only the router may reach them, and only for a request the
// gateway plugin already authenticated and will meter.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

const (
	// externalModelsPath lists the provider-backed model names.
	externalModelsPath = "/internal/gateway/external/models"
	// externalChatPath serves one chat completion from a provider.
	externalChatPath = "/internal/gateway/external/chat/completions"

	// dataPlaneMaxBodyBytes bounds a forwarded chat request. It matches the
	// gateway plugin's own body limit, so a request the plugin accepted is
	// never rejected here.
	dataPlaneMaxBodyBytes = 1 << 20
)

// ExternalModelProxy serves one chat completion from an external catalog
// model's provider. The model is addressed by the name the caller sent.
type ExternalModelProxy interface {
	ProxyExternalChatForGateway(ctx context.Context, modelName string, body []byte) ([]byte, error)
}

// ExternalModelCatalog reports which catalog models a provider serves.
type ExternalModelCatalog interface {
	ListExternalModelNames(ctx context.Context) ([]string, error)
}

// initExternalModels registers the provider-backed model endpoints. It is
// called from initGatewayAPI so they share its token guard and stay off when
// the plugin API is off: without the plugin nothing authenticates the caller,
// and these endpoints must never be reachable unauthenticated.
func (s *commonServer) initExternalModels(guard func(http.HandlerFunc) http.HandlerFunc) {
	var proxy ExternalModelProxy
	var catalog ExternalModelCatalog
	for _, svc := range s.services {
		if p, ok := svc.(ExternalModelProxy); ok && proxy == nil {
			proxy = p
		}
		if c, ok := svc.(ExternalModelCatalog); ok && catalog == nil {
			catalog = c
		}
	}
	if proxy == nil || catalog == nil {
		logger.S().Debugw("data plane: provider-backed models unavailable",
			"proxy", proxy != nil, "catalog", catalog != nil)
		return
	}
	s.monitorMux.HandleFunc(externalModelsPath, guard(s.externalModelsHandler(catalog)))
	s.monitorMux.HandleFunc(externalChatPath, guard(s.externalChatHandler(proxy)))
	logger.S().Infow("data plane: provider-backed model endpoints registered")
}

// externalModelsHandler answers the set of provider-backed model names.
func (s *commonServer) externalModelsHandler(catalog ExternalModelCatalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeDataPlaneError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed",
				"method not allowed")
			return
		}
		names, err := catalog.ListExternalModelNames(r.Context())
		if err != nil {
			logger.S().Warnw("data plane: list external models failed", "err", err)
			writeDataPlaneError(w, http.StatusServiceUnavailable, "server_error", "catalog_unavailable",
				"model catalog unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": names})
	}
}

// externalChatHandler forwards one chat completion to an external model.
func (s *commonServer) externalChatHandler(proxy ExternalModelProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeDataPlaneError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed",
				"method not allowed")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, dataPlaneMaxBodyBytes))
		if err != nil {
			writeDataPlaneError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body",
				"could not read request body")
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeDataPlaneError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body",
				"request body is not a JSON object")
			return
		}
		if req.Model == "" {
			writeDataPlaneError(w, http.StatusBadRequest, "invalid_request_error", "model_required",
				"model is required")
			return
		}

		respBody, err := proxy.ProxyExternalChatForGateway(r.Context(), req.Model, body)
		if err != nil {
			status, errType, code, message := dataPlaneError(err, req.Model)
			writeDataPlaneError(w, status, errType, code, message)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(respBody); err != nil {
			logger.S().Debugw("data plane: write external model response failed", "err", err)
		}
	}
}

// dataPlaneError maps a platform error onto the OpenAI-compatible error
// envelope an SDK already understands. A model the platform does not sell
// externally is "does not exist" from the caller's point of view; a provider
// failure is the platform's problem, so it stays a 5xx.
func dataPlaneError(err error, model string) (status int, errType, code, message string) {
	switch apierrors.CodeOf(err) {
	case apierrors.CodeProviderNotPublished, apierrors.CodeModelNotFound:
		return http.StatusNotFound, "invalid_request_error", "model_not_found",
			"model " + model + " does not exist"
	case apierrors.CodeProviderDisabled, apierrors.CodeProviderNotFound:
		return http.StatusServiceUnavailable, "server_error", "model_unavailable",
			"model " + model + " is temporarily unavailable"
	case apierrors.CodeProviderUpstreamUnauthorized:
		return http.StatusBadGateway, "server_error", "upstream_unauthorized",
			"the model provider rejected the platform credentials"
	default:
		return http.StatusServiceUnavailable, "server_error", "model_unavailable",
			"model " + model + " is temporarily unavailable"
	}
}

// writeDataPlaneError renders the OpenAI error envelope.
func writeDataPlaneError(w http.ResponseWriter, status int, errType, code, message string) {
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    code,
			"param":   "model",
		},
	})
	if err != nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
