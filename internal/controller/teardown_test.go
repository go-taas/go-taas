package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestTeardownSweepsLeakedModelRoute verifies the label sweep removes a
// model HTTPRoute whose name is derived from the served model, not the
// service. A stop/delete event may not carry the catalog model name, so the
// fast-path delete misses the route; the sweep is what guarantees it goes.
func TestTeardownSweepsLeakedModelRoute(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	leaked := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":      "qwen-router",
			"namespace": defaultAIBrixNamespace,
			"labels": map[string]any{
				mcLabelManagedBy: managedByValue,
				mcLabelServiceID: "svc-1",
			},
		},
	}}
	_, err := dyn.Resource(httpRouteGVR).Namespace(defaultAIBrixNamespace).
		Create(ctx, leaked, metav1.CreateOptions{})
	require.NoError(t, err)

	require.NoError(t, reconciler.ApplyInferServiceChange(ctx,
		changeMessage(testChangeEvent("delete", "demo"))))

	_, err = dyn.Resource(httpRouteGVR).Namespace(defaultAIBrixNamespace).
		Get(ctx, "qwen-router", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the leaked model route is swept by service id")
}

// TestTeardownLeavesReplacementServiceResources verifies a late delete event
// for a service whose name was reused does not tear down the replacement's
// objects: they carry a different service id.
func TestTeardownLeavesReplacementServiceResources(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, clientset, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	// The replacement service reuses the name and already owns its objects.
	for _, name := range []string{"demo-svc", "demo"} {
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: reconcileNamespace,
			Labels: map[string]string{
				mcLabelManagedBy: managedByValue,
				labelServiceID:   "svc-new",
			},
		}}
		_, err := clientset.CoreV1().Services(reconcileNamespace).Create(ctx, svc, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	storm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orchestration.aibrix.ai/v1alpha1",
		"kind":       "StormService",
		"metadata": map[string]any{
			"name":      "demo",
			"namespace": reconcileNamespace,
			"labels": map[string]any{
				mcLabelManagedBy: managedByValue,
				mcLabelServiceID: "svc-new",
			},
		},
	}}
	_, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).
		Create(ctx, storm, metav1.CreateOptions{})
	require.NoError(t, err)

	// A late delete event for the previous owner (svc-1).
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx,
		changeMessage(testChangeEvent("delete", "demo"))))

	for _, name := range []string{"demo-svc", "demo"} {
		_, err := clientset.CoreV1().Services(reconcileNamespace).Get(ctx, name, metav1.GetOptions{})
		assert.NoError(t, err, "the replacement service %q must survive the old owner's teardown", name)
	}
	_, err = dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).
		Get(ctx, "demo", metav1.GetOptions{})
	assert.NoError(t, err, "the replacement storm must survive the old owner's teardown")
}

// TestApplyUpsertSkipsDeletedService verifies a stale upsert delivered after
// the operator deleted the service does not recreate its resources.
func TestApplyUpsertSkipsDeletedService(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	reconciler.SetServiceStateProvider(func(context.Context, string) (string, bool, error) {
		return "terminated", true, nil
	})
	ctx := context.Background()

	require.NoError(t, reconciler.ApplyInferServiceChange(ctx,
		changeMessage(testChangeEvent("upsert", "demo"))))

	_, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).
		Get(ctx, "demo", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "a deleted service's stale change must not recreate the storm")
}

// TestApplyUpsertAbandonsWaitWhenStopped verifies the readiness wait notices
// a stop that is queued behind it and returns promptly instead of holding the
// consumer (and the teardown) for the full readiness timeout.
func TestApplyUpsertAbandonsWaitWhenStopped(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	calls := 0
	reconciler.SetServiceStateProvider(func(context.Context, string) (string, bool, error) {
		calls++
		if calls == 1 {
			// The reconcile starts normally.
			return "pending", true, nil
		}
		// The operator stopped the service while the engine was starting.
		return "stopped", true, nil
	})
	ctx := context.Background()

	require.NoError(t, reconciler.ApplyInferServiceChange(ctx,
		changeMessage(testChangeEvent("upsert", "demo"))))
	assert.GreaterOrEqual(t, calls, 2, "the wait must re-read the desired state")
}

// TestApplyUpsertReclaimsStaleName verifies a create that reuses the name of
// a terminated service replaces the previous owner's leftover objects instead
// of colliding with them.
func TestApplyUpsertReclaimsStaleName(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, clientset, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	staleSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-svc",
		Namespace: reconcileNamespace,
		Labels: map[string]string{
			mcLabelManagedBy: managedByValue,
			labelServiceID:   "svc-old",
		},
	}}
	_, err := clientset.CoreV1().Services(reconcileNamespace).Create(ctx, staleSvc, metav1.CreateOptions{})
	require.NoError(t, err)

	staleStorm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orchestration.aibrix.ai/v1alpha1",
		"kind":       "StormService",
		"metadata": map[string]any{
			"name":      "demo",
			"namespace": reconcileNamespace,
			"labels": map[string]any{
				mcLabelManagedBy: managedByValue,
				mcLabelServiceID: "svc-old",
			},
		},
	}}
	_, err = dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).
		Create(ctx, staleStorm, metav1.CreateOptions{})
	require.NoError(t, err)

	// The new service (svc-1) reuses the name.
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx,
		changeMessage(testChangeEvent("upsert", "demo"))))

	svc, err := clientset.CoreV1().Services(reconcileNamespace).Get(ctx, "demo-svc", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "svc-1", svc.Labels[labelServiceID], "the stale service is replaced by the new owner's")

	storm, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "svc-1", storm.GetLabels()[labelServiceID], "the stale storm is replaced by the new owner's")
}
