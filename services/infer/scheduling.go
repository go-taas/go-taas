// Per-pod resource shape and scheduling priority.
//
// A serving pod used to ask for exactly one device and nothing else: the
// cluster's scheduler then placed it wherever a GPU was free, with no promise
// about CPU or memory and no way to say which workload matters more. That is
// fine for a platform with one tenant and one model; on a shared fleet it
// lets a large model's pod start on a node that cannot hold it once the
// weights load, and it gives the scheduler no way to prefer a production
// service over a load test.
//
// PodScheduling is that missing input: how many devices one pod takes, how
// much CPU and memory it needs, and its priority. Every topology accepts it
// (single, multinode, pd) because the constraint is about the pod, not the
// layout.
//
// The priority is enforced by Volcano, which schedules these pods as a gang
// and may preempt a lower priority gang for a higher one (see the controller's
// podgroup.go). "High" and "low" therefore mean something only where a
// deployment has Volcano; without it the value is stored and ignored.

package infer

import (
	"math"
	"strings"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	infersv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

// Scheduling priorities a service may name. They map to Kubernetes
// PriorityClasses in the controller, so the set is closed.
const (
	PriorityHigh   = "high"
	PriorityNormal = "normal"
	PriorityLow    = "low"
)

// Bounds for one pod's requests. They exist to catch a typo (a memory request
// of 999999 MiB) before it reaches the scheduler, not to model any cluster.
const (
	maxGPUPerPod     = 8
	maxCPUPerPod     = 128000  // milli-cores: 128 cores
	maxMemoryMiB     = 1 << 20 // 1 TiB
	defaultGPUPerPod = 1
)

// PodScheduling is a normalized per-pod resource shape.
type PodScheduling struct {
	// GPUPerPod is the number of devices one pod requests (>= 1).
	GPUPerPod int
	// CPUMilli is the CPU request of one pod in milli-cores (0 = none).
	CPUMilli int64
	// MemoryMiB is the memory request of one pod in MiB (0 = none).
	MemoryMiB int64
	// Priority is high, normal or low.
	Priority string
}

// NormalizePodScheduling validates a requested per-pod shape and fills in the
// defaults. A nil request is the platform default: one device per pod, no CPU
// or memory request, normal priority.
func NormalizePodScheduling(req *infersv1.PodScheduling) (PodScheduling, error) {
	spec := PodScheduling{GPUPerPod: defaultGPUPerPod, Priority: PriorityNormal}
	if req == nil {
		return spec, nil
	}
	gpus := int(req.GetGpuPerPod())
	if gpus == 0 {
		gpus = defaultGPUPerPod
	}
	if gpus < 0 || gpus > maxGPUPerPod {
		return PodScheduling{}, apierrors.Newf(apierrors.CodeInferSchedulingInvalid,
			"infer: gpu_per_pod must be 1-%d", maxGPUPerPod)
	}
	spec.GPUPerPod = gpus

	cores := req.GetCpuCores()
	if cores < 0 || math.IsNaN(cores) || math.IsInf(cores, 0) {
		return PodScheduling{}, apierrors.New(apierrors.CodeInferSchedulingInvalid)
	}
	milli := int64(math.Round(cores * 1000))
	if milli > maxCPUPerPod {
		return PodScheduling{}, apierrors.Newf(apierrors.CodeInferSchedulingInvalid,
			"infer: cpu_cores must be at most %d", maxCPUPerPod/1000)
	}
	spec.CPUMilli = milli

	memory := req.GetMemoryMib()
	if memory < 0 || memory > maxMemoryMiB {
		return PodScheduling{}, apierrors.Newf(apierrors.CodeInferSchedulingInvalid,
			"infer: memory_mib must be 0-%d", maxMemoryMiB)
	}
	spec.MemoryMiB = memory

	switch strings.ToLower(strings.TrimSpace(req.GetPriority())) {
	case "", PriorityNormal:
		spec.Priority = PriorityNormal
	case PriorityHigh:
		spec.Priority = PriorityHigh
	case PriorityLow:
		spec.Priority = PriorityLow
	default:
		return PodScheduling{}, apierrors.Newf(apierrors.CodeInferSchedulingInvalid,
			"infer: priority must be high, normal or low, got %q", req.GetPriority())
	}
	return spec, nil
}

// SchedulingOf returns the per-pod shape stored on a service row, applying
// the same defaults so a row written before the columns existed reads the
// way it actually deployed.
func SchedulingOf(svc *InferenceService) PodScheduling {
	if svc == nil {
		return PodScheduling{GPUPerPod: defaultGPUPerPod, Priority: PriorityNormal}
	}
	gpus := svc.GPUPerPod
	if gpus < 1 {
		gpus = defaultGPUPerPod
	}
	priority := strings.ToLower(strings.TrimSpace(svc.Priority))
	if priority == "" {
		priority = PriorityNormal
	}
	return PodScheduling{
		GPUPerPod: gpus,
		CPUMilli:  svc.CPUMilli,
		MemoryMiB: svc.MemoryMiB,
		Priority:  priority,
	}
}

// clampGPUPerPod narrows a validated device count to the wire's int32 without
// an unchecked conversion.
func clampGPUPerPod(cards int) int32 {
	if cards < 1 {
		cards = 1
	}
	if cards > maxGPUPerPod {
		cards = maxGPUPerPod
	}
	return int32(cards) //nolint:gosec // G115: bounded to 1..maxGPUPerPod just above
}

// schedulingToProto projects a stored shape onto the wire.
func schedulingToProto(svc *InferenceService) *infersv1.PodScheduling {
	spec := SchedulingOf(svc)
	return &infersv1.PodScheduling{
		GpuPerPod: clampGPUPerPod(spec.GPUPerPod),
		CpuCores:  float64(spec.CPUMilli) / 1000,
		MemoryMib: spec.MemoryMiB,
		Priority:  spec.Priority,
	}
}

// validatePodsForTopology checks that a replica's devices divide into pods of
// the requested size.
//
// One pod owns gpu_per_pod devices, and a replica's parallelism is the total
// number of devices it shards across (tensor x pipeline). A multi-pod replica
// therefore needs (tp x pp) % gpu_per_pod == 0: an uneven split would give the
// engine a world size its pods cannot see, which vLLM refuses at startup.
// A single-pod replica has no such constraint - the one pod simply takes
// gpu_per_pod devices.
func validatePodsForTopology(spec TopologySpec, scheduling PodScheduling) error {
	if spec.Topology == TopologySingle {
		return nil
	}
	devices := spec.TensorParallelSize * spec.PipelineParallelSize
	if devices < 1 {
		devices = 1
	}
	if scheduling.GPUPerPod > devices || devices%scheduling.GPUPerPod != 0 {
		return apierrors.Newf(apierrors.CodeInferTopologyInvalid,
			"infer: %d devices per replica cannot be split into pods of %d; use a gpu_per_pod that divides tensor_parallel_size x pipeline_parallel_size",
			devices, scheduling.GPUPerPod)
	}
	return nil
}

// PodGroupSizeFor is how many pods one replica runs with when each pod owns
// gpuPerPod devices. A single-node replica is always one pod.
func (t TopologySpec) PodGroupSizeFor(gpuPerPod int) int {
	if t.Topology == TopologySingle {
		return 1
	}
	if gpuPerPod < 1 {
		gpuPerPod = 1
	}
	devices := t.TensorParallelSize * t.PipelineParallelSize
	if devices < 1 {
		return 1
	}
	size := devices / gpuPerPod
	if size < 1 {
		return 1
	}
	return size
}
