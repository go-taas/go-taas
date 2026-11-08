package infer

import (
	"encoding/json"
	"regexp"
	"strings"

	"gorm.io/datatypes"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Environment-variable handling for inference services. An operator can set
// variables the serving engine reads at startup (VLLM_USE_V1,
// CUDA_DEVICE_MAX_CONNECTIONS, HF_HOME, ...) on every topology. They are
// applied to every engine container of the service; a per-role entry
// overrides one variable for that role only.

const (
	// maxEnvVars bounds one service's variable count. A form offering a
	// handful of engine variables is far below it; the bound exists so a
	// script cannot build an unbounded pod spec.
	maxEnvVars = 64
	// maxEnvNameLen bounds one variable name.
	maxEnvNameLen = 128
	// maxEnvValueLen bounds one variable value. Values may hold a JSON
	// config or a long path list, so this is generous compared with a flag.
	maxEnvValueLen = 4096
)

// envNamePattern accepts a POSIX-style environment variable name. The engine
// and the shell only read names in this shape; anything else is silently
// inert, so it is rejected instead of stored.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnvNames are set by Kubernetes or by the AIBrix storm controller and
// carry the pod's identity. Overriding them breaks the distributed rendezvous
// (the rank is derived from HOSTNAME and ROLESET_INDEX), so a request naming
// one is refused instead of producing a service that cannot start.
var reservedEnvNames = map[string]string{
	"HOSTNAME":           "the pod hostname is used to derive the distributed rank",
	"POD_NAME":           "the pod name is assigned by Kubernetes",
	"POD_NAMESPACE":      "the pod namespace is assigned by Kubernetes",
	"POD_IP":             "the pod IP is assigned by Kubernetes",
	"NODE_NAME":          "the node name is assigned by Kubernetes",
	"STORM_SERVICE_NAME": "the storm name is assigned by the AIBrix controller",
	"ROLESET_NAME":       "the role set name is assigned by the AIBrix controller",
	"ROLESET_INDEX":      "the role set index is what the engine uses as its rank",
	"ROLE_NAME":          "the role name is assigned by the AIBrix controller",
	"ROLE_TEMPLATE_HASH": "the role template hash is assigned by the AIBrix controller",
}

// normalizeEnv validates the environment-variable map and returns it as the
// JSON the service stores. A nil/empty map stores {}.
//
// An empty value is preserved: unlike an engine flag, `FOO=` is a meaningful
// setting (it clears an inherited variable). A row without a name is ignored,
// which is how the console represents a row the operator has not filled in
// yet.
func normalizeEnv(env map[string]string) (datatypes.JSON, error) {
	if len(env) > maxEnvVars {
		return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
			"infer: at most %d environment variables are allowed", maxEnvVars)
	}
	out := make(map[string]string, len(env))
	for rawName, rawValue := range env {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		if len(name) > maxEnvNameLen || !envNamePattern.MatchString(name) {
			return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
				"infer: environment variable %q must start with a letter or underscore and contain only letters, digits and underscores", rawName)
		}
		if reason, reserved := reservedEnvNames[name]; reserved {
			return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
				"infer: environment variable %q is reserved: %s", name, reason)
		}
		if len(rawValue) > maxEnvValueLen {
			return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
				"infer: environment variable %q is longer than %d characters", name, maxEnvValueLen)
		}
		if strings.ContainsRune(rawValue, '\x00') {
			return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
				"infer: environment variable %q contains a NUL byte", name)
		}
		out[name] = rawValue
	}
	if len(out) == 0 {
		return datatypes.JSON("{}"), nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, apierrors.New(apierrors.CodeInferEngineArgsInvalid)
	}
	return datatypes.JSON(encoded), nil
}

// EnvMap decodes the stored environment variables into the map the change
// event carries. A malformed or absent column yields nil.
func EnvMap(stored datatypes.JSON) map[string]string {
	if len(stored) == 0 {
		return nil
	}
	out := map[string]string{}
	if err := json.Unmarshal(stored, &out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}
