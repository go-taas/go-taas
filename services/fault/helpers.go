// Package fault implements the fault-diagnosis and automatic fault
// tolerance module (feature #53): the incident lifecycle, the
// deterministic root-cause classifier, the recovery actions, and the
// per-category auto-recovery policy.
package fault

import (
	"encoding/json"

	"github.com/google/uuid"
)

// newUUID returns a new UUID v4 string.
func newUUID() string {
	return uuid.NewString()
}

// jsonUnmarshalMap decodes a JSON object into a bool map. It returns
// the unmarshal error (best-effort callers ignore it).
func jsonUnmarshalMap(raw []byte, out map[string]bool) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, &out)
}