package controller

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
)

// Accelerator vendor labels (AD3). The vendor set is closed.
const (
	labelAcceleratorVendor = "go-taas.com/accelerator"
	// fallbackAcceleratorLabel is the vendor label convention some
	// clusters already carry (GPU operator / platform teams set
	// gpu-vendor=nvidia). The controller prefers the documented
	// go-taas.com/accelerator label and falls back to this one when no
	// node carries it, so inference pods are schedulable on either.
	fallbackAcceleratorLabel = "gpu-vendor"
	labelDriverVersion       = "go-taas.com/driver-version"
	labelDevicePlugin        = "go-taas.com/device-plugin"

	// driverVersionLabelKeys are the labels a node may carry its GPU driver
	// version on, most specific first.
	//
	// `go-taas.com/driver-version` is the platform's own convention and wins
	// when a cluster sets it. NVIDIA's GPU Feature Discovery publishes the
	// version under `nvidia.com/cuda.driver-version.full` (595.91.07) plus
	// major/minor/revision parts, and no cluster sets the platform's label —
	// so reading only that left the version empty on every real install,
	// which the health rule then reported as "degraded" on a perfectly
	// healthy node.
)

// driverVersionLabelKeys are the node labels that may carry the GPU driver
// version, most specific first.
var driverVersionLabelKeys = []string{
	labelDriverVersion,
	"nvidia.com/cuda.driver-version.full",
	"nvidia.com/gpu.driver-version",
}

// inventoryNode is one node's raw signals in the published snapshot
// (feature #18, Section 4.1). The accelerator service computes the
// composite health from these raw signals (AD2).
type inventoryNode struct {
	NodeID            string              `json:"node_id"`
	Name              string              `json:"name"`
	Vendor            string              `json:"vendor"`
	CardTypes         []string            `json:"card_types"`
	GPUsAllocated     int64               `json:"gpus_allocated"`
	GPUsFree          int64               `json:"gpus_free"`
	DriverVersion     string              `json:"driver_version"`
	DevicePluginState string              `json:"device_plugin_state"`
	Readiness         string              `json:"readiness"`
	GPUs              []inventoryGPU      `json:"gpus"`
	Labels            map[string]string   `json:"labels"`
	Taints            []string            `json:"taints"`
	Resources         []inventoryResource `json:"resources"`
}

// inventoryGPU is one GPU's raw signal.
type inventoryGPU struct {
	Index     int64  `json:"index"`
	Model     string `json:"model"`
	Allocated bool   `json:"allocated"`
	Free      bool   `json:"free"`
	Note      string `json:"note"`
}

// inventoryResource is one card type's allocatable vs allocated.
type inventoryResource struct {
	CardType    string `json:"card_type"`
	Allocatable int64  `json:"allocatable"`
	Allocated   int64  `json:"allocated"`
}

// inventorySnapshot is the full fleet snapshot published on
// accelerator.inventory (AD1).
type inventorySnapshot struct {
	Nodes      []inventoryNode `json:"nodes"`
	ReportedAt time.Time       `json:"reported_at"`
}

// InventoryCollector lists compute nodes on an interval and publishes a
// full accelerator snapshot to the MQ (feature #18, Section 4.1). It is
// best-effort telemetry, not a reconcile: transient Kubernetes API
// errors are logged and the loop retries on the next tick.
type InventoryCollector struct {
	clientset kubernetes.Interface
	publisher mq.Client
	interval  time.Duration
}

// NewInventoryCollector builds an InventoryCollector over a clientset
// and a publish-capable MQ client.
func NewInventoryCollector(clientset kubernetes.Interface, publisher mq.Client, interval time.Duration) *InventoryCollector {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &InventoryCollector{clientset: clientset, publisher: publisher, interval: interval}
}

// Run collects and publishes the full snapshot on the interval until ctx
// is cancelled.
func (c *InventoryCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Publish once immediately so the cache is populated without
	// waiting a full interval.
	c.collectAndPublish(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collectAndPublish(ctx)
		}
	}
}

// collectAndPublish lists nodes, computes the raw signals and publishes
// the full snapshot. Errors are logged; the loop retries on the next
// tick.
func (c *InventoryCollector) collectAndPublish(ctx context.Context) {
	nodes, err := c.listNodes(ctx)
	if err != nil {
		logger.S().Warnw("controller: accelerator inventory collection failed",
			"err", err)
		return
	}
	snap := inventorySnapshot{Nodes: nodes, ReportedAt: time.Now().UTC()}
	body, err := json.Marshal(snap)
	if err != nil {
		logger.S().Warnw("controller: marshal inventory snapshot failed", "err", err)
		return
	}
	if err := c.publisher.Publish(ctx, mq.DefaultSubjects().AcceleratorInventory, body, nil); err != nil {
		logger.S().Warnw("controller: publish inventory snapshot failed", "err", err)
		return
	}
	logger.S().Infow("controller: accelerator inventory snapshot published",
		"nodes", len(nodes))
}

