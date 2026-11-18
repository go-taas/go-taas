package server

import (
	"context"
	"net/http"

	"github.com/go-taas/go-taas/pkg/logger"
)

// servingModelsPath answers which service serves which model. The data plane
// routes a model's public traffic by path ("/<namespace>/<service>/..."), the
// way the serving runtime writes its routes, so a caller that only knows the
// model name (the OpenAI "model" field) needs this map to build the path.
const servingModelsPath = "/internal/gateway/serving/models"

// ServingModelCatalog resolves a served model name to the service whose route
// answers it.
type ServingModelCatalog interface {
	// ServingModelRoutes maps each served model name to the service name its
	// route is named after.
	ServingModelRoutes(ctx context.Context) (map[string]string, error)
}

// initServingModels registers the served-model routing table. It shares the
// gateway API's guard and stays off with it: the table names models a caller
// can reach, so it must never be reachable unauthenticated.
func (s *commonServer) initServingModels(guard func(http.HandlerFunc) http.HandlerFunc) {
	var catalog ServingModelCatalog
	for _, svc := range s.services {
		if c, ok := svc.(ServingModelCatalog); ok && catalog == nil {
			catalog = c
		}
	}
	if catalog == nil {
		logger.S().Debugw("data plane: served-model table unavailable")
		return
	}
	s.monitorMux.HandleFunc(servingModelsPath, guard(s.servingModelsHandler(catalog)))
	logger.S().Infow("data plane: served-model endpoints registered")
}

// servingModelsHandler answers the served-model routing table.
func (s *commonServer) servingModelsHandler(catalog ServingModelCatalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeDataPlaneError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed",
				"method not allowed")
			return
		}
		routes, err := catalog.ServingModelRoutes(r.Context())
		if err != nil {
			logger.S().Warnw("data plane: served-model table read failed", "err", err)
			writeDataPlaneError(w, http.StatusServiceUnavailable, "server_error", "routing_unavailable",
				"model routing table unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": routes})
	}
}
