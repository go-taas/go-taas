package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestAvailableAcceleratorLabels(t *testing.T) {
	nodes := []corev1.Node{
		{},
		{},
	}
	nodes[0].Labels = map[string]string{
		"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090",
		"nvidia.com/gpu.count":   "2",
		"kubernetes.io/hostname": "sd4090",
	}
	nodes[1].Labels = map[string]string{
		"nvidia.com/gpu.product":  "NVIDIA-GeForce-RTX-4090",
		"go-taas.com/accelerator": "nvidia",
	}
	got := availableAcceleratorLabels(nodes)
	assert.Contains(t, got, "nvidia.com/gpu.product=NVIDIA-GeForce-RTX-4090")
	assert.Contains(t, got, "go-taas.com/accelerator=nvidia")
	assert.NotContains(t, got, "kubernetes.io/hostname=sd4090", "non-accelerator labels are omitted")
	// NVIDIA metadata keys are not selector candidates.
	assert.NotContains(t, got, "nvidia.com/gpu.count=2")
	// De-duplicated: product (both nodes) + the go-taas accelerator label.
	assert.Len(t, got, 2)
}

func TestShortUID(t *testing.T) {
	assert.Equal(t, "372523fd", shortUID("372523fd-cacf-4f1a-9425-71af16ede188"))
	assert.Equal(t, "abcdefgh", shortUID("abcdefgh"))
}
