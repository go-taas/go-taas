package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/go-taas/go-taas/pkg/config"
)

// TestInitDBWithRetryGivesUpAfterTheWindow keeps a genuinely broken
// configuration visible instead of retrying forever: startup has to fail
// loudly, not hang.
func TestInitDBWithRetryGivesUpAfterTheWindow(t *testing.T) {
	cfg := &config.DBConfig{Host: "127.0.0.1", Port: 1, User: "x", DBName: "x"}
	start := time.Now()
	_, err := initDBWithRetry(cfg, 0)
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "a zero window must not retry")
}
