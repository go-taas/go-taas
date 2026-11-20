// Package accelerator holds the vendor label contract for accelerator
// cards.
//
// Each accelerator vendor publishes its own node labels through its GPU
// operator (NVIDIA GPU Feature Discovery, AMD GPU Operator node labeller,
// Intel NFD rules, and the domestic device plugins). The platform must
// read those labels instead of requiring a go-taas.com/accelerator label:
// a cluster installed with a vendor operator alone has to be classified
// without platform-side annotation.
package accelerator

import (
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Platform labels. The explicit go-taas.com/accelerator label is an
// operator override; it is never required.
const (
	LabelVendor          = "go-taas.com/accelerator"
	LabelDriver          = "go-taas.com/driver-version"
	LabelLegacyVendor    = "gpu-vendor"
	LabelAcceleratorType = "go-taas.com/accelerator-type"
)

// VendorProfile is one vendor's auto-labelling contract.
type VendorProfile struct {
	// ID is the stable, lower-case API value ("nvidia", "amd", ...).
	ID string
	// DisplayName is the vendor name for UI and documentation.
	DisplayName string
	// LabelPrefixes are the node-label prefixes the vendor's operator owns.
	LabelPrefixes []string
	// vendor's device plugin advertises.
	// ModelLabelKeys carry the card model/product, most specific first.
	ModelLabelKeys []string
	// DriverLabelKeys carry the driver version, most specific first.
	DriverLabelKeys []string
	// HAMIRegisterAnnotations are HAMi node annotations whose device list
	// carries the card model when the vendor publishes no product label.
	HAMIRegisterAnnotations []string
	// HAMIRegisterAnnotationPrefixes cover per-card HAMi annotations
	// (Ascend publishes one annotation per card family).
	HAMIRegisterAnnotationPrefixes []string
}

// vendorProfiles is ordered: nvidia first (the canonical fleet vendor),
// then the other international vendors, then the domestic vendors.
var vendorProfiles = []VendorProfile{
	{
		ID:            "nvidia",
		DisplayName:   "NVIDIA",
		LabelPrefixes: []string{"nvidia.com/"},
		ModelLabelKeys: []string{
			"nvidia.com/gpu.product",
			"nvidia.com/gpu.model",
		},
		DriverLabelKeys: []string{
			"nvidia.com/cuda.driver-version.full",
			"nvidia.com/gpu.driver-version",
		},
		HAMIRegisterAnnotations: []string{"hami.io/node-nvidia-register"},
	},
	{
		ID:            "amd",
		DisplayName:   "AMD",
		LabelPrefixes: []string{"amd.com/", "beta.amd.com/"},
		ModelLabelKeys: []string{
			"amd.com/gpu.product-name",
			"beta.amd.com/gpu.product-name",
			"amd.com/gpu.product",
			"amd.com/gpu.family",
		},
		DriverLabelKeys: []string{
			"amd.com/gpu.driver-version",
			"beta.amd.com/gpu.driver-version",
		},
		HAMIRegisterAnnotations: []string{"hami.io/node-amd-register"},
	},
	{
		ID:            "intel",
		DisplayName:   "Intel",
		LabelPrefixes: []string{"gpu.intel.com/"},
		ModelLabelKeys: []string{
			"gpu.intel.com/product",
			"gpu.intel.com/family",
		},
		DriverLabelKeys: []string{
			"gpu.intel.com/driver-version",
		},
	},
	{
		ID:            "iluvatar",
		DisplayName:   "Iluvatar",
		LabelPrefixes: []string{"iluvatar.ai/", "iluvatar.com/"},
		ModelLabelKeys: []string{
			"iluvatar.ai/gpu.product",
			"iluvatar.ai/product",
			"iluvatar.com/gpu.product",
		},
		DriverLabelKeys: []string{
			"iluvatar.ai/gpu.driver-version",
		},
		HAMIRegisterAnnotations: []string{"hami.io/node-iluvatar-register", "hami.io/node-MR-V100-register"},
	},
	{
		ID:            "metax",
		DisplayName:   "MetaX",
		LabelPrefixes: []string{"metax-tech.com/", "metax.com/"},
		ModelLabelKeys: []string{
			"metax-tech.com/gpu.product",
			"metax-tech.com/product",
			"metax.com/gpu.product",
		},
		DriverLabelKeys: []string{
			"metax-tech.com/gpu.driver-version",
		},
	},
	{
		ID:            "huawei",
		DisplayName:   "Huawei Ascend",
		LabelPrefixes: []string{"huawei.com/"},
		ModelLabelKeys: []string{
			"huawei.com/Ascend.product",
		},
		DriverLabelKeys: []string{
			"huawei.com/npu-driver-version",
			"huawei.com/driver-version",
		},
		HAMIRegisterAnnotationPrefixes: []string{"hami.io/node-register-Ascend", "hami.io/node-register-ascend"},
	},
	{
		ID:            "cambricon",
		DisplayName:   "Cambricon",
		LabelPrefixes: []string{"cambricon.com/"},
		ModelLabelKeys: []string{
			"cambricon.com/gpu.model",
			"cambricon.com/mlu.model",
			// Cambricon's device plugin publishes unprefixed labels.
			"Model",
		},
		DriverLabelKeys: []string{
			"cambricon.com/gpu.driver-version",
			"DriverVersion",
		},
	},
	{
		ID:            "biren",
		DisplayName:   "Biren",
		LabelPrefixes: []string{"birentech.com/"},
		ModelLabelKeys: []string{
			"birentech.com/gpu.product",
		},
		DriverLabelKeys: []string{
			"birentech.com/gpu.driver-version",
		},
		HAMIRegisterAnnotations: []string{"hami.io/node-biren-register"},
	},
	{
		ID:            "enflame",
		DisplayName:   "Enflame",
		LabelPrefixes: []string{"enflame.com/"},
		ModelLabelKeys: []string{
			"enflame.com/gcu.product",
			"enflame.com/product",
		},
		DriverLabelKeys: []string{
			"enflame.com/gcu.driverVer",
			"enflame.com/gcu.driver-version",
		},
	},
	{
		ID:            "hygon",
		DisplayName:   "Hygon",
		LabelPrefixes: []string{"hygon.com/"},
		ModelLabelKeys: []string{
			"hygon.com/gpu.product",
			"hygon.com/dcu.product",
			"hygon.com/product",
		},
		DriverLabelKeys: []string{
			"hygon.com/dcu.driver-version",
		},
		HAMIRegisterAnnotations: []string{"hami.io/node-dcu-register", "hami.io/node-hcu-register"},
	},
	{
		ID:            "kunlunxin",
		DisplayName:   "Kunlunxin",
		LabelPrefixes: []string{"kunlunxin.com/"},
		ModelLabelKeys: []string{
			"kunlunxin.com/xpu.product",
			"kunlunxin.com/product",
		},
		DriverLabelKeys: []string{
			"kunlunxin.com/xpu.driver-version",
		},
		HAMIRegisterAnnotations: []string{"hami.io/node-register-xpu"},
	},
	{
		ID:            "mthreads",
		DisplayName:   "Moore Threads",
		LabelPrefixes: []string{"mthreads.com/", "mthreads.ai/"},
		ModelLabelKeys: []string{
			"mthreads.com/gpu.product",
			"mthreads.com/product",
		},
		DriverLabelKeys: []string{
			"mthreads.com/gpu.driver-version",
		},
	},
}

// VendorIDs returns the stable vendor IDs in canonical order.
func VendorIDs() []string {
	out := make([]string, 0, len(vendorProfiles))
	for _, p := range vendorProfiles {
		out = append(out, p.ID)
	}
	return out
}

// IsVendor reports whether id is a recognized accelerator vendor.
func IsVendor(id string) bool {
	_, ok := Profile(id)
	return ok
}

// Profile returns the vendor profile for id.
func Profile(id string) (VendorProfile, bool) {
	for _, p := range vendorProfiles {
		if p.ID == id {
			return p, true
		}
	}
	return VendorProfile{}, false
}

// DetectVendor identifies the accelerator vendor on a node.
//
// Precedence:
//  1. an explicit platform override (go-taas.com/accelerator, then the
//     legacy gpu-vendor label) when it names a known vendor;
//  2. the vendor operator's own node labels;
//  3. the vendor's device-plugin extended resource.
//
// It returns "" when no accelerator signal is present; the caller maps
// that to "unspecified".
func DetectVendor(labels map[string]string, resources map[corev1.ResourceName]int64) string {
	for _, key := range []string{LabelVendor, LabelLegacyVendor} {
		if v := strings.ToLower(strings.TrimSpace(labels[key])); IsVendor(v) {
			return v
		}
	}
	for _, p := range vendorProfiles {
		for _, prefix := range p.LabelPrefixes {
			for key := range labels {
				if strings.HasPrefix(key, prefix) {
					return p.ID
				}
			}
		}
	}
	for _, p := range vendorProfiles {
		for name := range resources {
			if vendor, _, ok := ClassifyResource(name); ok && vendor == p.ID {
				return p.ID
			}
		}
	}
	return ""
}

// ClassifyResource maps an accelerator extended resource to its vendor
// and device family. The family is the fallback card type when neither
// the vendor nor the resource encodes a concrete model.
func ClassifyResource(name corev1.ResourceName) (vendor, family string, ok bool) {
	key := string(name)
	type rule struct {
		prefix string
		vendor string
		family string
	}
	rules := []rule{
		{"nvidia.com/gpu", "nvidia", "gpu"},
		{"nvidia.com/mig-", "nvidia", "gpu"},
		{"amd.com/gpu", "amd", "gpu"},
		{"gpu.intel.com/i915", "intel", "gpu"},
		{"gpu.intel.com/xe", "intel", "gpu"},
		{"iluvatar.ai/gpu", "iluvatar", "gpu"},
		{"iluvatar.com/gpu", "iluvatar", "gpu"},
		{"iluvatar.ai/MR-V100", "iluvatar", "vgpu"},
		{"metax-tech.com/gpu", "metax", "gpu"},
		{"metax-tech.com/vgpu", "metax", "vgpu"},
		{"metax-tech.com/sgpu", "metax", "gpu"},
		{"metax.com/gpu", "metax", "gpu"},
		{"huawei.com/Ascend", "huawei", ""},
		{"cambricon.com/mlu", "cambricon", "mlu"},
		{"birentech.com/gpu", "biren", "gpu"},
		{"enflame.com/gcu", "enflame", "gcu"},
		{"enflame.com/vgcu", "enflame", "vgcu"},
		{"hygon.com/dcunum", "hygon", "dcu"},
		{"hygon.com/hcunum", "hygon", "hcu"},
		{"kunlunxin.com/xpu", "kunlunxin", "xpu"},
		{"kunlunxin.com/vxpu", "kunlunxin", "vxpu"},
		{"baidu.com/xpu", "kunlunxin", "xpu"},
		{"mthreads.com/gpu", "mthreads", "gpu"},
		{"mthreads.com/vgpu", "mthreads", "vgpu"},
	}
	// Intel also advertises monitoring resources with the same device
	// count; they are not schedulable cards and must not be counted twice.
	if strings.HasPrefix(key, "gpu.intel.com/i915_monitoring") ||
		strings.HasPrefix(key, "gpu.intel.com/xe_monitoring") ||
		key == "gpu.intel.com/monitoring" {
		return "", "", false
	}
	for _, r := range rules {
		if !strings.HasPrefix(key, r.prefix) {
			continue
		}
		family = r.family
		if family == "" {
			family = resourceFamilySuffix(key, r.prefix)
			if r.vendor == "huawei" && !strings.HasPrefix(family, "Ascend") {
				family = "Ascend" + family
			}
		}
		return r.vendor, family, true
	}
	return "", "", false
}

// IsAcceleratorResource reports whether a resource name is an accelerator
// extended resource.
func IsAcceleratorResource(name corev1.ResourceName) bool {
	_, _, ok := ClassifyResource(name)
	return ok
}

// resourceFamilySuffix returns the meaningful suffix after prefix, with
// per-mode qualifiers trimmed (Ascend910B4-memory -> Ascend910B4).
func resourceFamilySuffix(key, prefix string) string {
	s := strings.TrimPrefix(key, prefix)
	if i := strings.IndexAny(s, "-."); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		s = "ascend"
	}
	return s
}

