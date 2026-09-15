// Package billing implements the billing service: price management,
// balances and bills.
package billing

import (
	"context"

	"google.golang.org/grpc"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"

	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "billing"

// Service implements the billing gRPC service.
type Service struct {
	billingv1.UnimplementedBillingServiceServer

	components server.Components
}

// New constructs the billing service.
func New(components server.Components) *Service {
	return &Service{components: components}
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	billingv1.RegisterBillingServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return billingv1.RegisterBillingServiceHandler
}

// SetPrice creates or updates a price entry of the model x accelerator
// matrix.
func (s *Service) SetPrice(_ context.Context, _ *billingv1.SetPriceRequest) (*billingv1.SetPriceResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "billing: not implemented")
}

// ListPrices returns the price matrix.
func (s *Service) ListPrices(_ context.Context, _ *billingv1.ListPricesRequest) (*billingv1.ListPricesResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "billing: not implemented")
}

// GetBalance returns the prepaid balance or postpaid credit limit of an
// organization.
func (s *Service) GetBalance(_ context.Context, _ *billingv1.GetBalanceRequest) (*billingv1.GetBalanceResponse, error) {
	return nil, errors.Newf(errors.CodeAccountNotFound, "billing: not implemented")
}

// ListBills returns the bills of an organization.
func (s *Service) ListBills(_ context.Context, _ *billingv1.ListBillsRequest) (*billingv1.ListBillsResponse, error) {
	return nil, errors.Newf(errors.CodeInternal, "billing: not implemented")
}
