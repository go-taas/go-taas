// RDMA device requests for inference pods.
//
// A model sharded across machines, or split into prefill and decode roles,
// exchanges tensors and KV cache between pods. Over TCP that interconnect is
// the bottleneck: the engine spends more time moving activations than
// computing them. Every modern GPU node therefore carries one or more
// RDMA-capable NICs (RoCE or InfiniBand), and the engine uses them through
// NCCL when the pod can see the device.
//
// Kubernetes exposes such devices through DRA (Dynamic Resource Allocation):
// a DeviceClass names the kind of device, a ResourceClaimTemplate describes a
// request, and every pod created from that template gets its own allocation.
// The controller writes one template per service (so the device count is a
// service property) and the pods reference it; the chart owns the
// DeviceClass, because which devices a fleet offers is a cluster fact rather
// than a per-service one.

package infer

import (
	"strings"

	"gorm.io/datatypes"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

const (
	// defaultRdmaDevices is one device per pod: a single dual-port card, or
	// one rail of a rail-optimized pair. It is what an operator who only
	// ticks "RDMA" expects.
	defaultRdmaDevices = 1
	// maxRdmaDevices bounds one pod's request. A full node with one NIC per
	// GPU is the widest real layout, so 8 is the ceiling; the bound exists
	// to catch a typo before the scheduler sees an impossible request.
	maxRdmaDevices = 8
	// maxRdmaDeviceClassLen bounds a DeviceClass name. Kubernetes object
	// names are DNS subdomains, which is at most 253 characters.
	maxRdmaDeviceClassLen = 253
)

// RdmaSpec is a normalized RDMA device request.
type RdmaSpec struct {
	// Enabled is false for a service that asked for nothing. Devices and
	// DeviceClass are then zero values and the controller creates no claim.
	Enabled bool
	// Devices is the number of devices one pod requests (>= 1 when enabled).
	Devices int
	// DeviceClass is the DRA DeviceClass the devices come from. Empty means
	// "the platform default", which the controller fills in from its own
	// configuration.
	DeviceClass string
}

// NormalizeRdma validates an RDMA request and fills in the defaults. A nil
// request, or one with enabled=false, is the "no RDMA" case: the device count
// and class are dropped so a disabled request can never leak a claim onto the
// pods.
func NormalizeRdma(req *inferv1.RdmaConfig) (RdmaSpec, error) {
	if req == nil || !req.GetEnabled() {
		return RdmaSpec{}, nil
	}
	devices := int(req.GetDevices())
	if devices == 0 {
		devices = defaultRdmaDevices
	}
	if devices < 1 || devices > maxRdmaDevices {
		return RdmaSpec{}, apierrors.Newf(apierrors.CodeInferRdmaInvalid,
			"infer: rdma.devices must be 1-%d", maxRdmaDevices)
	}
	class := strings.TrimSpace(req.GetDeviceClass())
	if len(class) > maxRdmaDeviceClassLen {
		return RdmaSpec{}, apierrors.Newf(apierrors.CodeInferRdmaInvalid,
			"infer: rdma.device_class must be at most %d characters", maxRdmaDeviceClassLen)
	}
	if class != "" && !isDNSSubdomain(class) {
		return RdmaSpec{}, apierrors.Newf(apierrors.CodeInferRdmaInvalid,
			"infer: rdma.device_class %q must be a DNS subdomain (lowercase letters, digits, '-' and '.')", class)
	}
	return RdmaSpec{Enabled: true, Devices: devices, DeviceClass: class}, nil
}

// isDNSSubdomain reports whether s is a valid Kubernetes DNS subdomain, the
// shape a DeviceClass name must have.
func isDNSSubdomain(s string) bool {
	if s == "" || len(s) > maxRdmaDeviceClassLen {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" || len(part) > 63 {
			return false
		}
		for i, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			case r == '-':
				// A label must start and end with an alphanumeric.
				if i == 0 || i == len(part)-1 {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// protoRdma projects a stored spec onto the wire message. A service without
// RDMA returns a disabled message rather than nil, so a reader never has to
// distinguish "absent" from "off".
func protoRdma(spec RdmaSpec) *inferv1.RdmaConfig {
	return &inferv1.RdmaConfig{
		Enabled:     spec.Enabled,
		Devices:     clampToInt32(spec.Devices),
		DeviceClass: spec.DeviceClass,
	}
}

// rdmaSpecFromRow rebuilds the normalized spec from the stored columns.
func rdmaSpecFromRow(row *InferenceService) RdmaSpec {
	if !row.RdmaEnabled {
		return RdmaSpec{}
	}
	return RdmaSpec{
		Enabled:     true,
		Devices:     row.RdmaDevices,
		DeviceClass: row.RdmaDeviceClass,
	}
}

// ncclIBDisabledVar is the variable NCCL reads to skip InfiniBand entirely.
// Setting it on an RDMA-enabled service is a silent downgrade: the engine
// still starts, the devices are still allocated, and every transfer falls back
// to TCP without an error anywhere. It is the one environment variable the
// platform refuses to combine with an RDMA request.
const ncclIBDisabledVar = "NCCL_IB_DISABLE"

// validateRdmaEnv refuses an RDMA request whose environment variables disable
// the fabric. Everything else about the two settings is independent: RDMA
// allocates devices, env configures the engine, and only this pair is
// contradictory.
func validateRdmaEnv(rdma RdmaSpec, env datatypes.JSON, roleArgs datatypes.JSON) error {
	if !rdma.Enabled {
		return nil
	}
	if truthyEnvValue(EnvMap(env)[ncclIBDisabledVar]) {
		return apierrors.Newf(apierrors.CodeInferRdmaInvalid,
			"infer: %s=%s disables RDMA, so the requested devices would never be used; remove the variable or disable rdma",
			ncclIBDisabledVar, EnvMap(env)[ncclIBDisabledVar])
	}
	for _, role := range RoleEngineArgsList(roleArgs) {
		if truthyEnvValue(role.Env[ncclIBDisabledVar]) {
			return apierrors.Newf(apierrors.CodeInferRdmaInvalid,
				"infer: role %q sets %s=%s, which disables RDMA for that role; remove the variable or disable rdma",
				role.Role, ncclIBDisabledVar, role.Env[ncclIBDisabledVar])
		}
	}
	return nil
}

// truthyEnvValue reports whether an environment value asks for "on": NCCL
// reads the variable as a boolean, and an empty or zero value means "off".
func truthyEnvValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