// CardModelFromLabels returns the card product/model label, the vendor's
// own keys first and then the generic spellings an operator may set.
func CardModelFromLabels(vendor string, labels map[string]string) string {
	if p, ok := Profile(vendor); ok {
		for _, key := range p.ModelLabelKeys {
			if v := strings.TrimSpace(labels[key]); v != "" {
				return v
			}
		}
	}
	for _, key := range []string{LabelAcceleratorType, "gpu-product", "accelerator-type"} {
		if v := strings.TrimSpace(labels[key]); v != "" {
			return v
		}
	}
	return ""
}

// CardModelFromResource returns a card model encoded in the extended
// resource name. Most vendors put the model only in a label; Huawei puts
// it in the resource (huawei.com/Ascend910B4) and Cambricon encodes the
// MLU generation (cambricon.com/mlu370).
func CardModelFromResource(name corev1.ResourceName) string {
	key := string(name)
	const huaweiPrefix = "huawei.com/Ascend"
	if strings.HasPrefix(key, huaweiPrefix) {
		s := strings.TrimPrefix(key, huaweiPrefix)
		if i := strings.IndexAny(s, "-."); i >= 0 {
			s = s[:i]
		}
		if s == "" || strings.EqualFold(s, "Devices") {
			return ""
		}
		return "Ascend" + s
	}
	const cambriconPrefix = "cambricon.com/mlu"
	if strings.HasPrefix(key, cambriconPrefix) {
		s := strings.TrimPrefix(key, cambriconPrefix)
		if i := strings.IndexByte(s, '.'); i >= 0 {
			s = s[:i]
		}
		if s == "" {
			return ""
		}
		return "MLU" + s
	}
	return ""
}

