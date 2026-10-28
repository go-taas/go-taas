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
	// EventTypeUpdateVersion is the in-place version-change event
	// (feature #32, AD6). The controller recreates the pods with the new
	// weights while keeping the service identity and endpoints.
	EventTypeUpdateVersion = "update_version"
	// EventTypeRollback is the deployment rollback event (feature #34,
	// AD5). The controller applies the previous desired state while
	// keeping the service identity and endpoints.
	EventTypeRollback = "rollback"
)

// changeEventModel is the resolved model spec inside a change event. The
// controller never needs a database round-trip.
type changeEventModel struct {
	ModelID string `json:"model_id"`
	// Name is the catalog model name. Clients send it as the `model`
	// field of an inference request, so the controller advertises it to
	// AIBrix and passes it to the engine as its served model name.
	Name       string `json:"name,omitempty"`
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
	// PoolID is the vGPU slicing pool the service opted into (feature
	// #52, AD13). Empty = whole card.
	PoolID string `json:"pool_id,omitempty"`
	// SliceTemplateID is the slice template the service requested
	// (feature #52, AD13). Empty = whole card.
	SliceTemplateID string `json:"slice_template_id,omitempty"`
	// SliceMemoryMiB is the resolved slice memory in MiB (feature #52,
	// AD13). Set only for shared-GPU services.
	SliceMemoryMiB int64 `json:"slice_memory_mib,omitempty"`
	// SliceComputePct is the resolved slice compute percentage (feature
	// #52, AD13). Set only for shared-GPU services.
	SliceComputePct int64 `json:"slice_compute_pct,omitempty"`
	// EngineArgs are the serving-engine parameters, keyed by flag name
	// without the leading dashes (feature: engine parameters). A value of
	// "flag:true" is a valueless switch.
	EngineArgs map[string]string `json:"engine_args,omitempty"`
	// ExtraArgs are free-form engine arguments appended after EngineArgs.
	ExtraArgs []string `json:"extra_args,omitempty"`
	// RoleEngineArgs overrides EngineArgs per role, so a PD service can tune
	// prefill and decode independently.
	RoleEngineArgs []RoleEngineArgs `json:"role_engine_args,omitempty"`
	// Topology is the serving layout: single, distributed or pd. The
	// controller turns it into the AIBrix StormService roles.
	Topology string `json:"topology,omitempty"`
	// TensorParallelSize / PipelineParallelSize shape one replica's pod
	// group (1 for a single-node deploy).
	TensorParallelSize   int `json:"tensor_parallel_size,omitempty"`
	PipelineParallelSize int `json:"pipeline_parallel_size,omitempty"`
	// PrefillReplicas / DecodeReplicas are the PD role counts (0 for the
	// other topologies).
	PrefillReplicas int `json:"prefill_replicas,omitempty"`
	DecodeReplicas  int `json:"decode_replicas,omitempty"`
	PublishedAt     time.Time `json:"published_at"`
}

// buildChangeEvent composes the change event for a service row with its
// resolved model name, model version and image summary. modelName is the
// catalog name clients pass as `model`; an empty value falls back to the
// service name so the engine always has a servable name.
func buildChangeEvent(eventType string, svc *InferenceService, modelName, weightPath, imageReference, engine string) changeEvent {
	if modelName == "" {
		modelName = svc.Name
	}
	return changeEvent{
		EventType:      eventType,
		ServiceID:      svc.ID,
		OrganizationID: svc.OrganizationID,
		Name:           svc.Name,
		Model: changeEventModel{
			ModelID:    svc.ModelID,
			Name:       modelName,
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
		PoolID:          svc.PoolID,
		SliceTemplateID: svc.SliceTemplateID,
		SliceMemoryMiB:  svc.SliceMemoryGiB * 1024,
		SliceComputePct: svc.SliceComputePct,
		EngineArgs:      EngineArgsMap(svc.EngineArgs),
		ExtraArgs:       ExtraArgsList(svc.ExtraArgs),
		RoleEngineArgs:  RoleEngineArgsList(svc.RoleEngineArgs),
		Topology:        storedTopologyName(svc.Topology),
		TensorParallelSize:   maxInt(svc.TensorParallelSize, 1),
		PipelineParallelSize: maxInt(svc.PipelineParallelSize, 1),
		PrefillReplicas:     svc.PrefillReplicas,
		DecodeReplicas:      svc.DecodeReplicas,
		PublishedAt:     time.Now().UTC(),
	}
}

// maxInt returns the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
