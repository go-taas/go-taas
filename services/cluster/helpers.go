package cluster

import (
	"strings"

	"github.com/google/uuid"
)

// newUUID returns a fresh UUID v4 string for a primary key.
func newUUID() string { return uuid.NewString() }

// splitLabels splits the comma-separated accelerator label cache into a
// slice, dropping empty entries.
func splitLabels(joined string) []string {
	if joined == "" {
		return nil
	}
	parts := strings.Split(joined, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
