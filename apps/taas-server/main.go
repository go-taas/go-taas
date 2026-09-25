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

	"github.com/go-taas/go-taas/services/audit"
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
	tenancySvc := tenancy.New(srv.Components())
	srv.RegisterService(tenancySvc)
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
	auditSvc := audit.New(srv.Components())
	srv.RegisterService(auditSvc)

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
			// The model service validates the organization a grant names
			// (feature #13, AC1).
			modelSvc.SetOrgGuard(orgGuard)
			// Per-tenant model authorization (feature #13): the model
			// repository is the single default-allow check shared by the
			// control plane (infer) and the data plane (auth, AD5), and the
			// auth service resolves the granting caller for granted_by
			// (AD8). The data-plane verdict cache is a Redis one, wired
			// lazily from the Redis component with model.auth.cacheTTL
			// (AD6).
			authSvc.SetModelAuthorizer(model.NewRepository(gormDB))
			modelSvc.SetSessionResolver(authSvc)
			// Console surface separation (feature-17 AD6): the session's
			// active org wins over X-Organization-Id on the user-realm
			// reads of model/infer/metering/billing.
			modelSvc.SetSessionOrgResolver(authSvc)
			inferSvc.SetSessionOrgResolver(authSvc)
			meteringSvc.SetSessionOrgResolver(authSvc)
			billingSvc.SetSessionOrgResolver(authSvc)
			// The per-request cost attributor reads billing's
			// price_entries over the shared database (feature #9, AD3).
			meteringSvc.SetCostAttributor(metering.NewCostAttributor(gormDB))
			// The membership RoleGuard gates member/invitation management
			// (feature #10, AD7); the SessionResolver lets the tenancy
			// service resolve the authenticated caller (feature #10).
			tenancySvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			tenancySvc.SetSessionResolver(authSvc)
			// The audit service (feature #15) resolves the session's
			// active org and caller for the admin/end-user audit reads,
			// and gates the admin audit RPCs by the caller's role.
			auditSvc.SetSessionOrgResolver(authSvc)
			auditSvc.SetSessionUserResolver(authSvc)
			auditSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			// The auth service records key revokes and logins into the
			// audit trail best-effort (feature #15, AC1/AC3).
			auditRecorder := audit.NewRecorder(audit.NewRepository(gormDB))
			authSvc.SetAuditRecorder(auditRecorder)
			// The other mutating services record their control-plane
			// mutations into the same trail best-effort (feature #15,
			// FR1.2).
			modelSvc.SetAuditRecorder(auditRecorder)
			inferSvc.SetAuditRecorder(auditRecorder)
			billingSvc.SetAuditRecorder(auditRecorder)
			imageSvc.SetAuditRecorder(auditRecorder)
			tenancySvc.SetAuditRecorder(auditRecorder)
			// The auth session derives roles/accessible orgs from
			// org_members (feature #10, AD2/AD11).
			authSvc.SetMembershipResolver(tenancy.NewMembershipResolver(gormDB))
			// The delete-model and delete-image reference guards need the
			// infer repository (AC3, feature #3 D7).
			modelSvc.SetDeleteGuard(infer.NewDeleteModelGuard(gormDB))
			imageSvc.SetDeleteGuard(infer.NewDeleteImageGuard(gormDB))
			imageSvc.SetInUseProvider(infer.NewImageInUseProvider(gormDB))
			// Feature #16: the user-realm catalog's read-only autoscaling
			// projection is resolved from the infer module's service
			// status (AD13).
			modelSvc.SetAutoscalingProvider(infer.NewModelAutoscalingProvider(gormDB))
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
	if runner := infer.NewConcurrencyConsumerRunner(srv.Components()); runner != nil {
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
	if runner := metering.NewRequestLogRetentionRunnerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := audit.NewAuditRetentionRunnerRunner(srv.Components()); runner != nil {
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
	if runner := billing.NewCycleResetRunnerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	// Feature-14 auto-recharge runner: tops up prepaid accounts whose
	// balance falls below their threshold (bounded by the daily cap).
	if cfg.Billing.AutoRecharge.Enabled {
		srv.AddRunner(billing.NewAutoRechargeRunner(billingSvc, cfg.Billing.AutoRecharge.Interval))
	}

	srv.Serve()
}
