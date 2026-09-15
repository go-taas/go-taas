// Package infer implements the inference service lifecycle API: creating,
// scaling and deleting inference services. Desired-state changes are
// published to the message queue; the controller applies them to the
// cluster.
package infer

import (
	"context"

	"google.golang.org/grpc"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "infer"

// Service implements the inference service lifecycle gRPC service.
type Service struct {
	inferv1.UnimplementedInferServiceServiceServer

	components server.Components
}

// New constructs the inference service.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	inferv1.RegisterInferServiceServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return inferv1.RegisterInferServiceServiceHandler
}

// CreateInferenceService deploys a model with the given image and
// replicas. The change is published to the message queue and applied by
// the controller.
func (s *Service) CreateInferenceService(_ context.Context, _ *inferv1.CreateInferenceServiceRequest) (*inferv1.CreateInferenceServiceResponse, error) {
	// TODO: persist the desired state, then publish a change event on
	// subjects.InferServiceChanges for the controller.
	return nil, errors.Newf(errors.CodeInternal, "infer: not implemented")
}

// ListInferenceServices returns the inference services of the caller's
// organization.
func (s *Service) ListInferenceServices(_ context.Context, _ *inferv1.ListInferenceServicesRequest) (*inferv1.ListInferenceServicesResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "infer: not implemented")
}

// GetInferenceService returns one inference service with its status.
func (s *Service) GetInferenceService(_ context.Context, _ *inferv1.GetInferenceServiceRequest) (*inferv1.GetInferenceServiceResponse, error) {
	return nil, errors.Newf(errors.CodeInferServiceNotFound, "infer: not implemented")
}

// ScaleInferenceService changes the replica count of an inference
// service.
func (s *Service) ScaleInferenceService(_ context.Context, _ *inferv1.ScaleInferenceServiceRequest) (*inferv1.ScaleInferenceServiceResponse, error) {
	return nil, errors.Newf(errors.CodeInferServiceNotFound, "infer: not implemented")
}

// DeleteInferenceService removes an inference service.
func (s *Service) DeleteInferenceService(_ context.Context, _ *inferv1.DeleteInferenceServiceRequest) (*inferv1.DeleteInferenceServiceResponse, error) {
	return nil, errors.Newf(errors.CodeInferServiceNotFound, "infer: not implemented")
}
