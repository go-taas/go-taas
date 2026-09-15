// Package metering implements the metering service: ingesting token
// usage events from the data plane and serving voucher queries.
package metering

import (
	"context"

	"google.golang.org/grpc"

	meteringv1 "github.com/go-taas/go-taas/proto/taas/metering/v1"

	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "metering"

// Service implements the metering gRPC service.
type Service struct {
	meteringv1.UnimplementedMeteringServiceServer

	components server.Components
}

// New constructs the metering service.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	meteringv1.RegisterMeteringServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// IngestMeteringEvent records one token-usage event. It is called by the
// gateway's Wasm plugin through the message queue (or directly in
// tests).
func (s *Service) IngestMeteringEvent(_ context.Context, _ *meteringv1.IngestMeteringEventRequest) (*meteringv1.IngestMeteringEventResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "metering: not implemented")
}

// ListVouchers returns usage vouchers aggregated by period.
func (s *Service) ListVouchers(_ context.Context, _ *meteringv1.ListVouchersRequest) (*meteringv1.ListVouchersResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "metering: not implemented")
}
