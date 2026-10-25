package controller

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/go-taas/go-taas/pkg/k8s"
	"github.com/go-taas/go-taas/services/infer"
)

// logFetcher is the read-only Kubernetes-backed implementation of the
// infer module's LogFetcher seam (feature #33, AD2). It enumerates a
// service's pods/containers and streams their stdout/stderr from the
// Kubernetes API, exactly as kubectl logs does.
type logFetcher struct {
	clientset kubernetes.Interface
	namespace string
}

// NewLogFetcher builds the Kubernetes-backed LogFetcher for the infer
// module (feature #33, AD2).
func NewLogFetcher(client *k8s.Client, namespace string) infer.LogFetcher {
	if namespace == "" {
		namespace = "taas-infer"
	}
	return &logFetcher{clientset: client.Clientset(), namespace: namespace}
}

// serviceLabelKey is the label the controller stamps on inference pods
// (see resourceLabels in reconciler.go).
const serviceLabelKey = "go-taas.com/service-id"

// ListServiceLogPods lists the service's pods by the service label and
// returns each pod's containers and phase, masked as replica-N indices
// (feature #33, AD3).
func (f *logFetcher) ListServiceLogPods(ctx context.Context, serviceID string) ([]infer.LogPod, error) {
	pods, err := f.clientset.CoreV1().Pods(f.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: serviceLabelKey + "=" + serviceID,
	})
	if err != nil {
		return nil, fmt.Errorf("controller: list pods for service %s: %w", serviceID, err)
	}
	// Sort by creation time so replica indices are stable.
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp)
	})
	out := make([]infer.LogPod, 0, len(pods.Items))
	for i, pod := range pods.Items {
		replica := "replica-" + strconv.Itoa(i+1)
		for _, c := range pod.Spec.Containers {
			out = append(out, infer.LogPod{
				ReplicaIndex: replica,
				Container:    c.Name,
				State:        string(pod.Status.Phase),
			})
		}
	}
	return out, nil
}

// GetServiceLogs reads a container's stdout/stderr from the Kubernetes
// API, bounded by tail and since, and returns the lines plus a
// next_offset cursor (feature #33, AD4).
func (f *logFetcher) GetServiceLogs(ctx context.Context, serviceID, pod, container string, tail int, since string, _ string) (infer.LogWindow, error) {
	// Resolve the replica index to a pod name.
	pods, err := f.clientset.CoreV1().Pods(f.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: serviceLabelKey + "=" + serviceID,
	})
	if err != nil {
		return infer.LogWindow{}, fmt.Errorf("controller: list pods for service %s: %w", serviceID, err)
	}
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp)
	})
	// A caller that names no pod wants the logs of the service, not an
	// error: default to the first replica. Without this, the very first
	// read of the viewer (before it has picked a replica) answered an
	// internal error even though the pods were listed fine.
	if strings.TrimSpace(pod) == "" {
		pod = replicaLabel(1)
	}
	if len(pods.Items) == 0 {
		return infer.LogWindow{}, fmt.Errorf("controller: service %s has no pods", serviceID)
	}
	replicaIdx := replicaIndex(pod)
	if replicaIdx < 1 || replicaIdx > len(pods.Items) {
		return infer.LogWindow{}, fmt.Errorf("controller: unknown replica %q for service %s", pod, serviceID)
	}
	podName := pods.Items[replicaIdx-1].Name
	if container == "" {
		if len(pods.Items[replicaIdx-1].Spec.Containers) == 0 {
			return infer.LogWindow{}, fmt.Errorf("controller: pod %s has no containers", podName)
		}
		container = pods.Items[replicaIdx-1].Spec.Containers[0].Name
	}

	// Build the PodLogs options: tail lines and/or since time.
	opts := &corev1.PodLogOptions{Container: container, Timestamps: true}
	if tail > 0 {
		opts.TailLines = int64Ptr(int64(tail))
	}
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			opts.SinceTime = &metav1.Time{Time: t}
		}
	}
	req := f.clientset.CoreV1().Pods(f.namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return infer.LogWindow{}, fmt.Errorf("controller: read logs for pod %s container %s: %w", podName, container, err)
	}
	defer func() { _ = stream.Close() }()

	var lines []infer.LogLine
	lines, err = readLogLines(stream, tail)
	if err != nil {
		return infer.LogWindow{}, err
	}
	// The lines are returned newest-first (kubectl logs --tail order).
	// next_offset encodes the oldest line's timestamp for "load more".
	cursor := ""
	hasMore := false
	if len(lines) > 0 {
		oldest := lines[len(lines)-1].Timestamp
		cursor = fmt.Sprintf("%s|%s|%d", podName, container, oldest)
		hasMore = true
	}
	return infer.LogWindow{Lines: lines, NextOffset: cursor, HasMore: hasMore}, nil
}

// readLogLines reads the log stream and parses each line into a
// timestamp/level/message triple. Lines are returned newest-first.
func readLogLines(r io.Reader, tail int) ([]infer.LogLine, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var lines []infer.LogLine
	for scanner.Scan() {
		text := scanner.Text()
		ts, msg := splitTimestamp(text)
		lines = append(lines, infer.LogLine{
			Timestamp: ts,
			Level:     detectLevel(msg),
			Message:   msg,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("controller: read log stream: %w", err)
	}
	// Reverse to newest-first.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[:tail]
	}
	return lines, nil
}

// splitTimestamp splits a "2026-01-01T00:00:00.000000000Z message" line
// into its unix timestamp and the message text. When the line has no
// leading timestamp, the timestamp is 0 and the whole line is the
// message.
func splitTimestamp(line string) (int64, string) {
	idx := strings.Index(line, " ")
	if idx <= 0 {
		return 0, line
	}
	tsPart := line[:idx]
	msg := line[idx+1:]
	if t, err := time.Parse(time.RFC3339Nano, tsPart); err == nil {
		return t.Unix(), msg
	}
	return 0, line
}

// detectLevel best-effort detects a log level from a message (feature
// #33, AD6). Returns "" when none is detected.
func detectLevel(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "[error]") || strings.Contains(lower, "error:"):
		return "error"
	case strings.Contains(lower, "[warn") || strings.Contains(lower, "warning:"):
		return "warn"
	case strings.Contains(lower, "[debug]"):
		return "debug"
	case strings.Contains(lower, "[info]"):
		return "info"
	}
	return ""
}

// replicaIndex parses a "replica-N" index into N (1-based). Returns 0
// for a malformed index.
func replicaIndex(pod string) int {
	if !strings.HasPrefix(pod, "replica-") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(pod, "replica-"))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func int64Ptr(v int64) *int64 { return &v }

// ensure logFetcher implements infer.LogFetcher at compile time.
var _ infer.LogFetcher = (*logFetcher)(nil)
