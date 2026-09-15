package server

import (
	"context"
	"sync/atomic"

	"google.golang.org/grpc/health/grpc_health_v1"
)

// healthServer implements the standard gRPC health protocol. It reports
// SERVING only after the server finished startup, so orchestrators
// (Kubernetes probes, load balancers) do not route traffic to a process
// that is still initializing.
type healthServer struct {
	grpc_health_v1.UnimplementedHealthServer

	serving atomic.Bool
}

func newHealthServer() *healthServer {
	h := &healthServer{}
	h.serving.Store(false)
	return h
}

func (h *healthServer) setServing() {
	h.serving.Store(true)
}

// Check implements grpc_health_v1.HealthServer.
func (h *healthServer) Check(_ context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	status := grpc_health_v1.HealthCheckResponse_NOT_SERVING
	if h.serving.Load() {
		status = grpc_health_v1.HealthCheckResponse_SERVING
	}
	return &grpc_health_v1.HealthCheckResponse{Status: status}, nil
}

// Watch implements grpc_health_v1.HealthServer.
func (h *healthServer) Watch(_ *grpc_health_v1.HealthCheckRequest, w grpc_health_v1.Health_WatchServer) error {
	status := grpc_health_v1.HealthCheckResponse_NOT_SERVING
	if h.serving.Load() {
		status = grpc_health_v1.HealthCheckResponse_SERVING
	}
	// Send the current status once; streaming updates are not needed for
	// the platform's probe patterns.
	return w.Send(&grpc_health_v1.HealthCheckResponse{Status: status})
}
