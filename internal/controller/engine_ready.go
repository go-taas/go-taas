package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-taas/go-taas/pkg/logger"
)

// Engine lifecycle observation (feature: service state machine).
//
// A Kubernetes pod is "Running" long before the engine inside it can serve
// traffic: the container process starts, then vLLM/SGLang load the weights
// and build their runtime. The console therefore distinguishes:
//
//	queued    the controller accepted the change; nothing is scheduled yet
//	starting  the scheduler placed a pod on a node and the engine is loading
//	running   the engine itself answered its health endpoint
//	failed    scheduling, pulling, starting or health-checking failed
//
// The state is derived from the cluster, not from a timer, so it stays
// truthful when a deploy stalls.

// engineReadyTimeout bounds how long one deploy waits for the engine to
// answer its health endpoint. Loading a large model over a shared
// filesystem takes minutes; a deploy that never answers must not block the
// consumer forever.
const engineReadyTimeout = 20 * time.Minute

// engineProbeInterval is how often the engine's health endpoint is polled
// once a pod is running.
const engineProbeInterval = 3 * time.Second

// engineHealthPath is the readiness endpoint of the supported engines.
// vLLM serves /health; SGLang serves /health_generate (it only answers once
// the model is loaded and able to generate). Both also answer /health, so
// the probe tries them in order.
var engineHealthPaths = []string{"/health", "/health_generate"}

// engineHTTPClient probes the engine through the pod IP. The timeout is
// short: a slow answer means "not ready yet", not an error.
var engineHTTPClient = &http.Client{Timeout: 3 * time.Second}

// awaitEngineReady follows one deploy until the engine answers, reporting
// "starting" when the scheduler placed a pod and "running" when the engine
// responds. Scheduling and container failures surface as failed with the
// cluster's own reason.
func (r *k8sReconciler) awaitEngineReady(ctx context.Context, evt changeEvent) error {
	ctx, cancel := context.WithTimeout(ctx, engineReadyTimeout)
	defer cancel()

	port := enginePort
	if pinned := enginePortFrom(evt.EngineArgs); pinned > 0 {
		port = pinned
	}

	reportedStarting := false
	ticker := time.NewTicker(engineProbeInterval)
	defer ticker.Stop()
	for {
		pods, err := r.clientset.CoreV1().Pods(r.namespace).
			List(ctx, metav1.ListOptions{LabelSelector: serviceLabelKey + "=" + evt.ServiceID})
		if err != nil {
			if ctx.Err() != nil {
				return r.engineTimeoutError(ctx, evt)
			}
			return fmt.Errorf("controller: list pods for %s: %w", evt.Name, err)
		}
		if len(pods.Items) == 0 {
			// No pod yet: still queued behind the scheduler. A deployment
			// that never produces a pod is reported by the timeout below.
			if ctx.Err() != nil {
				return r.engineTimeoutError(ctx, evt)
			}
			select {
			case <-ctx.Done():
				return r.engineTimeoutError(ctx, evt)
			case <-ticker.C:
			}
			continue
		}

		pod := newestPod(pods.Items)
		// A container that will never start is a failure now, not in 20
		// minutes: the reason (ImagePullBackOff, CreateContainerError,
		// OOMKilled) is actionable. A storm has several pods (one per
		// device, one per PD role), so any pod that can never start fails
		// the whole deploy: the pod group cannot serve without it.
		if reason := terminalPodFailureIn(pods.Items); reason != "" {
			return errors.New(reason)
		}

		if pod.Spec.NodeName != "" && !reportedStarting {
			// The scheduler placed the pod: the engine is pulling weights
			// and starting up.
			if err := r.publishStatus(ctx, evt.ServiceID, "starting", nil, nil); err != nil {
				logger.S().Warnw("controller: publish starting status failed",
					"service_id", evt.ServiceID, "err", err)
			}
			reportedStarting = true
			logger.S().Infow("controller: inference service starting",
				"service_id", evt.ServiceID, "name", evt.Name, "node", pod.Spec.NodeName)
		}

		// "running" means an engine is actually serving. In a distributed or
		// PD storm only one pod of the group exposes the API (rank 0 / the
		// decode role), and which one it is is the engine's business, so the
		// first pod that answers is enough; polling the wrong rank forever
		// would report a healthy service as failed.
		if r.anyPodAnswers(ctx, pods.Items, port) {
			return nil
		}

		if ctx.Err() != nil {
			return r.engineTimeoutError(ctx, evt)
		}
		select {
		case <-ctx.Done():
			return r.engineTimeoutError(ctx, evt)
		case <-ticker.C:
		}
	}
}