// listNodes lists all nodes and computes the raw signals per node.
func (c *InventoryCollector) listNodes(ctx context.Context) ([]inventoryNode, error) {
	list, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	// Allocation comes from the pods on each node. One cluster-wide list is
	// cheaper than a query per node, and the pod count on a GPU fleet is
	// small.
	allocated, err := c.allocatedByNode(ctx)
	if err != nil {
		// Telemetry only: a failed pod list must not drop the whole
		// snapshot, and reporting 0 allocated is visibly wrong rather than
		// silently plausible.
		logger.S().Warnw("controller: list pods for accelerator allocation failed", "err", err)
		allocated = nil
	}
	nodes := make([]inventoryNode, 0, len(list.Items))
	for i := range list.Items {
		n := &list.Items[i]
		nodes = append(nodes, nodeToInventory(n, allocated[n.Name]))
	}
	return nodes, nil
}

// allocatedByNode sums each node's accelerator requests across the pods it
// hosts, keyed by node name and then by resource name.
//
// Finished pods are skipped: a Succeeded/Failed pod has released its devices,
// so counting it would report capacity as used forever.
func (c *InventoryCollector) allocatedByNode(ctx context.Context) (map[string]map[corev1.ResourceName]int64, error) {
	pods, err := c.clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[corev1.ResourceName]int64, len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		nodeName := p.Spec.NodeName
		if nodeName == "" {
			continue // unscheduled: nothing is reserved on a node yet
		}
		switch p.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			continue
		}
		for _, ctr := range p.Spec.Containers {
			for name, qty := range requestedAccelerators(ctr.Resources) {
				if out[nodeName] == nil {
					out[nodeName] = map[corev1.ResourceName]int64{}
				}
				out[nodeName][name] += qty
			}
		}
	}
	return out, nil
}

// requestedAccelerators returns a container's accelerator requests, treating
// an extended resource set only in limits as a request (the Kubernetes
// convention: an extended resource with a limit and no request is requested
// at the limit).
func requestedAccelerators(res corev1.ResourceRequirements) map[corev1.ResourceName]int64 {
	out := map[corev1.ResourceName]int64{}
	for name, qty := range res.Limits {
		if _, ok := cardTypeFromResource(name); ok {
			out[name] = qty.Value()
		}
	}
	for name, qty := range res.Requests {
		if _, ok := cardTypeFromResource(name); ok {
			out[name] = qty.Value()
		}
	}
	return out
}

// nodeToInventory computes the raw signals for one node (Section 4.1).
func nodeToInventory(n *corev1.Node, allocated map[corev1.ResourceName]int64) inventoryNode {
	labels := n.Labels
	vendor := vendorFromNode(n)
	readiness := readinessFromNode(n)
	driverVersion := driverVersionFromNode(labels)
	devicePlugin := devicePluginFromNode(n, labels)

	// The card type is the card model (nvidia.com/gpu.product =
	// NVIDIA-GeForce-RTX-4090, ...), not the device-plugin resource family
	// ("gpu"): the compatibility matrix and the deploy form are keyed by
	// card type, and "gpu" cannot tell an A800 from a 4090.
	model := cardModelFromNode(vendor, labels)

	cardTypes := []string{}
	resources := []inventoryResource{}
	var gpusAllocated, gpusFree int64
	for name, qty := range n.Status.Allocatable {
		cardType, ok := cardTypeFromResource(name)
		if !ok {
			continue
		}
		if model != "" {
			cardType = model
		}
		allocatable := qty.Value()
		used := allocated[name]
		cardTypes = append(cardTypes, cardType)
		resources = append(resources, inventoryResource{
			CardType: cardType, Allocatable: allocatable, Allocated: used,
		})
		gpusAllocated += used
		gpusFree += allocatable - used
	}

	taints := make([]string, 0, len(n.Spec.Taints))
	for _, t := range n.Spec.Taints {
		taints = append(taints, t.Key+"="+t.Value+":"+string(t.Effect))
	}

	return inventoryNode{
		NodeID:            string(n.UID),
		Name:              n.Name,
		Vendor:            vendor,
		CardTypes:         cardTypes,
		GPUsAllocated:     gpusAllocated,
		GPUsFree:          gpusFree,
		DriverVersion:     driverVersion,
		DevicePluginState: devicePlugin,
		Readiness:         readiness,
		GPUs:              []inventoryGPU{},
		Labels:            acceleratorLabels(labels),
		Taints:            taints,
		Resources:         resources,
	}
}

