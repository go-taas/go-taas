package cluster

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// ClusterRepository persists registered clusters.
type ClusterRepository struct { //nolint:revive // cluster.ClusterRepository is the documented domain name
	db *database.Manager
}

// NewClusterRepository constructs a ClusterRepository bound to a GORM
// database.
func NewClusterRepository(db *gorm.DB) *ClusterRepository {
	return &ClusterRepository{db: database.NewManager(db)}
}

// CreateCluster inserts a new cluster row. A unique violation on the
// name maps to CodeClusterExists.
func (r *ClusterRepository) CreateCluster(ctx context.Context, c *Cluster) error {
	err := r.db.DB(ctx).Create(c).Error
	if isUniqueViolation(err) {
		return apierrors.New(apierrors.CodeClusterExists)
	}
	return err
}

// FindClusterByID returns one cluster by id. A miss maps to
// CodeClusterNotFound.
func (r *ClusterRepository) FindClusterByID(ctx context.Context, clusterID string) (*Cluster, error) {
	if _, err := uuid.Parse(clusterID); err != nil {
		return nil, apierrors.New(apierrors.CodeClusterNotFound)
	}
	var row Cluster
	err := r.db.DB(ctx).Where("id = ?", clusterID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeClusterNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListClusters returns the clusters, newest first.
func (r *ClusterRepository) ListClusters(ctx context.Context) ([]Cluster, error) {
	var rows []Cluster
	err := r.db.DB(ctx).Order("created_at DESC").Find(&rows).Error
	return rows, err
}

// UpdateClusterState sets a cluster's state.
func (r *ClusterRepository) UpdateClusterState(ctx context.Context, clusterID, state string) error {
	return r.db.DB(ctx).Model(&Cluster{}).Where("id = ?", clusterID).Update("state", state).Error
}

// isUniqueViolation reports whether a GORM error is a unique-constraint
// violation (portable across SQLite and Postgres).
func isUniqueViolation(err error) bool {
	return err != nil && (errors.Is(err, gorm.ErrDuplicatedKey) || containsUnique(err))
}

func containsUnique(err error) bool {
	return err != nil && (contains(err, "UNIQUE constraint failed") || contains(err, "duplicate key"))
}

func contains(err error, substr string) bool {
	return err != nil && len(err.Error()) >= len(substr) && indexOf(err.Error(), substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}