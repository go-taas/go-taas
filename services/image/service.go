// Package image implements the inference engine image registry service:
// registering engine images and triggering warmup on GPU nodes.
package image

import (
	"context"

	"google.golang.org/grpc"

	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"

	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "image"

// Service implements the image registry gRPC service.
type Service struct {
	imagev1.UnimplementedImageServiceServer

	components server.Components
}

// New constructs the image registry service.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	imagev1.RegisterImageServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return imagev1.RegisterImageServiceHandler
}

// RegisterImage registers an inference engine image.
func (s *Service) RegisterImage(_ context.Context, _ *imagev1.RegisterImageRequest) (*imagev1.RegisterImageResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "image: not implemented")
}

// ListImages returns registered images, optionally filtered by the
// accelerator type they support.
func (s *Service) ListImages(_ context.Context, _ *imagev1.ListImagesRequest) (*imagev1.ListImagesResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "image: not implemented")
}

// TriggerWarmup asks the controller to pre-pull an image on nodes
// matching the selector.
func (s *Service) TriggerWarmup(_ context.Context, _ *imagev1.TriggerWarmupRequest) (*imagev1.TriggerWarmupResponse, error) {
	return nil, errors.Newf(errors.CodeImageNotFound, "image: not implemented")
}
