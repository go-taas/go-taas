package infer

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
)

// Change-event types carried on infer.services.changes.
const (
	EventTypeUpsert = "upsert"
	EventTypeDelete = "delete"
)

// changeEventModel is the resolved model spec inside a change event. The
// controller never needs a database round-trip.
type changeEventModel struct {
	ModelID    string `json:"model_id"`
	Version    string `json:"version"`
	WeightPath string `json:"weight_path"`
}

// changeEventImage is the resolved image spec inside a change event.
type changeEventImage struct {
	ImageID   string `json:"image_id"`
	Reference string `json:"reference"`
	Engine    string `json:"engine"`
}

// changeEvent is the desired-state change published on
// infer.services.changes (architecture Section 4.5.1).
type changeEvent struct {
	EventType       string           `json:"event_type"`
	ServiceID       string           `json:"service_id"`
	OrganizationID  string           `json:"organization_id"`
	Name            string           `json:"name"`
	Model           changeEventModel `json:"model"`
	Image           changeEventImage `json:"image"`
	Replicas        int              `json:"replicas"`
	Accelerator     string           `json:"accelerator"`
	AcceleratorType string           `json:"accelerator_type"`
	// Autoscaling is the effective autoscaling policy (feature #16,
	// §6.1). Nil when autoscaling is not configured (the controller
	// treats it as disabled).
	Autoscaling *AutoscalingPolicy `json:"autoscaling,omitempty"`
	PublishedAt time.Time          `json:"published_at"`
}

// buildChangeEvent composes the change event for a service row with its
// resolved model version and image summary.
func buildChangeEvent(eventType string, svc *InferenceService, weightPath, imageReference, engine string) changeEvent {
	return changeEvent{
		EventType:      eventType,
		ServiceID:      svc.ID,
		OrganizationID: svc.OrganizationID,
		Name:           svc.Name,
		Model: changeEventModel{
			ModelID:    svc.ModelID,
			Version:    svc.ModelVersion,
			WeightPath: weightPath,
		},
		Image: changeEventImage{
			ImageID:   svc.ImageID,
			Reference: imageReference,
			Engine:    engine,
		},
		Replicas:        svc.Replicas,
		Accelerator:     svc.Accelerator,
		AcceleratorType: svc.AcceleratorType,
		PublishedAt:     time.Now().UTC(),
	}
}

// publishChange publishes the event on the changes subject. Headers
// carry event_type and service_id for broker-side routing/debugging.
func publishChange(ctx context.Context, client mq.Client, evt changeEvent) error {
	body, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	headers := map[string]string{
		"event_type": evt.EventType,
		"service_id": evt.ServiceID,
	}
	return client.Publish(ctx, mq.DefaultSubjects().InferServiceChanges, body, headers)
}

// publishChangeWithCompensation publishes after the desired state is
// durably committed. When the publish fails, the compensating delete
// removes the just-written row so "published ⇒ durable" holds; the
// original publish error is returned (the caller maps it to CodeInternal).
func publishChangeWithCompensation(
	ctx context.Context,
	client mq.Client,
	repo *InferenceServiceRepository,
	evt changeEvent,
	createdServiceID string,
) error {
	if err := publishChange(ctx, client, evt); err != nil {
		// Compensating delete: best effort, failures are logged.
		if delErr := repo.MarkTerminated(ctx, evt.OrganizationID, createdServiceID); delErr != nil {
			logger.S().Errorw("infer: compensating delete after publish failure failed",
				"service_id", createdServiceID, "err", delErr)
		}
		return err
	}
	return nil
}
