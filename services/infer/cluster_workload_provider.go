package infer

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	"github.com/go-taas/go-taas/services/cluster"
)

// ClusterWorkloadProvider lists the inference services placed on a
// cluster for the cluster module (feature #40, AD7).
type ClusterWorkloadProvider struct {
	db *database.Manager
}

// NewClusterWorkloadProvider constructs a ClusterWorkloadProvider bound
// to a GORM database.
func NewClusterWorkloadProvider(db *gorm.DB) *ClusterWorkloadProvider {
	return &ClusterWorkloadProvider{db: database.NewManager(db)}
}

// ListServicesByCluster returns the services placed on a cluster.
func (p *ClusterWorkloadProvider) ListServicesByCluster(ctx context.Context, clusterID string) ([]cluster.ClusterWorkload, error) {
	var rows []struct {
		ID        string
		Name      string
		ModelID   string
		State     string
		CreatedAt time.Time
	}
	err := p.db.DB(ctx).Table("inference_services").
		Select("id, name, model_id, state, created_at").
		Where("cluster_id = ?", clusterID).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]cluster.ClusterWorkload, 0, len(rows))
	for _, r := range rows {
		out = append(out, cluster.ClusterWorkload{
			ServiceID: r.ID, Name: r.Name, ModelID: r.ModelID, State: r.State, CreatedAt: r.CreatedAt.Unix(),
		})
	}
	return out, nil
}