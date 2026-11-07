package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-taas/go-taas/pkg/mq"
)

// TestPodFailureDetectorReportsTerminalFailure verifies the detector
// publishes a failed status for a service whose pod has a terminal
// failure, without deleting any resources.
func TestPodFailureDetectorReportsTerminalFailure(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, clientset := newFakeReconciler(publisher)
	_ = reconciler

	detector := NewPodFailureDetector(clientset, reconcileNamespace, time.Millisecond, publisher)

	// A pod with ImagePullBackOff for service "demo".
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: reconcileNamespace,
			Labels:    map[string]string{labelServiceID: "svc-demo"},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "engine",
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "pull failed"},
				},
			}},
		},
	}
	_, err := clientset.CoreV1().Pods(reconcileNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)

	detector.scanOnce(context.Background())

	require.Len(t, publisher.reports, 1)
	report := publisher.reports[0]
	assert.Equal(t, "svc-demo", report.ServiceID)
	assert.Equal(t, "failed", report.State)
	require.NotNil(t, report.FailureReason)
	assert.Contains(t, *report.FailureReason, "ImagePullBackOff")

	// The pod is still there: the detector never deletes resources.
	_, err = clientset.CoreV1().Pods(reconcileNamespace).Get(context.Background(), "demo-0", metav1.GetOptions{})
	require.NoError(t, err)
}

// TestPodFailureDetectorSkipsHealthyPods verifies no status is published
// when all pods are healthy.
func TestPodFailureDetectorSkipsHealthyPods(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	_, clientset := newFakeReconciler(publisher)

	detector := NewPodFailureDetector(clientset, reconcileNamespace, time.Millisecond, publisher)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: reconcileNamespace,
			Labels:    map[string]string{labelServiceID: "svc-demo"},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "engine",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
	_, err := clientset.CoreV1().Pods(reconcileNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)

	detector.scanOnce(context.Background())
	assert.Empty(t, publisher.reports)
}

// TestApplyStopDeletesResources verifies the controller's stop handling
// deletes the StormService and autoscaler while keeping the catalog row
// (the row is the infer module's concern; the controller only tears down
// k8s resources).
func TestApplyStopDeletesResources(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	evt := testChangeEventWithAutoscaling("upsert", "demo", &autoscalingPolicy{
		Enabled: true, MinReplicas: 1, MaxReplicas: 10,
		TargetConcurrency: 32, ScaleToZero: false, CooldownSeconds: 300,
	})
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))
	_, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)

	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(testChangeEvent("stop", "demo"))))

	_, err = dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the storm is deleted on stop")
	_, err = dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).Get(ctx, autoscalerName("demo"), metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the autoscaler is deleted on stop")
}

// TestApplyStartRecreatesResources verifies the controller's start
// handling recreates the StormService from the stored spec.
func TestApplyStartRecreatesResources(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	evt := testChangeEvent("upsert", "demo")
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(testChangeEvent("stop", "demo"))))

	_, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))

	// Start recreates the storm.
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(testChangeEvent("start", "demo"))))
	_, err = dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)
}

// TestApplyUpdateEngineArgsRecreatesStorm verifies the controller's
// update_engine_args handling recreates the StormService with the new
// engine arguments (a hot update).
func TestApplyUpdateEngineArgsRecreatesStorm(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	evt := testChangeEvent("upsert", "demo")
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))
	_, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)

	// The engine-args event reuses applyUpsert, so the storm is recreated
	// with the new args.
	evt2 := testChangeEvent("update_engine_args", "demo")
	evt2.EngineArgs = map[string]string{"max-model-len": "8192"}
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt2)))

	storm, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotNil(t, storm)
}