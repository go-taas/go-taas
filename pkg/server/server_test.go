package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestNewServerValidation(t *testing.T) {
	_, err := NewServer(&Options{})
	assert.Error(t, err, "empty name must be rejected")

	_, err = NewServer(&Options{Name: "test"})
	assert.Error(t, err, "missing service port must be rejected")

	_, err = NewServer(&Options{Name: "test", ServicePort: 1})
	assert.Error(t, err, "missing monitor port must be rejected")

	_, err = NewServer(&Options{Name: "test", ServicePort: 1, MonitorPort: 2, EnableGateway: true})
	assert.Error(t, err, "enabled gateway without port must be rejected")

	srv, err := NewServer(&Options{Name: "test", ServicePort: 1, MonitorPort: 2})
	require.NoError(t, err)
	assert.NotNil(t, srv)
}

func TestHealthServerServing(t *testing.T) {
	h := newHealthServer()

	resp, err := h.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING, resp.Status)

	h.setServing()
	resp, err = h.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, resp.Status)
}

// stubService is a minimal Service used to exercise registration.
type stubService struct{}

func (s *stubService) AttachToServer(_ *grpc.Server) {}
func (s *stubService) ServiceName() string           { return "stub" }

func TestRegisterServiceAndRunner(t *testing.T) {
	srv, err := NewServer(&Options{Name: "test", ServicePort: 1, MonitorPort: 2})
	require.NoError(t, err)

	srv.RegisterService(&stubService{})
	srv.AddRunner(runnerFunc(func(_ context.Context) error { return nil }))

	cs := srv.(*commonServer)
	assert.Len(t, cs.services, 1)
	assert.Len(t, cs.runners, 1)
	assert.Equal(t, "stub", cs.services[0].ServiceName())
}

type runnerFunc func(ctx context.Context) error

func (f runnerFunc) Run(ctx context.Context) error { return f(ctx) }

func TestBindingHostOrDefault(t *testing.T) {
	assert.Equal(t, "0.0.0.0", (&Options{}).BindingHostOrDefault())
	assert.Equal(t, "127.0.0.1", (&Options{BindingHost: "127.0.0.1"}).BindingHostOrDefault())
}

func TestLogLevelFromConfig(t *testing.T) {
	assert.Equal(t, "debug", levelString(LogLevelFromConfig("debug")))
	assert.Equal(t, "info", levelString(LogLevelFromConfig("")))
	assert.Equal(t, "info", levelString(LogLevelFromConfig("unknown")))
	assert.Equal(t, "warn", levelString(LogLevelFromConfig("warn")))
	assert.Equal(t, "error", levelString(LogLevelFromConfig("error")))
}

func levelString(l zapcore.Level) string {
	switch l {
	case zapcore.DebugLevel:
		return "debug"
	case zapcore.InfoLevel:
		return "info"
	case zapcore.WarnLevel:
		return "warn"
	case zapcore.ErrorLevel:
		return "error"
	default:
		return "other"
	}
}
