package accelerator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestIsVendor(t *testing.T) {
	for _, id := range VendorIDs() {
		assert.True(t, IsVendor(id), id)
	}
	assert.False(t, IsVendor(""))
	assert.False(t, IsVendor("unspecified"))
	assert.False(t, IsVendor("unknown-vendor"))
}

func TestVendorIDsAreStableAndUnique(t *testing.T) {
	ids := VendorIDs()
	assert.NotEmpty(t, ids)
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate vendor id %q", id)
		}
		seen[id] = struct{}{}
	}
	assert.Equal(t, "nvidia", ids[0], "nvidia stays the canonical first vendor")
}

// TestDetectVendor covers the vendor's own operator labels and extended
// resources, without any go-taas.com/* platform label.
func TestDetectVendor(t *testing.T) {
	cases := []struct {
		name      string
		labels    map[string]string
		resources []corev1.ResourceName
		want      string
	}{
		{
			name:   "nvidia product label",
			labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090"},
			want:   "nvidia",
		},
		{
			name:      "nvidia resource",
			resources: []corev1.ResourceName{"nvidia.com/gpu"},
			want:      "nvidia",
		},
		{
			name:   "amd node labeller",
			labels: map[string]string{"amd.com/gpu.product-name": "Instinct MI300X"},
			want:   "amd",
		},
		{
			name:      "amd resource",
			resources: []corev1.ResourceName{"amd.com/gpu"},
			want:      "amd",
		},
		{
			name:   "intel nfd label",
			labels: map[string]string{"gpu.intel.com/product": "Flex_170"},
			want:   "intel",
		},
		{
			name:      "intel resource",
			resources: []corev1.ResourceName{"gpu.intel.com/i915"},
			want:      "intel",
		},
		{
			name:      "huawei ascend resource",
			resources: []corev1.ResourceName{"huawei.com/Ascend910B4"},
			want:      "huawei",
		},
		{
			name:      "cambricon resource",
			resources: []corev1.ResourceName{"cambricon.com/mlu370"},
			want:      "cambricon",
		},
		{
			name:      "biren resource",
			resources: []corev1.ResourceName{"birentech.com/gpu"},
			want:      "biren",
		},
		{
			name:      "enflame resource",
			resources: []corev1.ResourceName{"enflame.com/gcu"},
			want:      "enflame",
		},
		{
			name:      "hygon resource",
			resources: []corev1.ResourceName{"hygon.com/dcunum"},
			want:      "hygon",
		},
		{
			name:      "kunlunxin resource",
			resources: []corev1.ResourceName{"kunlunxin.com/xpu"},
			want:      "kunlunxin",
		},
		{
			name:      "mthreads resource",
			resources: []corev1.ResourceName{"mthreads.com/gpu"},
			want:      "mthreads",
		},
		{
			name:   "iluvatar label",
			labels: map[string]string{"iluvatar.ai/gpu.present": "true"},
			want:   "iluvatar",
		},
		{
			name:      "metax resource",
			resources: []corev1.ResourceName{"metax-tech.com/vgpu"},
			want:      "metax",
		},
		{
			name:      "explicit platform label wins",
			labels:    map[string]string{"go-taas.com/accelerator": "metax", "nvidia.com/gpu.product": "x"},
			resources: []corev1.ResourceName{"nvidia.com/gpu"},
			want:      "metax",
		},
		{
			name:   "legacy gpu-vendor fallback",
			labels: map[string]string{"gpu-vendor": "amd"},
			want:   "amd",
		},
		{
			name:   "unknown explicit label ignored",
			labels: map[string]string{"go-taas.com/accelerator": "bogus"},
			want:   "",
		},
		{
			name:   "no accelerator signal",
			labels: map[string]string{"kubernetes.io/hostname": "node01"},
			want:   "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resources := map[corev1.ResourceName]int64{}
			for _, r := range c.resources {
				resources[r] = 1
			}
			assert.Equal(t, c.want, DetectVendor(c.labels, resources))
		})
	}
}

func TestClassifyResource(t *testing.T) {
	cases := []struct {
		name   string
		vendor string
		family string
	}{
		{"nvidia.com/gpu", "nvidia", "gpu"},
		{"nvidia.com/mig-1g.5gb", "nvidia", "gpu"},
		{"amd.com/gpu", "amd", "gpu"},
		{"amd.com/gpu_pf", "amd", "gpu"},
		{"gpu.intel.com/i915", "intel", "gpu"},
		{"gpu.intel.com/xe", "intel", "gpu"},
		{"iluvatar.ai/gpu", "iluvatar", "gpu"},
		{"iluvatar.com/gpu", "iluvatar", "gpu"},
		{"metax-tech.com/gpu", "metax", "gpu"},
		{"metax-tech.com/vgpu", "metax", "vgpu"},
		{"metax.com/gpu", "metax", "gpu"},
		{"huawei.com/Ascend910B4", "huawei", "Ascend910B4"},
		{"cambricon.com/mlu370", "cambricon", "mlu"},
		{"birentech.com/gpu", "biren", "gpu"},
		{"enflame.com/gcu", "enflame", "gcu"},
		{"enflame.com/vgcu", "enflame", "vgcu"},
		{"hygon.com/dcunum", "hygon", "dcu"},
		{"hygon.com/hcunum", "hygon", "hcu"},
		{"kunlunxin.com/xpu", "kunlunxin", "xpu"},
		{"kunlunxin.com/vxpu", "kunlunxin", "vxpu"},
		{"mthreads.com/gpu", "mthreads", "gpu"},
		{"mthreads.com/vgpu", "mthreads", "vgpu"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vendor, family, ok := ClassifyResource(corev1.ResourceName(c.name))
			require.True(t, ok, c.name)
			assert.Equal(t, c.vendor, vendor, c.name)
			assert.Equal(t, c.family, family, c.name)
		})
	}
	_, _, ok := ClassifyResource("cpu")
	assert.False(t, ok)
	_, _, ok = ClassifyResource("gpu.intel.com/i915_monitoring")
	assert.False(t, ok, "monitoring resources are not schedulable cards")
	_, _, ok = ClassifyResource("gpu.intel.com/monitoring")
	assert.False(t, ok)
	_, _, ok = ClassifyResource("example.com/other")
	assert.False(t, ok)
}

