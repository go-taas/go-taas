// Package auth implements the authentication and authorization service:
// local accounts, API keys and the gateway-facing key verification used
// to authenticate inference traffic.
package auth

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "auth"

// Service implements the auth gRPC service.
type Service struct {
	authv1.UnimplementedAuthServiceServer

	components server.Components
}

// New constructs the auth service.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	authv1.RegisterAuthServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return authv1.RegisterAuthServiceHandler
}

// CreateUser registers a local account.
func (s *Service) CreateUser(_ context.Context, _ *authv1.CreateUserRequest) (*authv1.CreateUserResponse, error) {
	return nil, errors.Newf(errors.CodeUnauthorized, "auth: not implemented")
}

// Login authenticates a local account and issues a session token.
func (s *Service) Login(_ context.Context, _ *authv1.LoginRequest) (*authv1.LoginResponse, error) {
	return nil, errors.Newf(errors.CodeUnauthorized, "auth: not implemented")
}

// ListAPIKeys returns the API keys of the caller's organization.
func (s *Service) ListAPIKeys(_ context.Context, _ *authv1.ListAPIKeysRequest) (*authv1.ListAPIKeysResponse, error) {
	return nil, errors.Newf(errors.CodeUnauthorized, "auth: not implemented")
}

// CreateAPIKey issues a new API key. The plaintext key is returned
// exactly once; only its salted hash is stored.
func (s *Service) CreateAPIKey(_ context.Context, _ *authv1.CreateAPIKeyRequest) (*authv1.CreateAPIKeyResponse, error) {
	return nil, errors.Newf(errors.CodeUnauthorized, "auth: not implemented")
}

// RevokeAPIKey revokes an API key. Revocation takes effect after the
// gateway-side cache TTL expires.
func (s *Service) RevokeAPIKey(_ context.Context, _ *authv1.RevokeAPIKeyRequest) (*authv1.RevokeAPIKeyResponse, error) {
	return nil, errors.Newf(errors.CodeUnauthorized, "auth: not implemented")
}

// VerifyAPIKey authenticates an inference request by its API key digest.
// It is called by the gateway on the data-plane request path.
func (s *Service) VerifyAPIKey(_ context.Context, req *authv1.VerifyAPIKeyRequest) (*authv1.VerifyAPIKeyResponse, error) {
	if req.GetKeyDigest() == "" {
		return nil, status.Error(codes.InvalidArgument, "auth: key digest is required")
	}
	// TODO: look up the key digest in the cache (Redis) first, then in
	// the database, and return the organization, key id and role bound
	// to the key.
	return nil, errors.Newf(errors.CodeAPIKeyNotFound, "auth: not implemented")
}
