package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/go-taas/go-taas/pkg/config"
	grpcmiddleware "github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/logger"
)

// Server is the common implementation of a running gRPC server.
type Server interface {
	// RegisterService attaches a service to the gRPC server (and gateway
	// when the service implements ServiceWithGateway).
	RegisterService(svc Service)

	// AddRunner registers a background task started with the server.
	AddRunner(runner Runner)

	// Init initializes all components. It must be called after all
	// services are registered and before Serve.
	Init()

	// Serve blocks until a shutdown signal arrives or a component fails.
	Serve()

	// Components returns the shared platform components.
	Components() Components
}

type commonServer struct {
	opts Options

	grpcServer   *grpc.Server
	gatewayMux   *runtime.ServeMux
	monitorMux   *http.ServeMux
	healthServer *healthServer

	services []Service
	runners  []Runner

	components *components
}

// NewServer constructs a Server from opts.
func NewServer(opts *Options) (Server, error) {
	if opts.Name == "" {
		return nil, errors.New("server: name is required")
	}
	if opts.ServicePort <= 0 {
		return nil, fmt.Errorf("server: invalid service port %d", opts.ServicePort)
	}
	if opts.MonitorPort <= 0 {
		return nil, fmt.Errorf("server: invalid monitor port %d", opts.MonitorPort)
	}
	if opts.EnableGateway && opts.GatewayPort <= 0 {
		return nil, fmt.Errorf("server: invalid gateway port %d", opts.GatewayPort)
	}
	// Create the components shell up front so Components() returns a
	// usable interface even before Init: services constructed with
	// srv.Components() before Init would otherwise capture a non-nil
	// interface wrapping a nil pointer that panics on first use. Init
	// populates the shell's fields in place.
	return &commonServer{opts: *opts, components: &components{}}, nil
}

// RegisterService implements Server.
func (s *commonServer) RegisterService(svc Service) {
	s.services = append(s.services, svc)
}

// AddRunner implements Server.
func (s *commonServer) AddRunner(runner Runner) {
	s.runners = append(s.runners, runner)
}

// Components implements Server.
func (s *commonServer) Components() Components {
	return s.components
}

// Init implements Server.
func (s *commonServer) Init() {
	cfg := config.GetConfig()

	// Initialize shared components. The shell was created in NewServer;
	// populate its fields in place so references captured earlier stay
	// valid.
	initialized := newComponents(cfg, s.opts)
	s.components.cfg = initialized.cfg
	s.components.db = initialized.db
	s.components.redis = initialized.redis
	s.components.mq = initialized.mq

	// Run schema migrations for services that implement Migrator, right
	// after components initialization. A migration failure aborts
	// startup: serving against a missing or drifted schema would
	// silently corrupt state.
	for _, svc := range s.services {
		m, ok := svc.(Migrator)
		if !ok {
			continue
		}
		if err := m.Migrate(context.Background()); err != nil {
			logger.S().Fatalw("service migration failed", "service", svc.ServiceName(), "err", err)
		}
		logger.S().Infow("service migrated", "service", svc.ServiceName())
	}

	// Build the gRPC server with the standard interceptor chain.
	interceptors := append(
		[]grpc.UnaryServerInterceptor{grpcmiddleware.UnaryServerInterceptor()},
		s.opts.UnaryInterceptors...,
	)
	s.grpcServer = grpc.NewServer(grpc.ChainUnaryInterceptor(interceptors...))
	grpc_prometheus.Register(s.grpcServer)

	// Health and reflection come first so probes work even before all
	// services are attached.
	s.healthServer = newHealthServer()
	grpc_health_v1.RegisterHealthServer(s.grpcServer, s.healthServer)
	reflection.Register(s.grpcServer)

	// Attach registered services.
	for _, svc := range s.services {
		svc.AttachToServer(s.grpcServer)
		logger.S().Infow("service attached", "service", svc.ServiceName())
	}

	// Build the gateway.
	if s.opts.EnableGateway {
		s.gatewayMux = runtime.NewServeMux(
			runtime.WithErrorHandler(gatewayErrorHandler),
			runtime.WithIncomingHeaderMatcher(incomingHeaderMatcher),
		)
		s.initGateway()
	}

	// Build the monitor server (metrics + health + pprof).
	s.initMonitor()
}