// anyPodAnswers reports whether any live pod of the service answers the
// engine's health endpoint. A distributed or PD storm only exposes the API on
// one pod, and which one is the engine's business, so the first answer wins.
func (r *k8sReconciler) anyPodAnswers(ctx context.Context, pods []corev1.Pod, port int) bool {
	for i := range pods {
		pod := &pods[i]
		if pod.DeletionTimestamp != nil || pod.Status.PodIP == "" {
			continue
		}
		if r.probeEngine(ctx, pod.Status.PodIP, port) {
			return true
		}
	}
	return false
}

// probeEngine runs the injected prober, or the HTTP health probe.
func (r *k8sReconciler) probeEngine(ctx context.Context, host string, port int) bool {
	if r.engineProber != nil {
		return r.engineProber(ctx, host, port)
	}
	return r.engineAnswers(ctx, host, port)
}

// engineAnswers probes the engine's health endpoint on the pod IP.
func (r *k8sReconciler) engineAnswers(ctx context.Context, host string, port int) bool {
	for _, path := range engineHealthPaths {
		url := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + path
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := engineHTTPClient.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return true
		}
	}
	return false
}

// engineTimeoutError explains a deploy that never became ready, preferring
// the cluster's own reason when one is available.
func (r *k8sReconciler) engineTimeoutError(ctx context.Context, evt changeEvent) error {
	detail := r.deployBlockedReason(context.WithoutCancel(ctx), evt.Name)
	if detail == "" {
		detail = "the engine did not answer its health endpoint"
	}
	return fmt.Errorf("controller: service %s not ready within %s: %s",
		evt.Name, engineReadyTimeout, detail)
}

// newestPod returns the most recently created pod of the set.
func newestPod(pods []corev1.Pod) corev1.Pod {
	newest := pods[0]
	for _, pod := range pods[1:] {
		if pod.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = pod
		}
	}
	return newest
}

// terminalPodFailureIn returns the first terminal failure among the pods of a
// storm. A pod that is being deleted is skipped: a rolling update leaves
// terminating pods around, and their old state says nothing about the new
// desired spec.
func terminalPodFailureIn(pods []corev1.Pod) string {
	for i := range pods {
		if pods[i].DeletionTimestamp != nil {
			continue
		}
		if reason := terminalPodFailure(pods[i]); reason != "" {
			return reason
		}
	}
	return ""
}

// terminalPodFailure returns a human-readable reason when the pod can never
// become ready on its own: an image that cannot be pulled, a container
// that cannot be created, or one that keeps crashing.
func terminalPodFailure(pod corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName",
				"CreateContainerError", "CreateContainerConfigError", "RunContainerError":
				if w.Message != "" {
					return w.Reason + ": " + w.Message
				}
				return w.Reason
			}
		}
		if t := cs.State.Terminated; t != nil && strings.TrimSpace(t.Reason) == "OOMKilled" {
			return "OOMKilled: the engine ran out of memory while loading the model"
		}
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse &&
			cond.Reason == corev1.PodReasonUnschedulable && cond.Message != "" {
			// Unschedulable is terminal for this deploy: the requested
			// accelerator stays unavailable until someone changes the request.
			return "Unschedulable: " + cond.Message
		}
	}
	return ""
}
