// Package server provides the common implementation of a running gRPC
// server with an optional grpc-gateway HTTP frontend and a metrics/health
// monitor port. All platform binaries embed their services into this
// framework.
package server

import (
	"context"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
)

// Service represents an implemented gRPC service.
type Service interface {
	// AttachToServer registers the service implementation on the gRPC server.
	AttachToServer(grpcServer *grpc.Server)

	// ServiceName returns the unique service name used in logs and metrics.
	ServiceName() string
}

// ServiceHandlerRegisterFn is the grpc-gateway generated function that
// registers an HTTP handler on a started gateway mux.
type ServiceHandlerRegisterFn func(ctx context.Context, mux *runtime.ServeMux, conn *grpc.ClientConn) error

// ServiceWithGateway is implemented by services that also expose an
// HTTP/JSON facade through the control-plane gateway.
type ServiceWithGateway interface {
	Service

	// GetServiceHandlerRegisterFn returns the grpc-gateway generated
	// service handler register function.
	GetServiceHandlerRegisterFn() ServiceHandlerRegisterFn
}

// Runner is a background task started with the server and cancelled on
// shutdown.
type Runner interface {
	Run(ctx context.Context) error
}

// Options configures a Server.
type Options struct {
	// Name identifies the server in logs.
	Name string

	// BindingHost is the host the gRPC and gateway servers bind to.
	BindingHost string
	// ServicePort is the gRPC serving port.
	ServicePort int
	// MonitorPort is the HTTP port for metrics, health and pprof.
	MonitorPort int
	// GatewayPort is the HTTP port of the grpc-gateway frontend.
	GatewayPort int

	// EnableGateway turns on the grpc-gateway HTTP frontend.
	EnableGateway bool

	// EnableComponentDB initializes the PostgreSQL component.
	EnableComponentDB bool
	// EnableComponentRedis initializes the Redis component.
	EnableComponentRedis bool
	// EnableComponentMQ initializes the message-queue component.
	EnableComponentMQ bool

	// ConfigPath is the glob pattern of configuration files.
	ConfigPath string

	// UnaryInterceptors are chained in front of the built-in ones.
	UnaryInterceptors []grpc.UnaryServerInterceptor
}

// Components gives services access to shared platform components.
type Components interface {
	// DB returns the PostgreSQL handle, or nil when the DB component is
	// disabled.
	DB() DBComponent
	// Redis returns the Redis handle, or nil when the Redis component is
	// disabled.
	Redis() RedisComponent
	// MQ returns the message-queue client, or nil when the MQ component is
	// disabled.
	MQ() MQComponent
}

// DBComponent is the database surface exposed to services.
type DBComponent interface {
	// GormDB returns the underlying *gorm.DB.
	GormDB() any
}

// RedisComponent is the Redis surface exposed to services.
type RedisComponent interface {
	// Client returns the underlying *redis.Client.
	Client() any
}

// MQComponent is the message-queue surface exposed to services.
type MQComponent interface {
	// Publish sends a message to a subject.
	Publish(ctx context.Context, subject string, body []byte, headers map[string]string) error
}

// Collector is implemented by services that export custom Prometheus
// metrics.
type Collector interface {
	Collectors() []prometheus.Collector
}

// Migrator is implemented by services that own database schema. Init
// invokes Migrate for every registered service implementing it, right
// after components initialization; an error aborts startup. The GORM
// model is the single source of truth for the schema (AutoMigrate), so
// no hand-written DDL is kept.
type Migrator interface {
	// Migrate brings the service's schema up to date.
	Migrate(ctx context.Context) error
}
