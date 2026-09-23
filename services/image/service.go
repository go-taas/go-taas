// Package image implements the inference engine image registry service:
// registering engine images and triggering warmup on GPU nodes.
package image

import (
	"context"
	"math"

	"google.golang.org/grpc"

	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

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

// RegisterImage registers an inference engine image. The transitional
// in-memory registry is read-only (seeded from configuration); writable
// registration ships with feature #3.
func (s *Service) RegisterImage(_ context.Context, _ *imagev1.RegisterImageRequest) (*imagev1.RegisterImageResponse, error) {
	return nil, errors.Newf(errors.CodeImageExists, "image: registration not implemented (feature #3)")
}

// ListImages returns registered images, optionally filtered by the
// accelerator and engine they support.
func (s *Service) ListImages(_ context.Context, req *imagev1.ListImagesRequest) (*imagev1.ListImagesResponse, error) {
	entries := List(req.GetAccelerator(), req.GetEngine())
	images := make([]*imagev1.ImageSummary, 0, len(entries))
	for _, e := range entries {
		images = append(images, &imagev1.ImageSummary{
			ImageId:     e.ImageID,
			Name:        e.Name,
			Tag:         e.Tag,
			Accelerator: e.Accelerator,
			Engine:      e.Engine,
		})
	}
	return &imagev1.ListImagesResponse{
		Response: okResponse(),
		Images:   images,
		PageMeta: &commonv1.PageMeta{Total: int64(len(images)), Offset: 0, Limit: clampToInt32(len(images))},
	}, nil
}

// clampToInt32 bounds v to the int32 range proto fields accept.
func clampToInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < 0 {
		return 0
	}
	return int32(v)
}

// TriggerWarmup asks the controller to pre-pull an image on nodes
// matching the selector. The controller side stays a stub until
// feature #3.
func (s *Service) TriggerWarmup(_ context.Context, req *imagev1.TriggerWarmupRequest) (*imagev1.TriggerWarmupResponse, error) {
	if _, err := Lookup(req.GetImageId()); err != nil {
		return nil, err
	}
	return nil, errors.Newf(errors.CodeImageWarmupFailed, "image: warmup not implemented (feature #3)")
}

// okResponse builds the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