// vendorFromNode derives the accelerator vendor (AD3). The explicit
// go-taas.com/accelerator label wins; otherwise the vendor is inferred
// from well-known device-plugin labels and extended resources, so a node
// that only advertises nvidia.com/gpu is classified as nvidia instead of
// "unspecified". A node with no accelerator signal stays "unspecified".
func vendorFromNode(n *corev1.Node) string {
	if v := n.Labels[labelAcceleratorVendor]; v != "" {
		switch v {
		case "nvidia", "iluvatar", "metax":
			return v
		}
	}
	labels := n.Labels
	// Device-plugin labels published by the vendor operators.
	if isTruthy(labels["nvidia.com/gpu.present"]) ||
		labels["nvidia.com/gpu.product"] != "" ||
		labels["nvidia.com/gpu.machine"] != "" {
		return "nvidia"
	}
	if _, ok := labels["iluvatar.ai/gpu.present"]; ok {
		return "iluvatar"
	}
	for l := range labels {
		if strings.HasPrefix(l, "iluvatar.ai/") {
			return "iluvatar"
		}
		if strings.HasPrefix(l, "metax-tech.com/") {
			return "metax"
		}
	}
	// Extended resources advertised by the device plugins.
	for name := range n.Status.Allocatable {
		switch {
		case strings.HasPrefix(string(name), "nvidia.com/"):
			return "nvidia"
		case strings.HasPrefix(string(name), "iluvatar.ai/"):
			return "iluvatar"
		case strings.HasPrefix(string(name), "metax-tech.com/"):
			return "metax"
		}
	}
	return "unspecified"
}

// isTruthy reports whether a label value means true.
func isTruthy(v string) bool {
	return v == "true" || v == "True" || v == "1"
}

// readinessFromNode derives the node readiness from the Ready condition.
func readinessFromNode(n *corev1.Node) string {
	for _, cond := range n.Status.Conditions {
		if cond.Type != corev1.NodeReady {
			continue
		}
		switch cond.Status {
		case corev1.ConditionTrue:
			return "ready"
		case corev1.ConditionFalse:
			return "not_ready"
		default:
			return "unknown"
		}
	}
	return "unknown"
}

// devicePluginFromNode derives the device-plugin state from the node
// label or the extended-resource allocatable.
func devicePluginFromNode(n *corev1.Node, labels map[string]string) string {
	if v := labels[labelDevicePlugin]; v != "" {
		switch v {
		case "healthy":
			return "healthy"
		case "degraded":
			return "degraded"
		default:
			return "unknown"
		}
	}
	// Fall back: a node with accelerator extended resources and no
	// explicit label is assumed healthy.
	for name := range n.Status.Allocatable {
		if _, ok := cardTypeFromResource(name); ok {
			return "healthy"
		}
	}
	return "unknown"
}

// driverVersionFromNode returns the node's GPU driver version, or "".
func driverVersionFromNode(labels map[string]string) string {
	for _, key := range driverVersionLabelKeys {
		if v := strings.TrimSpace(labels[key]); v != "" {
			return v
		}
	}
	// Some drivers publish only the parts.
	major := strings.TrimSpace(labels["nvidia.com/cuda.driver-version.major"])
	minor := strings.TrimSpace(labels["nvidia.com/cuda.driver-version.minor"])
	rev := strings.TrimSpace(labels["nvidia.com/cuda.driver-version.revision"])
	if major == "" || minor == "" {
		return ""
	}
	if rev != "" {
		return major + "." + minor + "." + rev
	}
	return major + "." + minor
}

// cardModelLabelKeys returns the vendor label keys that carry the card
// model, most specific first. Vendors publish the product name under
// their own prefix; go-taas.com/accelerator-type and gpu-product are the
// generic spellings an operator may use.
func cardModelLabelKeys(vendor string) []string {
	switch vendor {
	case "nvidia":
		return []string{"nvidia.com/gpu.product", "nvidia.com/gpu.model"}
	case "iluvatar":
		return []string{"iluvatar.ai/gpu.product", "iluvatar.ai/product", "gpu-product"}
	case "metax":
		return []string{"metax-tech.com/gpu.product", "metax-tech.com/product", "gpu-product"}
	default:
		return []string{"go-taas.com/accelerator-type", "gpu-product"}
	}
}

// cardModelFromNode returns the node's card model from the vendor product
// label, or "" when the vendor publishes none.
func cardModelFromNode(vendor string, labels map[string]string) string {
	for _, key := range cardModelLabelKeys(vendor) {
		if v := strings.TrimSpace(labels[key]); v != "" {
			return v
		}
	}
	return ""
}

// cardTypeFromResource parses the device-plugin resource family from an
// accelerator extended-resource name (e.g. nvidia.com/gpu -> gpu). It is
// the fallback card type when the vendor publishes no product label, and
// it returns false for non-accelerator resources.
func cardTypeFromResource(name corev1.ResourceName) (string, bool) {
	switch string(name) {
	case "nvidia.com/gpu", "iluvatar.com/gpu", "iluvatar.ai/gpu",
		"metax.com/gpu", "metax-tech.com/gpu", "metax-tech.com/vgpu":
		return "gpu", true
	default:
		return "", false
	}
}

// acceleratorLabels returns the accelerator-relevant node labels.
func acceleratorLabels(labels map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		if k == labelAcceleratorVendor || k == labelDriverVersion || k == labelDevicePlugin ||
			k == "nvidia.com/cuda.driver-version.full" || k == "nvidia.com/gpu.product" {
			out[k] = v
		}
	}
	return out
}
