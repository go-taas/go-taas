// Package logger provides structured logging for the platform.
//
// It wraps zap to provide a project-wide logging abstraction. All code
// should use logger.S() (or logger.L()) instead of calling zap.S() /
// zap.L() directly, so the underlying sink and level stay configurable.
//
// Usage:
//
//	logger.S().Infow("message", "key", value)
//	logger.S().With("trace_id", tid).Info("message")
//	logger.L().Info("message", zap.String("key", value))
package logger

import (
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// globalLogger is the global SugaredLogger.
	globalLogger atomic.Pointer[zap.SugaredLogger]
	// globalZapLogger is the global structured Logger.
	globalZapLogger atomic.Pointer[zap.Logger]
)

// Config holds logger configuration.
type Config struct {
	// Level is the minimum log level (default: Info).
	Level zapcore.Level
	// Development mode uses more human-friendly output (default: false).
	Development bool
	// Encoding format: "json" or "console" (default: "console").
	Encoding string
}

// DefaultConfig returns the default logger configuration.
func DefaultConfig() Config {
	return Config{
		Level:       zapcore.InfoLevel,
		Development: false,
		Encoding:    "console",
	}
}

// Init initializes the global logger with the given configuration.
// This should be called once at application startup; it is safe to call
// again (e.g. after loading configuration) to re-initialize.
func Init(cfg Config) error {
	zapCfg := zap.Config{
		Level:             zap.NewAtomicLevelAt(cfg.Level),
		Development:       cfg.Development,
		Encoding:          cfg.Encoding,
		EncoderConfig:     zap.NewProductionEncoderConfig(),
		OutputPaths:       []string{"stderr"},
		ErrorOutputPaths:  []string{"stderr"},
		DisableStacktrace: true,
	}
	if cfg.Development || cfg.Encoding == "console" {
		zapCfg.EncoderConfig = zap.NewDevelopmentEncoderConfig()
	}

	l, err := zapCfg.Build()
	if err != nil {
		return err
	}

	globalZapLogger.Store(l)
	globalLogger.Store(l.Sugar())
	return nil
}

// L returns the global structured logger. It falls back to a nop logger
// when Init has not been called yet, so early startup code can log safely.
func L() *zap.Logger {
	if l := globalZapLogger.Load(); l != nil {
		return l
	}
	return zap.NewNop()
}

// S returns the global sugared logger. It falls back to a nop logger
// when Init has not been called yet.
func S() *zap.SugaredLogger {
	if s := globalLogger.Load(); s != nil {
		return s
	}
	return zap.NewNop().Sugar()
}

// Sync flushes any buffered log entries. Safe to call multiple times.
func Sync() {
	if s := globalLogger.Load(); s != nil {
		_ = s.Sync()
	}
}