func TestCardModelFromLabels(t *testing.T) {
	cases := []struct {
		name   string
		vendor string
		labels map[string]string
		want   string
	}{
		{
			name:   "nvidia product",
			vendor: "nvidia",
			labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090"},
			want:   "NVIDIA-GeForce-RTX-4090",
		},
		{
			name:   "amd product name",
			vendor: "amd",
			labels: map[string]string{"amd.com/gpu.product-name": "Instinct MI300X"},
			want:   "Instinct MI300X",
		},
		{
			name:   "amd legacy beta product",
			vendor: "amd",
			labels: map[string]string{"beta.amd.com/gpu.product-name": "Instinct MI250X"},
			want:   "Instinct MI250X",
		},
		{
			name:   "intel product",
			vendor: "intel",
			labels: map[string]string{"gpu.intel.com/product": "Flex_170"},
			want:   "Flex_170",
		},
		{
			name:   "iluvatar generic product",
			vendor: "iluvatar",
			labels: map[string]string{"gpu-product": "BI-V100"},
			want:   "BI-V100",
		},
		{
			name:   "metax product",
			vendor: "metax",
			labels: map[string]string{"metax-tech.com/gpu.product": "MetaX-C500"},
			want:   "MetaX-C500",
		},
		{
			name:   "vendor label preferred over generic",
			vendor: "nvidia",
			labels: map[string]string{"gpu-product": "generic", "nvidia.com/gpu.product": "A100"},
			want:   "A100",
		},
		{
			name:   "no model",
			vendor: "biren",
			labels: map[string]string{"birentech.com/gpu.present": "true"},
			want:   "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, CardModelFromLabels(c.vendor, c.labels))
		})
	}
}

func TestCardModelFromResource(t *testing.T) {
	assert.Equal(t, "Ascend910B4", CardModelFromResource(corev1.ResourceName("huawei.com/Ascend910B4")))
	assert.Equal(t, "Ascend910B4", CardModelFromResource(corev1.ResourceName("huawei.com/Ascend910B4-memory")))
	assert.Equal(t, "MLU370", CardModelFromResource(corev1.ResourceName("cambricon.com/mlu370")))
	assert.Equal(t, "MLU370", CardModelFromResource(corev1.ResourceName("cambricon.com/mlu370.share")))
	assert.Equal(t, "MLU370", CardModelFromResource(corev1.ResourceName("cambricon.com/mlu370.mim-2m.8gb")))
	assert.Empty(t, CardModelFromResource(corev1.ResourceName("nvidia.com/gpu")))
}

func TestCardModelFromHAMI(t *testing.T) {
	annotations := map[string]string{
		"hami.io/node-amd-register":   `[{"id":"0","type":"AMD Instinct MI300X","health":true}]`,
		"hami.io/node-biren-register": `[{"id":"0","type":"Biren BR104","health":true}]`,
		"unrelated":                   `[{"type":"should-not-be-read"}]`,
	}
	assert.Equal(t, "AMD Instinct MI300X", CardModelFromHAMI("amd", annotations))
	assert.Equal(t, "Biren BR104", CardModelFromHAMI("biren", annotations))
	assert.Equal(t, "", CardModelFromHAMI("nvidia", annotations))
}

func TestDriverVersion(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{
			name:   "platform label wins",
			labels: map[string]string{"go-taas.com/driver-version": "1.2.3", "nvidia.com/cuda.driver-version.full": "595.91.07"},
			want:   "1.2.3",
		},
		{
			name:   "nvidia full",
			labels: map[string]string{"nvidia.com/cuda.driver-version.full": "595.91.07"},
			want:   "595.91.07",
		},
		{
			name:   "amd",
			labels: map[string]string{"amd.com/gpu.driver-version": "6.2.1"},
			want:   "6.2.1",
		},
		{
			name:   "enflame",
			labels: map[string]string{"enflame.com/gcu.driverVer": "1.7.2.2402"},
			want:   "1.7.2.2402",
		},
		{
			name: "nvidia parts",
			labels: map[string]string{
				"nvidia.com/cuda.driver-version.major":    "595",
				"nvidia.com/cuda.driver-version.minor":    "91",
				"nvidia.com/cuda.driver-version.revision": "07",
			},
			want: "595.91.07",
		},
		{
			name:   "absent",
			labels: map[string]string{},
			want:   "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, DriverVersion(c.labels))
		})
	}
}

func TestSelectableLabel(t *testing.T) {
	for _, key := range []string{
		"nvidia.com/gpu.product",
		"amd.com/gpu.product-name",
		"gpu.intel.com/product",
		"iluvatar.ai/gpu.product",
		"metax-tech.com/gpu.product",
		"huawei.com/Ascend910B4",
		"cambricon.com/node-mlu-cards",
		"birentech.com/gpu",
		"enflame.com/gcu",
		"hygon.com/dcunum",
		"kunlunxin.com/xpu",
		"mthreads.com/gpu",
		"go-taas.com/accelerator",
		"gpu-vendor",
	} {
		assert.True(t, SelectableLabel(key), key)
	}
	assert.False(t, SelectableLabel("kubernetes.io/hostname"))
	assert.False(t, SelectableLabel("nvidia.com/gpu.deploy.driver"))
}
