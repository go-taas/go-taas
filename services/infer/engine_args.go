package infer

import (
	"encoding/json"
	"regexp"
	"strings"

	"gorm.io/datatypes"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Serving-engine parameter handling (feature: engine parameters). The
// console offers the common flags of the chosen engine and lets the
// operator add the rest by hand; the controller turns the stored map into
// the container command line.

const (
	// maxEngineArgs bounds one service's parameter count. A form that
	// offers ~30 flags plus custom rows is far below it; the bound exists
	// so a script cannot build an unbounded command line.
	maxEngineArgs = 64
	// maxEngineArgLen bounds one flag name and one value.
	maxEngineArgLen = 512
	// maxExtraArgs bounds the free-form argument list.
	maxExtraArgs = 64
)

// engineArgNamePattern accepts an engine flag without its leading dashes:
// lowercase letters, digits, hyphens and underscores ("max-model-len",
// "enable_prefix_caching").
var engineArgNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

// normalizeEngineArgs validates the engine parameter map and returns it as
// the JSON the service stores. A nil/empty map stores {}.
func normalizeEngineArgs(args map[string]string) (datatypes.JSON, error) {
	if len(args) > maxEngineArgs {
		return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
			"infer: at most %d engine arguments are allowed", maxEngineArgs)
	}
	out := make(map[string]string, len(args))
	for rawName, rawValue := range args {
		name := strings.TrimSpace(rawName)
		value := strings.TrimSpace(rawValue)
		if !engineArgNamePattern.MatchString(name) {
			return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
				"infer: engine argument %q must be lowercase letters, digits, hyphens or underscores", rawName)
		}
		if len(value) > maxEngineArgLen {
			return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
				"infer: engine argument %q is longer than %d characters", name, maxEngineArgLen)
		}
		if value == "" {
			continue
		}
		out[name] = value
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

// normalizeExtraArgs validates the free-form argument list and returns it
// as the JSON the service stores. A nil/empty list stores [].
func normalizeExtraArgs(args []string) (datatypes.JSON, error) {
	if len(args) > maxExtraArgs {
		return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
			"infer: at most %d extra arguments are allowed", maxExtraArgs)
	}
	out := make([]string, 0, len(args))
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		if arg == "" {
			continue
		}
		if len(arg) > maxEngineArgLen {
			return nil, apierrors.Newf(apierrors.CodeInferEngineArgsInvalid,
				"infer: extra argument %q is longer than %d characters", arg, maxEngineArgLen)
		}
		if strings.ContainsAny(arg, "\n\r") {
			return nil, apierrors.New(apierrors.CodeInferEngineArgsInvalid)
		}
		out = append(out, arg)
	}
	if len(out) == 0 {
		return datatypes.JSON("[]"), nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, apierrors.New(apierrors.CodeInferEngineArgsInvalid)
	}
	return datatypes.JSON(encoded), nil
}

// EngineArgsMap decodes the stored engine parameters into the map the
// change event carries. A malformed or absent column yields nil.
func EngineArgsMap(stored datatypes.JSON) map[string]string {
	if len(stored) == 0 {
		return nil
	}
	out := map[string]string{}
	if err := json.Unmarshal(stored, &out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}

// ExtraArgsList decodes the stored extra arguments. A malformed or absent
// column yields nil.
func ExtraArgsList(stored datatypes.JSON) []string {
	if len(stored) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(stored, &out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}