// CardModelFromHAMI returns the card model from HAMi's node registration
// annotation, the fallback source for vendors that publish no product
// label.
func CardModelFromHAMI(vendor string, annotations map[string]string) string {
	p, ok := Profile(vendor)
	if !ok {
		return ""
	}
	keys := append([]string{}, p.HAMIRegisterAnnotations...)
	for key := range annotations {
		for _, prefix := range p.HAMIRegisterAnnotationPrefixes {
			if strings.HasPrefix(key, prefix) {
				keys = append(keys, key)
			}
		}
	}
	for _, key := range keys {
		raw := strings.TrimSpace(annotations[key])
		if raw == "" {
			continue
		}
		var devices []struct {
			Type  string `json:"type"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal([]byte(raw), &devices); err != nil {
			continue
		}
		for _, d := range devices {
			if v := strings.TrimSpace(d.Type); v != "" {
				return v
			}
			if v := strings.TrimSpace(d.Model); v != "" {
				return v
			}
		}
	}
	return ""
}

// DriverVersion returns the accelerator driver version from the labels a
// vendor operator publishes, or "".
func DriverVersion(labels map[string]string) string {
	if v := strings.TrimSpace(labels[LabelDriver]); v != "" {
		return v
	}
	for _, p := range vendorProfiles {
		for _, key := range p.DriverLabelKeys {
			if v := strings.TrimSpace(labels[key]); v != "" {
				return v
			}
		}
	}
	// NVIDIA also publishes only the version parts.
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

// nvidiaSelectorLabels keeps the NVIDIA hardware keys that are useful as
// node selectors; the other nvidia.com/* keys carry driver/plugin
// metadata (deploy flags, GFD timestamps) no selector should target.
var nvidiaSelectorLabels = map[string]struct{}{
	"nvidia.com/gpu.product":        {},
	"nvidia.com/gpu.model":          {},
	"nvidia.com/gpu.machine":        {},
	"nvidia.com/gpu.family":         {},
	"nvidia.com/gpu.memory":         {},
	"nvidia.com/gpu.present":        {},
	"nvidia.com/vgpu.present":       {},
	"nvidia.com/gpu.driver-version": {},
}

// vendorSelectorPrefixes are namespaces small enough to offer wholesale
// as node selectors.
var vendorSelectorPrefixes = []string{
	"amd.com/", "beta.amd.com/", "gpu.intel.com/", "iluvatar.ai/", "iluvatar.com/",
	"metax-tech.com/", "metax.com/", "huawei.com/", "cambricon.com/", "birentech.com/",
	"enflame.com/", "hygon.com/", "kunlunxin.com/", "mthreads.com/", "mthreads.ai/",
}

// SelectableLabel reports whether a label key is useful as a node
// selector for accelerator hardware or as an inventory fact.
func SelectableLabel(key string) bool {
	if strings.HasPrefix(key, "go-taas.com/") || key == LabelLegacyVendor {
		return true
	}
	if _, ok := nvidiaSelectorLabels[key]; ok {
		return true
	}
	if strings.HasPrefix(key, "nvidia.com/cuda.driver-version.") {
		return true
	}
	for _, prefix := range vendorSelectorPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	switch key {
	case LabelAcceleratorType, "gpu-product", "accelerator-type":
		return true
	default:
		return false
	}
}
