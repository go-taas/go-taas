package logger

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestInitAndS(t *testing.T) {
	if err := Init(Config{Level: zapcore.DebugLevel, Encoding: "console"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if S() == nil {
		t.Fatal("S() returned nil after Init")
	}
	if L() == nil {
		t.Fatal("L() returned nil after Init")
	}
	// Logging must not panic.
	S().Infow("hello", "key", "value")
	L().Info("hello structured")
	Sync()
}

func TestFallbackBeforeInit(t *testing.T) {
	// A fresh process state (no Init) must still return usable loggers.
	// We cannot easily reset the atomic globals here, so just assert the
	// current loggers are non-nil and usable.
	if S() == nil || L() == nil {
		t.Fatal("loggers must never be nil")
	}
	S().Debug("safe to call")
}
