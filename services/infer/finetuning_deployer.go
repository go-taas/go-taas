package infer

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	"github.com/go-taas/go-taas/pkg/mq"
)

// FineTuningServiceDeployer deploys a fine-tuned model as an inference
// service for the finetuning module (feature #39, AD8). It is consumed
// by the finetuning service.
type FineTuningServiceDeployer struct {
	db        *database.Manager
	publisher mq.Client
}

// NewFineTuningServiceDeployer constructs a FineTuningServiceDeployer
// bound to a GORM database and an MQ publisher.
func NewFineTuningServiceDeployer(db *gorm.DB, publisher mq.Client) *FineTuningServiceDeployer {
	return &FineTuningServiceDeployer{db: database.NewManager(db), publisher: publisher}
}

// DeployModel creates an inference service for a model version and
// returns the service id. It mirrors the infer service's create path:
// it writes the service row and publishes the desired-state change
// event for the Controller to reconcile.
func (d *FineTuningServiceDeployer) DeployModel(ctx context.Context, name, modelID, modelVersion, imageID, accelerator string, replicas int32) (string, error) {
	repo := NewInferenceServiceRepository(d.db.DB(ctx))
	svc := &InferenceService{
		ID:              NewServiceID(),
		Name:            name,
		ModelID:         modelID,
		ModelVersion:    modelVersion,
		ImageID:         imageID,
		Accelerator:     accelerator,
		AcceleratorType: accelerator,
		Replicas:        int(replicas),
		State:           "pending",
	}
	if err := repo.Create(ctx, svc); err != nil {
		return "", err
	}
	// Publish the desired-state change event (the Controller reconciles
	// it into a Deployment).
	body, _ := json.Marshal(map[string]any{
		"event_type": "create", "service_id": svc.ID, "name": svc.Name,
		"model": map[string]any{"model_id": modelID, "version": modelVersion},
		"image": map[string]any{"image_id": imageID},
		"replicas": replicas, "accelerator": accelerator, "accelerator_type": accelerator,
	})
	if d.publisher != nil {
		_ = d.publisher.Publish(ctx, mq.DefaultSubjects().InferServiceChanges, body, nil)
	}
	return svc.ID, nil
}