package cluster

import (
	"github.com/google/uuid"
)

// newUUID returns a fresh UUID v4 string for a primary key.
func newUUID() string { return uuid.NewString() }