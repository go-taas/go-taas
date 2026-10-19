package infer

import (
	"context"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

// GetServiceStatus returns the service's status including any active
// incident (feature #53, AD10). The org is resolved from the session,
// never from a client-supplied service id — only the caller's own
// service's incident is returned.
func (s *Service) GetServiceStatus(ctx context.Context, req *inferv1.GetServiceStatusRequest) (*inferv1.GetServiceStatusResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId())
	if err != nil {
		return nil, err
	}
	resp := &inferv1.GetServiceStatusResponse{
		Response: okResponse(),
		State:    row.State,
	}
	if s.incidentReader != nil {
		incident, ok, err := s.incidentReader.ActiveIncidentForService(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			resp.Degraded = true
			resp.Incident = &inferv1.ServiceIncident{
				IncidentId: incident.IncidentID,
				Severity:   incident.Severity,
				Status:     incident.Status,
				RootCause:  incident.RootCause,
				Message:    incident.Message,
			}
		}
	}
	return resp, nil
}