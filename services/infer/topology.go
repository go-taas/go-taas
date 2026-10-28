// Serving topology: how one inference service is laid out on the fleet.
//
// Every layout is realized by one AIBrix StormService (see the controller's
// stormservice.go), so the topology is the only place the difference between
// a single-node deploy, a distributed deploy and a PD-disaggregated deploy is
// expressed in this module:
//
//	single       one pod per replica
//	multinode    one pod group per replica: every worker is its own pod on
//	             its own machine, and the workers rendezvous so tensor and
//	             pipeline parallelism span nodes
//	pd           separate prefill and decode roles, each with its own
//	             replica count
//
// The wire enum is infersv1.ServingTopology; the database stores the same
// string so an operator reading the row sees a name instead of a number.

package infer

import (
	"strings"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Topology names as stored in inference_services.topology.
const (
	TopologySingle = "single"
	// TopologyMultiNode spreads one replica across several pods, one per
	// machine. It replaced the single-pod "distributed" layout, whose name
	// stays accepted on input so an older client keeps working.
	TopologyMultiNode = "multinode"
	TopologyPD        = "pd"

	// legacyTopologyDistributed is the pre-rename name of TopologyMultiNode.
	legacyTopologyDistributed = "distributed"
)

// Bounds for the parallelism a topology may request.
const (
	// maxParallelSize bounds tensor and pipeline parallelism per replica.
	// It matches the StormService CRD's podGroupSize ceiling (100).
	maxParallelSize = 100
	// maxRoleReplicas bounds the prefill/decode worker count of a PD
	// service.
	maxRoleReplicas = 100
	// maxPodsPerReplica bounds pods * replicas so a single deploy cannot
	// ask for an unbounded pod group.
	maxPodsPerReplica = 100
)

// TopologySpec is a normalized serving layout.
type TopologySpec struct {
	// Topology is one of TopologySingle, TopologyMultiNode, TopologyPD.
	Topology string
	// TensorParallelSize is the TP degree of one replica.
	TensorParallelSize int
	// PipelineParallelSize is the PP degree of one replica.
	PipelineParallelSize int
	// PrefillReplicas / DecodeReplicas are the PD role counts; 0 for the
	// other topologies.
	PrefillReplicas int
	DecodeReplicas  int
}

// PodGroupSize is how many pods one replica of the role runs with: one pod
// per worker (tensor/pipeline shard), each on its own machine. A single-node
// replica is one pod.
func (t TopologySpec) PodGroupSize() int {
	size := t.TensorParallelSize * t.PipelineParallelSize
	if size < 1 {
		return 1
	}
	return size
}

// NormalizeTopology validates a requested layout and returns it with every
// field filled in, so the caller never stores a partially specified spec.
// engine is the serving engine of the chosen image: PD disaggregation needs
// an engine that speaks the prefill/decode split.
//
// Validation order: topology, parallelism bounds, engine support, role
// counts. Nothing is written when a check fails.
func NormalizeTopology(topology string, tensorParallel, pipelineParallel, prefill, decode int, engine string) (TopologySpec, error) {
	// The set is closed: an unknown layout is a client bug, not a request to
	// pick a default. An empty value means "single", which is what a client
	// that predates topologies sends.
	name := strings.ToLower(strings.TrimSpace(topology))
	if name == "" {
		name = TopologySingle
	}
	if name == legacyTopologyDistributed {
		// The layout was renamed when it became genuinely multi-pod; the old
		// name keeps working so a console or script written before the
		// rename is not broken by it.
		name = TopologyMultiNode
	}
	switch name {
	case TopologySingle, TopologyMultiNode, TopologyPD:
	default:
		return TopologySpec{}, apierrors.Newf(apierrors.CodeInferTopologyInvalid,
			"infer: unknown topology %q; use single, multinode or pd", topology)
	}
	spec := TopologySpec{Topology: name}
	switch spec.Topology {
	case TopologySingle:
		// A single-node deploy is exactly one pod per replica; parallelism
		// is not expressible there, so a stray value is ignored rather than
		// silently shaping a pod group the operator did not ask for.
		spec.TensorParallelSize = 1
		spec.PipelineParallelSize = 1
		return spec, nil
	case TopologyMultiNode, TopologyPD:
	default:
		return TopologySpec{}, apierrors.New(apierrors.CodeInferTopologyInvalid)
	}

	tp := tensorParallel
	if tp == 0 {
		tp = 1
	}
	pp := pipelineParallel
	if pp == 0 {
		pp = 1
	}
	if tp < 1 || tp > maxParallelSize || pp < 1 || pp > maxParallelSize {
		return TopologySpec{}, apierrors.Newf(apierrors.CodeInferTopologyInvalid,
			"infer: tensor_parallel_size and pipeline_parallel_size must be 1-%d", maxParallelSize)
	}
	if tp*pp > maxPodsPerReplica {
		return TopologySpec{}, apierrors.Newf(apierrors.CodeInferTopologyInvalid,
			"infer: one replica may span at most %d pods", maxPodsPerReplica)
	}
	spec.TensorParallelSize = tp
	spec.PipelineParallelSize = pp

	if spec.Topology == TopologyMultiNode {
		if tp*pp < 2 {
			// Multi-machine serving with one pod is a single-node deploy;
			// asking for it is a mistake worth surfacing instead of
			// silently collapsing.
			return TopologySpec{}, apierrors.Newf(apierrors.CodeInferTopologyInvalid,
				"infer: multi-machine serving needs tensor_parallel_size * pipeline_parallel_size > 1 (one pod per worker); use the single-node topology for one pod")
		}
		return spec, nil
	}

	// PD disaggregation: the engine has to support the prefill/decode split.
	if !SupportsPDDisaggregation(engine) {
		return TopologySpec{}, apierrors.Newf(apierrors.CodeInferTopologyInvalid,
			"infer: PD disaggregation needs a vLLM or SGLang engine image, got %q", engine)
	}
	if prefill < 1 {
		prefill = 1
	}
	if decode < 1 {
		decode = 1
	}
	if prefill > maxRoleReplicas || decode > maxRoleReplicas {
		return TopologySpec{}, apierrors.Newf(apierrors.CodeInferTopologyInvalid,
			"infer: prefill_replicas and decode_replicas must be 1-%d", maxRoleReplicas)
	}
	spec.PrefillReplicas = prefill
	spec.DecodeReplicas = decode
	return spec, nil
}

// SupportsPDDisaggregation reports whether an engine speaks the
// prefill/decode split. The check is on the engine name the image catalog
// stores, so a newly registered engine image is covered without a code
// change as long as it is registered as vLLM or SGLang.
func SupportsPDDisaggregation(engine string) bool {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "vllm", "sglang":
		return true
	default:
		return false
	}
}

// TopologyOf returns the normalized layout stored on a service row.
func TopologyOf(svc *InferenceService) TopologySpec {
	if svc == nil {
		return TopologySpec{Topology: TopologySingle, TensorParallelSize: 1, PipelineParallelSize: 1}
	}
	tp := svc.TensorParallelSize
	if tp < 1 {
		tp = 1
	}
	pp := svc.PipelineParallelSize
	if pp < 1 {
		pp = 1
	}
	return TopologySpec{
		Topology:             storedTopologyName(svc.Topology),
		TensorParallelSize:   tp,
		PipelineParallelSize: pp,
		PrefillReplicas:      svc.PrefillReplicas,
		DecodeReplicas:       svc.DecodeReplicas,
	}
}

// storedTopologyName normalizes a stored topology name.
func storedTopologyName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case TopologyMultiNode, legacyTopologyDistributed:
		return TopologyMultiNode
	case TopologyPD:
		return TopologyPD
	default:
		return TopologySingle
	}
}
