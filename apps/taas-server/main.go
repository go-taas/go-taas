// Command taas-server runs the unified control-plane gRPC server hosting
// all microservices (auth, model, image, infer, metering, billing) behind
// one gRPC endpoint, with the grpc-gateway HTTP frontend and the monitor
// port besides it.
package main

import (
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"

	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/billing"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/metering"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

var _, _, _ = version, commit, buildTime

func main() {
	args := (&config.Arguments{
		ServicePort: 9090,
		MonitorPort: 9092,
		GatewayPort: 9091,
		ConfigPath:  config.DefaultConfigPath(),
	}).Parse()

	config.ParseConfigs(args.ConfigPath)
	cfg := config.GetConfig()
	if err := logger.Init(logger.Config{
		Level:    server.LogLevelFromConfig(cfg.Log.Level),
		Encoding: cfg.Log.Encoding,
	}); err != nil {
		panic(err)
	}

	srv, err := server.NewServer(&server.Options{
		Name:                 "taas-server",
		BindingHost:          args.BindingHost,
		ServicePort:          args.ServicePort,
		MonitorPort:          args.MonitorPort,
		GatewayPort:          args.GatewayPort,
		EnableGateway:        true,
		EnableComponentDB:    !args.NoDatabase,
		EnableComponentRedis: true,
		EnableComponentMQ:    true,
		ConfigPath:           args.ConfigPath,
	})
	if err != nil {
		logger.S().Fatalw("build server failed", "err", err)
	}

	// The tenancy service is registered first so its Migrate (which
	// creates the organizations/projects tables and seeds the default
	// organization) runs before the consumers' migrates — order is only
	// tidy, not functionally required (feature #6).
	srv.RegisterService(tenancy.New(srv.Components()))
	authSvc := auth.New(srv.Components())
	srv.RegisterService(authSvc)
	modelSvc := model.New(srv.Components())
	srv.RegisterService(modelSvc)
	imageSvc := image.New(srv.Components())
	srv.RegisterService(imageSvc)
	inferSvc := infer.New(srv.Components())
	srv.RegisterService(inferSvc)
	meteringSvc := metering.New(srv.Components())
	srv.RegisterService(meteringSvc)
	billingSvc := billing.New(srv.Components())
	srv.RegisterService(billingSvc)

	// The delete-model and delete-image reference guards need the infer
	// repository; wire them after both services are registered (AC3,
	// feature #3 D7). The components are only available after Init, so
	// both wirings happen there.
	srv.Init()

	if dbComponent := srv.Components().DB(); dbComponent != nil {
		if gormDB, ok := dbComponent.GormDB().(*gorm.DB); ok {
			// The tenancy OrgGuard validates the transitional organization
			// context of the org-scoped APIs (feature #6, FR3).
			orgGuard := tenancy.NewOrgGuard(gormDB)
			authSvc.SetOrgGuard(orgGuard)
			inferSvc.SetOrgGuard(orgGuard)
			meteringSvc.SetOrgGuard(orgGuard)
			billingSvc.SetOrgGuard(orgGuard)
			// The delete-model and delete-image reference guards need the
			// infer repository (AC3, feature #3 D7).
			modelSvc.SetDeleteGuard(infer.NewDeleteModelGuard(gormDB))
			imageSvc.SetDeleteGuard(infer.NewDeleteImageGuard(gormDB))
			imageSvc.SetInUseProvider(infer.NewImageInUseProvider(gormDB))
		}
	}
	// The SSO session store is Redis-backed (feature #7). Wire it from
	// the Redis component so the auth service can issue and resolve
	// sessions.
	if redisComponent := srv.Components().Redis(); redisComponent != nil {
		if client, ok := redisComponent.Client().(*goredis.Client); ok {
			authSvc.SetSessionStore(auth.NewSessionStore(client, cfg.Auth.SessionTTL))
		}
	}
	if runner := infer.NewStatusConsumerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := image.NewWarmupStatusConsumerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := metering.NewEventConsumerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := metering.NewSettlementRunnerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := metering.NewRetentionRunnerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := billing.NewEventConsumerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := billing.NewSettlementsConsumerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := billing.NewReconciliationRunnerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}

	srv.Serve()
}
