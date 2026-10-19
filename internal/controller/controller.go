// Package controller consumes desired-state change messages from the
// message queue and reconciles them into Kubernetes resources:
// inference workloads, Services and image pre-pull jobs.
package controller

import (
	"context"
	"fmt"

	"github.com/go-taas/go-taas/pkg/mq"
)

// subjects holds the canonical subject names consumed by the controller.
var subjects = mq.DefaultSubjects()

// Reconciler applies desired-state changes to the cluster. It is the
// seam where message handling meets Kubernetes: keeping it an interface
// lets the consume loop be tested against a fake.
type Reconciler interface {
	// ApplyInferServiceChange applies one inference-service desired-state
	// change event.
	ApplyInferServiceChange(ctx context.Context, msg mq.Message) error

	// ApplyImageWarmup applies one image pre-pull task.
	ApplyImageWarmup(ctx context.Context, msg mq.Message) error

	// SetVGPUConfig installs the vGPU slicing-pool HAMI contract
	// (feature #52, AD13). It must be called before serving.
	SetVGPUConfig(policyAnnotation, placementPolicy string, poolSelector map[string]string)
}

// Controller wires the message queue subscriptions to a Reconciler.
type Controller struct {
	client     mq.Client
	reconciler Reconciler
	// inventory is the optional accelerator inventory collector
	// (feature #18). Nil when not configured.
	inventory *InventoryCollector
	// resourceSampler is the optional per-service resource sampling loop
	// (feature #37). Nil when not configured.
	resourceSampler *ResourceSampler
	// runtimeCollector is the optional distributed-inference-runtime
	// component + ModelClaim collector (feature #47). Nil when not
	// configured.
	runtimeCollector *RuntimeCollector
	// runtimeScheme is the optional serving-scheme consumer (feature #47).
	runtimeScheme *RuntimeSchemeConsumer
	// runtimeModelChange is the optional model-change consumer
	// (feature #47).
	runtimeModelChange *RuntimeModelChangeConsumer
}

// New constructs a Controller consuming client and applying events with
// reconciler.
func New(client mq.Client, reconciler Reconciler) *Controller {
	return &Controller{client: client, reconciler: reconciler}
}

// SetInventoryCollector installs the accelerator inventory collector
// (feature #18). It must be called before Run.
func (c *Controller) SetInventoryCollector(collector *InventoryCollector) {
	c.inventory = collector
}

// SetResourceSampler installs the per-service resource sampling loop
// (feature #37). It must be called before Run.
func (c *Controller) SetResourceSampler(sampler *ResourceSampler) {
	c.resourceSampler = sampler
}

// SetRuntimeCollector installs the distributed-inference-runtime
// collector (feature #47). It must be called before Run.
func (c *Controller) SetRuntimeCollector(collector *RuntimeCollector) {
	c.runtimeCollector = collector
}

// SetRuntimeSchemeConsumer installs the serving-scheme consumer
// (feature #47). It must be called before Run.
func (c *Controller) SetRuntimeSchemeConsumer(consumer *RuntimeSchemeConsumer) {
	c.runtimeScheme = consumer
}

// SetRuntimeModelChangeConsumer installs the runtime model-change
// consumer (feature #47). It must be called before Run.
func (c *Controller) SetRuntimeModelChangeConsumer(consumer *RuntimeModelChangeConsumer) {
	c.runtimeModelChange = consumer
}

// Run subscribes to all consumed subjects and blocks until ctx is
// cancelled or a subscription fails. Subscriptions are started in the
// background because broker clients differ in whether Subscribe blocks;
// Run only owns the overall lifecycle.
func (c *Controller) Run(ctx context.Context) error {
	errCh := make(chan error, 4)

	go func() {
		if err := c.client.Subscribe(ctx, subjects.InferServiceChanges, func(msg mq.Message) error {
			return c.reconciler.ApplyInferServiceChange(ctx, msg)
		}); err != nil {
			errCh <- err
		}
	}()
	go func() {
		if err := c.client.Subscribe(ctx, subjects.ImageWarmups, func(msg mq.Message) error {
			return c.reconciler.ApplyImageWarmup(ctx, msg)
		}); err != nil {
			errCh <- err
		}
	}()
	if c.inventory != nil {
		go c.inventory.Run(ctx)
	}
	if c.resourceSampler != nil {
		go c.resourceSampler.Run(ctx)
	}
	// Feature #47: the runtime scheme/model-change consumers and the
	// component collector.
	if c.runtimeScheme != nil {
		go func() {
			if err := c.runtimeScheme.Run(ctx); err != nil {
				errCh <- err
			}
		}()
	}
	if c.runtimeModelChange != nil {
		go func() {
			if err := c.runtimeModelChange.Run(ctx); err != nil {
				errCh <- err
			}
		}()
	}
	if c.runtimeCollector != nil {
		go c.runtimeCollector.Run(ctx)
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return fmt.Errorf("controller: subscription failed: %w", err)
	}
}
