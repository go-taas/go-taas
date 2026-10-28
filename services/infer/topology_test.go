package infer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func TestNormalizeTopologySingleIgnoresParallelism(t *testing.T) {
	// A single-node deploy is one pod per replica: a stray parallelism value
	// must not shape a pod group the operator did not ask for.
	spec, err := NormalizeTopology(TopologySingle, 4, 2, 3, 3, "vllm")
	require.NoError(t, err)
	assert.Equal(t, TopologySingle, spec.Topology)
	assert.Equal(t, 1, spec.TensorParallelSize)
	assert.Equal(t, 1, spec.PipelineParallelSize)
	assert.Equal(t, 0, spec.PrefillReplicas)
	assert.Equal(t, 0, spec.DecodeReplicas)
	assert.Equal(t, 1, spec.PodGroupSize())
}

func TestNormalizeTopologyDistributed(t *testing.T) {
	spec, err := NormalizeTopology(TopologyDistributed, 4, 2, 0, 0, "vllm")
	require.NoError(t, err)
	assert.Equal(t, TopologyDistributed, spec.Topology)
	assert.Equal(t, 4, spec.TensorParallelSize)
	assert.Equal(t, 2, spec.PipelineParallelSize)
	assert.Equal(t, 8, spec.PodGroupSize(), "one pod per shard")
}

func TestNormalizeTopologyDistributedNeedsMoreThanOnePod(t *testing.T) {
	// "Distributed" with a single pod is a single-node deploy: surface the
	// mistake instead of silently collapsing the layout.
	_, err := NormalizeTopology(TopologyDistributed, 1, 1, 0, 0, "vllm")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferTopologyInvalid, apierrors.CodeOf(err))

	// Zero values default to 1/1, which is still one pod.
	_, err = NormalizeTopology(TopologyDistributed, 0, 0, 0, 0, "vllm")
	require.Error(t, err)
}

func TestNormalizeTopologyParallelismBounds(t *testing.T) {
	_, err := NormalizeTopology(TopologyDistributed, maxParallelSize+1, 1, 0, 0, "vllm")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferTopologyInvalid, apierrors.CodeOf(err))

	_, err = NormalizeTopology(TopologyDistributed, 0, -1, 0, 0, "vllm")
	require.Error(t, err)

	// The pod group ceiling keeps one replica from asking for an unbounded
	// number of pods.
	_, err = NormalizeTopology(TopologyDistributed, maxParallelSize, maxParallelSize, 0, 0, "vllm")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferTopologyInvalid, apierrors.CodeOf(err))
}

func TestNormalizeTopologyPD(t *testing.T) {
	spec, err := NormalizeTopology(TopologyPD, 2, 1, 2, 3, "vllm")
	require.NoError(t, err)
	assert.Equal(t, TopologyPD, spec.Topology)
	assert.Equal(t, 2, spec.PrefillReplicas)
	assert.Equal(t, 3, spec.DecodeReplicas)
	assert.Equal(t, 2, spec.PodGroupSize())

	// Role counts default to one worker each.
	spec, err = NormalizeTopology(TopologyPD, 1, 1, 0, 0, "sglang")
	require.NoError(t, err)
	assert.Equal(t, 1, spec.PrefillReplicas)
	assert.Equal(t, 1, spec.DecodeReplicas)
}

func TestNormalizeTopologyPDNeedsASplittingEngine(t *testing.T) {
	_, err := NormalizeTopology(TopologyPD, 1, 1, 1, 1, "custom")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferTopologyInvalid, apierrors.CodeOf(err))
	assert.Contains(t, err.Error(), "vLLM or SGLang")
}

func TestNormalizeTopologyPDReplicaBounds(t *testing.T) {
	_, err := NormalizeTopology(TopologyPD, 1, 1, maxRoleReplicas+1, 1, "vllm")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferTopologyInvalid, apierrors.CodeOf(err))

	_, err = NormalizeTopology(TopologyPD, 1, 1, 1, maxRoleReplicas+1, "vllm")
	require.Error(t, err)
}

func TestNormalizeTopologyUnknownWireValue(t *testing.T) {
	_, err := NormalizeTopology("cluster-mesh", 1, 1, 0, 0, "vllm")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferTopologyInvalid, apierrors.CodeOf(err))
}

func TestStoredTopologyNameNormalizes(t *testing.T) {
	// A legacy or unknown stored value reads as single node.
	assert.Equal(t, TopologySingle, storedTopologyName(""))
	assert.Equal(t, TopologySingle, storedTopologyName("weird"))
	assert.Equal(t, TopologyDistributed, storedTopologyName("Distributed"))
	assert.Equal(t, TopologyPD, storedTopologyName(" PD "))
}

func TestNormalizeTopologyAcceptsNames(t *testing.T) {
	for _, name := range []string{TopologySingle, TopologyDistributed, TopologyPD} {
		tp := 1
		if name != TopologySingle {
			tp = 2
		}
		spec, err := NormalizeTopology(name, tp, 1, 1, 1, "vllm")
		require.NoError(t, err)
		assert.Equal(t, name, spec.Topology)
	}
	// Empty means single node: older clients send no topology at all.
	spec, err := NormalizeTopology("", 1, 1, 0, 0, "vllm")
	require.NoError(t, err)
	assert.Equal(t, TopologySingle, spec.Topology)
	// Case and whitespace are tolerated.
	spec, err = NormalizeTopology(" PD ", 1, 1, 1, 1, "vllm")
	require.NoError(t, err)
	assert.Equal(t, TopologyPD, spec.Topology)
}

func TestTopologyOfNormalizesEmptyRow(t *testing.T) {
	spec := TopologyOf(&InferenceService{})
	assert.Equal(t, TopologySingle, spec.Topology)
	assert.Equal(t, 1, spec.TensorParallelSize)
	assert.Equal(t, 1, spec.PipelineParallelSize)

	spec = TopologyOf(nil)
	assert.Equal(t, TopologySingle, spec.Topology)
	assert.Equal(t, 1, spec.PodGroupSize())

	spec = TopologyOf(&InferenceService{
		Topology:             TopologyPD,
		TensorParallelSize:   2,
		PipelineParallelSize: 2,
		PrefillReplicas:      2,
		DecodeReplicas:       4,
	})
	assert.Equal(t, TopologyPD, spec.Topology)
	assert.Equal(t, 4, spec.PodGroupSize())
	assert.Equal(t, 2, spec.PrefillReplicas)
	assert.Equal(t, 4, spec.DecodeReplicas)
}

func TestSupportsPDDisaggregation(t *testing.T) {
	assert.True(t, SupportsPDDisaggregation("vllm"))
	assert.True(t, SupportsPDDisaggregation("SGLang"))
	assert.True(t, SupportsPDDisaggregation(" vllm "))
	assert.False(t, SupportsPDDisaggregation("custom"))
	assert.False(t, SupportsPDDisaggregation(""))
}
