// Package controller consumes desired-state change messages from the
// message queue and reconciles them into Kubernetes resources:
// inference workloads, Services and image pre-pull jobs.
package controller

import (
	"context"
	"fmt"

	"github.com/go-taas/go-taas/pkg/k8s"
	"github.com/go-taas/go-taas/pkg/logger"
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
}

// Controller wires the message queue subscriptions to a Reconciler.
type Controller struct {
	client     mq.Client
	reconciler Reconciler
}

// New constructs a Controller consuming client and applying events with
// reconciler.
func New(client mq.Client, reconciler Reconciler) *Controller {
	return &Controller{client: client, reconciler: reconciler}
}

// Run subscribes to all consumed subjects and blocks until ctx is
// cancelled or a subscription fails. Subscriptions are started in the
// background because broker clients differ in whether Subscribe blocks;
// Run only owns the overall lifecycle.
func (c *Controller) Run(ctx context.Context) error {
	errCh := make(chan error, 2)

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

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return fmt.Errorf("controller: subscription failed: %w", err)
	}
}

// k8sReconciler is the default Reconciler backed by the Kubernetes
// clients.
type k8sReconciler struct {
	client *k8s.Client
}

// NewK8sReconciler builds the default Kubernetes-backed reconciler.
func NewK8sReconciler(client *k8s.Client) Reconciler {
	return &k8sReconciler{client: client}
}

// ApplyInferServiceChange implements Reconciler.
func (r *k8sReconciler) ApplyInferServiceChange(_ context.Context, msg mq.Message) error {
	logger.S().Infow("applying inference service change",
		"subject", msg.Subject, "body_bytes", len(msg.Body))
	// TODO: decode the desired state (workload spec, replicas, image,
	// model mount) from msg.Body and create/update/delete the
	// corresponding Deployment and Service through r.client.
	return nil
}

// ApplyImageWarmup implements Reconciler.
func (r *k8sReconciler) ApplyImageWarmup(_ context.Context, msg mq.Message) error {
	logger.S().Infow("applying image warmup",
		"subject", msg.Subject, "body_bytes", len(msg.Body))
	// TODO: decode the pre-pull task (image reference, node selector)
	// from msg.Body and create a DaemonSet job / pre-pull pod on the
	// matching nodes through r.client.
	return nil
}
