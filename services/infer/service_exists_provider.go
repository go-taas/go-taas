package infer

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
)

// ServiceExistsProvider reports whether an inference service id exists
// (feature #37, AD9). It is consumed by the resourcemetrics module to
// validate the service_id against the infer service contract (10301).
type ServiceExistsProvider struct {
	db *database.Manager
}

// NewServiceExistsProvider constructs a ServiceExistsProvider bound to a
// GORM database.
func NewServiceExistsProvider(db *gorm.DB) *ServiceExistsProvider {
	return &ServiceExistsProvider{db: database.NewManager(db)}
}

// ServiceExists reports whether an inference service id exists. A
// malformed (non-UUID) id is treated as not-found because it can never
// match a stored row (the infer service id is a UUID).
func (p *ServiceExistsProvider) ServiceExists(ctx context.Context, serviceID string) (bool, error) {
	if _, err := uuid.Parse(serviceID); err != nil {
		return false, nil
	}
	var count int64
	err := p.db.DB(ctx).Table("inference_services").Where("id = ?", serviceID).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}