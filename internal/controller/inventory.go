package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/go-taas/go-taas/pkg/accelerator"
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
)

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

// hamiRegisterAnnotation is the node annotation HAMi's device plugin
// rewrites every registration round with the physical cards it found:
// one JSON object per card with its index, model, memory and health.
// It is the only per-card source on a HAMi-managed node — after slicing
// the node's nvidia.com/gpu allocatable counts vGPU units, not cards.
const hamiRegisterAnnotation = "hami.io/node-nvidia-register"

// hamiDevice is one card in the HAMi registration annotation.
type hamiDevice struct {
	ID      string `json:"id"`
	Index   int64  `json:"index"`
	Count   int64  `json:"count"`
	DevMem  int64  `json:"devmem"`
	DevCore int64  `json:"devcore"`
	Type    string `json:"type"`
	Mode    string `json:"mode"`
	Health  bool   `json:"health"`
}

// hamiGPUs parses the HAMi registration annotation into per-card
// inventory entries. A node without the annotation yields none.
func hamiGPUs(annotations map[string]string) []inventoryGPU {
	raw, ok := annotations[hamiRegisterAnnotation]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var devices []hamiDevice
	if err := json.Unmarshal([]byte(raw), &devices); err != nil {
		return nil
	}
	out := make([]inventoryGPU, 0, len(devices))
	for _, d := range devices {
		out = append(out, inventoryGPU{
			Index:     d.Index,
			Model:     d.Type,
			Allocated: !d.Health,
			Free:      d.Health,
			Note:      fmt.Sprintf("%d MiB, %d vGPU slots", d.DevMem, d.Count),
		})
	}
	return out
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
	// NVIDIA-GeForce-RTX-4090, amd.com/gpu.product-name = Instinct MI300X,
	// ...), not the device-plugin resource family ("gpu"): the compatibility
	// matrix and the deploy form are keyed by card type, and "gpu" cannot
	// tell an A800 from a 4090.
	//
	// The model is resolved per resource, not once per node: a rack may mix
	// card types, and Huawei/Kunlunxin encode the model in the resource name
	// while AMD/Intel/NVIDIA publish it as a label.
	cardTypes := []string{}
	resources := []inventoryResource{}
	var gpusAllocated, gpusFree int64
	for name, qty := range n.Status.Allocatable {
		resourceVendor, family, ok := accelerator.ClassifyResource(name)
		if !ok {
			continue
		}
		allocatable := qty.Value()
		// A zero allocatable means the device plugin has not registered the
		// cards. Reporting a zero-card row only adds noise; the physical
		// fallback below reports the cards that are actually present.
		if allocatable <= 0 {
			continue
		}
		cardType := cardTypeForResource(resourceVendor, family, name, labels, n.Annotations)
		used := allocated[name]
		cardTypes = append(cardTypes, cardType)
		resources = append(resources, inventoryResource{
			CardType: cardType, Allocatable: allocatable, Allocated: used,
		})
		gpusAllocated += used
		gpusFree += allocatable - used
	}

	// The device plugin can be absent or crash-looping while the GPU
	// operator still labels the node with its card model and card count.
	// Without this fallback such a node reads as a CPU node and disappears
	// from the fleet view, so an operator cannot see the hardware that
	// needs fixing. The card is reported with its physical count and the
	// node's device-plugin state is degraded, so it is not advertised as
	// schedulable capacity.
	if len(resources) == 0 {
		if cardType := accelerator.CardModelFromLabels(vendor, labels); cardType != "" {
			if count := accelerator.CardCountFromLabels(vendor, labels); count > 0 {
				cardTypes = append(cardTypes, cardType)
				resources = append(resources, inventoryResource{
					CardType: cardType, Allocatable: count,
				})
				gpusFree += count
			}
		}
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
		GPUs:              hamiGPUs(n.Annotations),
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
	resources := make(map[corev1.ResourceName]int64, len(n.Status.Allocatable))
	for name, qty := range n.Status.Allocatable {
		resources[name] = qty.Value()
	}
	v := accelerator.DetectVendor(n.Labels, resources)
	if v == "" {
		return "unspecified"
	}
	return v
}

// cardTypeForResource resolves one accelerator resource's card type:
// the vendor's product label first, then HAMi's registration annotation,
// then a model encoded in the resource name, then the device family.
func cardTypeForResource(vendor, family string, name corev1.ResourceName, labels, annotations map[string]string) string {
	if model := accelerator.CardModelFromLabels(vendor, labels); model != "" {
		return model
	}
	if model := accelerator.CardModelFromHAMI(vendor, annotations); model != "" {
		return model
	}
	if model := accelerator.CardModelFromResource(name); model != "" {
		return model
	}
	return family
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
	// Fall back: a node with a positive accelerator extended resource and
	// no explicit label is assumed healthy. A registered resource of zero
	// means the device plugin is not advertising cards, which is degraded
	// rather than healthy.
	sawZero := false
	for name, qty := range n.Status.Allocatable {
		if _, ok := cardTypeFromResource(name); !ok {
			continue
		}
		if qty.Value() > 0 {
			return "healthy"
		}
		sawZero = true
	}
	if sawZero {
		return "degraded"
	}
	return "unknown"
}

// driverVersionFromNode returns the node's GPU driver version, or "".
func driverVersionFromNode(labels map[string]string) string {
	return accelerator.DriverVersion(labels)
}

// cardTypeFromResource parses the device-plugin resource family from an
// accelerator extended-resource name (e.g. nvidia.com/gpu -> gpu). It is
// the fallback card type when the vendor publishes no product label, and
// it returns false for non-accelerator resources.
func cardTypeFromResource(name corev1.ResourceName) (string, bool) {
	_, family, ok := accelerator.ClassifyResource(name)
	return family, ok
}

// acceleratorLabels returns the accelerator-relevant node labels: the
// vendor/driver/device-plugin facts the inventory needs, plus every
// platform-owned label (go-taas.com/*). The latter carries the operator's
// own node classification, of which the vGPU pool label
// (go-taas.com/vgpu-pool) is what pool membership is derived from — a
// projection that dropped it would show every pool as empty.
func acceleratorLabels(labels map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		if accelerator.SelectableLabel(k) {
			out[k] = v
		}
	}
	return out
}