// initGateway dials the local gRPC server and registers every
// gateway-capable service.
func (s *commonServer) initGateway() {
	ctx := context.Background()
	conn, err := grpc.NewClient(
		fmt.Sprintf("%s:%d", s.opts.BindingHostOrDefault(), s.opts.ServicePort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		logger.S().Panicf("gateway dial failed: %v", err)
	}

	for _, svc := range s.services {
		gw, ok := svc.(ServiceWithGateway)
		if !ok {
			continue
		}
		if err := gw.GetServiceHandlerRegisterFn()(ctx, s.gatewayMux, conn); err != nil {
			logger.S().Panicf("gateway register %s failed: %v", svc.ServiceName(), err)
		}
	}
}

// initMonitor wires the metrics, health and pprof endpoints.
func (s *commonServer) initMonitor() {
	s.monitorMux = http.NewServeMux()

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		grpc_prometheus.DefaultServerMetrics,
	)
	for _, svc := range s.services {
		if c, ok := svc.(Collector); ok {
			registry.MustRegister(c.Collectors()...)
		}
	}
	s.monitorMux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	s.monitorMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.monitorMux.HandleFunc("/debug/pprof/", pprof.Index)
	s.monitorMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	s.monitorMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

// Serve implements Server. It runs the gRPC server, the gateway and the
// monitor server until a shutdown signal arrives.
func (s *commonServer) Serve() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	group, ctx := errgroup.WithContext(ctx)

	// gRPC server.
	group.Go(func() error {
		lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", s.opts.BindingHostOrDefault(), s.opts.ServicePort))
		if err != nil {
			return fmt.Errorf("grpc listen: %w", err)
		}
		logger.S().Infow("grpc server listening", "addr", lis.Addr().String())
		if err := s.grpcServer.Serve(lis); err != nil {
			return fmt.Errorf("grpc serve: %w", err)
		}
		return nil
	})

	// Gateway server.
	if s.opts.EnableGateway {
		group.Go(func() error {
			server := &http.Server{
				Addr:              fmt.Sprintf("%s:%d", s.opts.BindingHostOrDefault(), s.opts.GatewayPort),
				Handler:           s.gatewayMux,
				ReadHeaderTimeout: 10 * time.Second,
			}
			logger.S().Infow("gateway listening", "addr", server.Addr)
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("gateway serve: %w", err)
			}
			return nil
		})
	}

	// Monitor server.
	group.Go(func() error {
		server := &http.Server{
			Addr:              fmt.Sprintf("%s:%d", s.opts.BindingHostOrDefault(), s.opts.MonitorPort),
			Handler:           s.monitorMux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		logger.S().Infow("monitor listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("monitor serve: %w", err)
		}
		return nil
	})

	// Background runners.
	for _, r := range s.runners {
		runner := r
		group.Go(func() error {
			if err := runner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.S().Errorw("runner exited", "err", err)
				return err
			}
			return nil
		})
	}

	// Mark serving once everything is up.
	s.healthServer.setServing()

	err := group.Wait()
	s.shutdown()
	if err != nil {
		logger.S().Fatalw("server exited with error", "err", err)
	}
	logger.S().Info("server stopped")
}

// shutdown gracefully stops the gRPC server and closes components.
func (s *commonServer) shutdown() {
	s.grpcServer.GracefulStop()
	if s.components != nil {
		s.components.Close()
	}
	logger.Sync()
}

// BindingHostOrDefault returns the configured binding host, defaulting to
// all interfaces.
func (o *Options) BindingHostOrDefault() string {
	if o.BindingHost == "" {
		return "0.0.0.0"
	}
	return o.BindingHost
}

// LogLevelFromConfig maps a textual log level to a zapcore level.
func LogLevelFromConfig(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
