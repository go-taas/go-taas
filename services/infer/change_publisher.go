package infer

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/services/model"
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
	// EventTypeStop stops a running service: the controller deletes its
	// Kubernetes resources while keeping the catalog row.
	EventTypeStop = "stop"
	// EventTypeStart starts a stopped service: the controller recreates
	// its Kubernetes resources from the stored spec.
	EventTypeStart = "start"
	// EventTypeUpdateEngineArgs changes the serving-engine startup
	// parameters: the controller recreates the pods with the new args.
	EventTypeUpdateEngineArgs = "update_engine_args"
	// EventTypeReconcileRoute re-applies the model's serving route without
	// touching the workload. A route is named after the served model, not
	// the service, so two services of one model share it: a teardown has to
	// re-point it at a survivor, and a running service has to be able to
	// restore a route that was deleted out of band. Both cases publish this
	// event instead of an upsert, which would also rewrite the workload.
	EventTypeReconcileRoute = "reconcile_route"
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
	// Env are the environment variables set on every engine container of
	// the service.
	Env map[string]string `json:"env,omitempty"`
	// Rdma is the RDMA device request of the service's pods. Nil means the
	// service asked for no device.
	Rdma *changeEventRdma `json:"rdma,omitempty"`
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
	// GPUPerPod / CPUMilli / MemoryMiB are the per-pod resource requests of
	// every role. GPUPerPod 0 reads as 1; the CPU and memory requests are
	// omitted when zero, which leaves the pod's shape to the image.
	GPUPerPod int   `json:"gpu_per_pod,omitempty"`
	CPUMilli  int64 `json:"cpu_milli,omitempty"`
	MemoryMiB int64 `json:"memory_mib,omitempty"`
	// Priority is the scheduling priority ("high", "normal", "low"). It is
	// what Volcano preempts on, so it is carried on every event.
	Priority    string    `json:"priority,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

// changeEventRdmaFor projects a row's RDMA columns onto the event. A service
// without RDMA carries nil, so the controller's default is "no claim".
func changeEventRdmaFor(svc *InferenceService) *changeEventRdma {
	if !svc.RdmaEnabled || svc.RdmaDevices < 1 {
		return nil
	}
	return &changeEventRdma{
		Enabled:     true,
		Devices:     svc.RdmaDevices,
		DeviceClass: svc.RdmaDeviceClass,
	}
}

// changeEventRdma is the RDMA request inside a change event.
type changeEventRdma struct {
	Enabled     bool   `json:"enabled"`
	Devices     int    `json:"devices"`
	DeviceClass string `json:"device_class,omitempty"`
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
		Replicas:             svc.Replicas,
		Accelerator:          svc.Accelerator,
		AcceleratorType:      svc.AcceleratorType,
		PoolID:               svc.PoolID,
		SliceTemplateID:      svc.SliceTemplateID,
		SliceMemoryMiB:       svc.SliceMemoryGiB * 1024,
		SliceComputePct:      svc.SliceComputePct,
		EngineArgs:           EngineArgsMap(svc.EngineArgs),
		ExtraArgs:            ExtraArgsList(svc.ExtraArgs),
		Env:                  EnvMap(svc.Env),
		Rdma:                 changeEventRdmaFor(svc),
		RoleEngineArgs:       RoleEngineArgsList(svc.RoleEngineArgs),
		Topology:             storedTopologyName(svc.Topology),
		TensorParallelSize:   maxInt(svc.TensorParallelSize, 1),
		PipelineParallelSize: maxInt(svc.PipelineParallelSize, 1),
		PrefillReplicas:      svc.PrefillReplicas,
		DecodeReplicas:       svc.DecodeReplicas,
		GPUPerPod:            svc.GPUPerPod,
		CPUMilli:             svc.CPUMilli,
		MemoryMiB:            svc.MemoryMiB,
		Priority:             svc.Priority,
		PublishedAt:          time.Now().UTC(),
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

// routeFailoverLimit caps how many survivor services one teardown re-points
// the model route at. One is enough to keep the model routable; the cap only
// exists so a pathological fleet cannot turn one delete into a publish storm.
const routeFailoverLimit = 5

// publishRouteReconciliations re-publishes a route-reconciliation event for
// every running service of the given model except the excluded one.
//
// It exists because the serving route is named after the *model*, not the
// service: two services that serve the same model share one HTTPRoute, and
// the route can only point at one backend. When one of them is torn down, the
// route would otherwise be deleted with it and the survivor would stay
// unroutable until an operator touched it — the gateway answers 503
// (`httproutes ... not found`) for every request in the meantime.
//
// Publishing the survivor's reconciliation *before* the teardown event also
// makes the controller's ownership check do the right thing: the route no
// longer serves the stopping service, so the teardown leaves it alone instead
// of deleting it and relying on a later recreate.
//
// Failures are logged, never returned: a missing survivor route must not fail
// the stop/delete the operator asked for. The serving converger retries on its
// next tick.
func publishRouteReconciliations(
	ctx context.Context,
	client mq.Client,
	repo *InferenceServiceRepository,
	models *model.Repository,
	modelName, excludeServiceID string,
) {
	if client == nil || repo == nil || excludeServiceID == "" {
		return
	}
	row, err := repo.FindByID(ctx, excludeServiceID)
	if err != nil || row == nil || row.ModelID == "" {
		return
	}
	survivors, err := repo.ListRunningByModel(ctx, row.ModelID, excludeServiceID, routeFailoverLimit)
	if err != nil {
		logger.S().Warnw("infer: list model survivor services failed",
			"service_id", excludeServiceID, "err", err)
		return
	}
	for _, survivor := range survivors {
		name := modelName
		if name == "" {
			name = servedModelName(ctx, models, survivor)
		}
		evt := buildChangeEvent(EventTypeReconcileRoute, survivor, name, "", "", "")
		if err := publishChange(ctx, client, evt); err != nil {
			logger.S().Warnw("infer: publish route reconciliation failed",
				"service_id", survivor.ID, "err", err)
		}
	}
}
