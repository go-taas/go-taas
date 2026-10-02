// Package docs implements the curated user-realm API catalog served by
// the API documentation explorer (feature #38). It serves the end-user
// API Docs page (GetApiDocs), returning a static, versioned projection
// of the endpoints a tenant/Agent can call.
package docs

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	docsv1 "github.com/go-taas/go-taas/proto/taas/docs/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ServiceName is the unique name of this service.
const ServiceName = "docs"

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization, set by the gateway from the
// X-Organization-Id HTTP header (the auth module's pattern).
const organizationMetadataKey = "x-organization-id"

// SessionOrgResolver resolves the session's active organization
// (feature-17 AD6). It is implemented by the auth module and injected at
// wiring time. Nil until wired: the transitional X-Organization-Id
// header is used.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id from
// the session (feature #10). It is implemented by the auth module and
// injected at wiring time.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the docs RPC. It is
// implemented by tenancy.RoleGuard.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

// roleMember is the minimum org role for the docs RPC (AD1): the
// catalog is a user-realm surface. It must be a key of the tenancy
// roleRank map (RoleMember), so a non-member session is rejected with
// 10036.
const roleMember = tenancy.RoleMember

// Service implements the docs gRPC service.
type Service struct {
	docsv1.UnimplementedDocsServiceServer

	// catalogVersion is the version of the curated catalog (AD3, AD7).
	catalogVersion string

	// sessionOrgResolver resolves the session's active organization
	// (feature-17 AD6). Nil until wired: the transitional
	// X-Organization-Id header is used.
	sessionOrgResolver SessionOrgResolver

	// sessionUserResolver resolves the caller's user id for the role
	// check (feature #10). Nil until wired: no role check.
	sessionUserResolver SessionUserResolver

	// roleGuard gates the docs RPC by the caller's role (AD1). Nil until
	// wired: no role check (unit tests).
	roleGuard RoleGuard
}

// New constructs the docs service.
func New() *Service {
	return &Service{catalogVersion: catalogVersion}
}

// SetCatalogVersion overrides the served catalog version. It is used by
// tests and FVT.
func (s *Service) SetCatalogVersion(v string) { s.catalogVersion = v }

// SetSessionOrgResolver injects the session-organization resolver used
// by the user binding (feature-17 AD6).
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver used by the
// role check (feature #10).
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard that gates the docs RPC by the
// caller's role (AD1).
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// NewForFVT constructs a docs service for full-verification tests.
func NewForFVT() *Service {
	return &Service{catalogVersion: catalogVersion}
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	docsv1.RegisterDocsServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return docsv1.RegisterDocsServiceHandler
}

// Migrate implements server.Migrator: the docs feature is a pure read of
// a static catalog, so there is nothing to migrate.
func (s *Service) Migrate(_ context.Context) error { return nil }

// resolveOrg returns the organization context for a read (feature-17
// AD6): the session's active org when a session is present, otherwise
// the transitional X-Organization-Id header.
func (s *Service) resolveOrg(ctx context.Context) (string, error) {
	if s.sessionOrgResolver != nil {
		if org, err := s.sessionOrgResolver.SessionActiveOrg(ctx); err != nil {
			return "", err
		} else if org != "" {
			return org, nil
		}
	}
	return resolveOrganizationID(ctx)
}

// resolveOrganizationID reads the transitional caller organization from
// the x-organization-id gRPC metadata. Missing or empty values are
// unauthorized (the auth module's pattern).
func resolveOrganizationID(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	values := md.Get(organizationMetadataKey)
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	return strings.TrimSpace(values[0]), nil
}

// resolveSessionActor returns the caller's user id and whether a session
// is present.
func (s *Service) resolveSessionActor(ctx context.Context) (string, bool, error) {
	if s.sessionUserResolver == nil {
		return "", false, nil
	}
	userID, err := s.sessionUserResolver.SessionUserID(ctx)
	if err == nil && userID != "" {
		return userID, true, nil
	}
	if err != nil && apierrors.CodeOf(err) != apierrors.CodeSessionInvalid {
		return "", false, err
	}
	return "", false, nil
}

// requireUserRole enforces the minimum org role on the docs RPC (AD1).
// The RoleGuard resolves the caller from a session; in the transitional
// (session-less) path there is no session user to check, so the check is
// skipped and the org header itself is the access boundary (feature-17
// AD6).
func (s *Service) requireUserRole(ctx context.Context, orgID string) error {
	if s.roleGuard == nil || orgID == "" {
		return nil
	}
	userID, hasSession, err := s.resolveSessionActor(ctx)
	if err != nil {
		return err
	}
	if !hasSession {
		return nil
	}
	return s.roleGuard.RequireRole(ctx, orgID, userID, roleMember)
}

// GetApiDocs returns the curated user-realm API catalog (AC1, AC2). It
// is end-user-only (AD1). The name matches the proto RPC (GetApiDocs),
// so the revive APIDocs suggestion is suppressed.
func (s *Service) GetApiDocs(ctx context.Context, _ *docsv1.GetApiDocsRequest) (*docsv1.GetApiDocsResponse, error) { //nolint:revive // GetApiDocs is the proto RPC name
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireUserRole(ctx, orgID); err != nil {
		return nil, err
	}
	return &docsv1.GetApiDocsResponse{
		Response:   okResponse(),
		Categories: catalog(s.catalogVersion),
	}, nil
}

// okResponse returns the standard success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}