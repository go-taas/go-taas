package finetuning

import (
	"encoding/json"

	"github.com/google/uuid"
)

// newUUID returns a fresh UUID v4 string for a primary key.
func newUUID() string { return uuid.NewString() }

// jsonMarshal serializes a value to JSON bytes.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }