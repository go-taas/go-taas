// Package model implements the model registry service: registering
// models and their versions, and serving registry queries to the rest of
// the control plane.
package model

import (
	"context"

	"google.golang.org/grpc"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"

	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "model"

// Service implements the model registry gRPC service.
type Service struct {
	modelv1.UnimplementedModelServiceServer

	components server.Components
}

// New constructs the model registry service.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	modelv1.RegisterModelServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return modelv1.RegisterModelServiceHandler
}

// RegisterModel registers a new model with its first version.
func (s *Service) RegisterModel(_ context.Context, _ *modelv1.RegisterModelRequest) (*modelv1.RegisterModelResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "model: not implemented")
}

// ListModels returns the models visible to the caller.
func (s *Service) ListModels(_ context.Context, _ *modelv1.ListModelsRequest) (*modelv1.ListModelsResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "model: not implemented")
}

// GetModel returns one model with its versions.
func (s *Service) GetModel(_ context.Context, _ *modelv1.GetModelRequest) (*modelv1.GetModelResponse, error) {
	return nil, errors.Newf(errors.CodeModelNotFound, "model: not implemented")
}

// DeleteModel removes a model from the registry.
func (s *Service) DeleteModel(_ context.Context, _ *modelv1.DeleteModelRequest) (*modelv1.DeleteModelResponse, error) {
	return nil, errors.Newf(errors.CodeModelNotFound, "model: not implemented")
}
