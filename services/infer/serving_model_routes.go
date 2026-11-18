package infer

import (
	"context"
	"strings"
)

// ServingModelRoutes maps each served model name to the service whose route
// answers it. A model's route is named after the service, so the data plane
// resolves a model's path by looking the service up here; the served name is
// the catalog model name the engine was started with, or the service name when
// a service predates the catalog link.
//
// Two services of one model serve the same name. The most recently updated one
// wins, which is the one a new request should reach; a caller that needs a
// specific one addresses it by path ("/<service>/v1/...").
func (s *Service) ServingModelRoutes(ctx context.Context) (map[string]string, error) {
	if s.repo == nil {
		return nil, nil
	}
	running, err := s.repo.ListRunning(ctx, 0)
	if err != nil {
		return nil, err
	}
	routes := make(map[string]string, len(running))
	modelNames := make(map[string]string, len(running))
	ids := make([]string, 0, len(running))
	for _, svc := range running {
		id := strings.TrimSpace(svc.ModelID)
		if id == "" {
			continue
		}
		if _, seen := modelNames[id]; !seen {
			modelNames[id] = ""
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		var rows []struct {
			ID   string
			Name string
		}
		if err := s.repo.DB(ctx).Table("models").Select("id, name").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			modelNames[row.ID] = row.Name
		}
	}
	for _, svc := range running {
		served := strings.TrimSpace(modelNames[svc.ModelID])
		if served == "" {
			served = svc.Name
		}
		if _, taken := routes[served]; !taken {
			routes[served] = svc.Name
		}
	}
	return routes, nil
}
