package optimization

import (
	"crypto/sha256"
	"encoding/hex"
)

// Fingerprint derives the stable identity of one computed
// recommendation: sha256(rule | target_type | target_id | suggestion)
// hex (design D4). The suggestion participates so a changed suggestion
// for the same target is a new condition, not a silent mutation; the
// estimated saving deliberately does not participate so a recomputed
// (drifting) saving keeps the identity — the upsert then preserves
// recommendation_id and first_seen_at (FR1.3).
func Fingerprint(rule, targetType, targetID, suggestion string) string {
	h := sha256.New()
	h.Write([]byte(rule))
	h.Write([]byte{'|'})
	h.Write([]byte(targetType))
	h.Write([]byte{'|'})
	h.Write([]byte(targetID))
	h.Write([]byte{'|'})
	h.Write([]byte(suggestion))
	return hex.EncodeToString(h.Sum(nil))
}
