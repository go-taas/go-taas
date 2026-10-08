// Command taas-server runs the unified control-plane gRPC server hosting
// all microservices (auth, model, image, infer, metering, billing) behind
// one gRPC endpoint, with the grpc-gateway HTTP frontend and the monitor
// port besides it.
package main

import (
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/k8s"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/modelhub"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/registry"
	"github.com/go-taas/go-taas/pkg/server"

	"github.com/go-taas/go-taas/internal/controller"
	"github.com/go-taas/go-taas/services/accelerator"
	"github.com/go-taas/go-taas/services/account"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/batch"
	"github.com/go-taas/go-taas/services/billing"
	"github.com/go-taas/go-taas/services/cluster"
	"github.com/go-taas/go-taas/services/docs"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/metering"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/notification"
	"github.com/go-taas/go-taas/services/observability"
	"github.com/go-taas/go-taas/services/prompt"
	"github.com/go-taas/go-taas/services/resourcemetrics"
	"github.com/go-taas/go-taas/services/tenancy"
	"github.com/go-taas/go-taas/services/tracing"
	"github.com/go-taas/go-taas/services/webhook"
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
	// Feature: model download from a model hub (ModelScope/HuggingFace).
	// The downloader writes into the JuiceFS-mounted weights directory so
	// inference pods read the same weights. Empty weightsDir disables the
	// feature.
	if cfg.Model.WeightsDir != "" {
		modelSvc.SetWeightsDir(cfg.Model.WeightsDir)
		modelSvc.SetModelDownloader(modelhub.NewHTTPDownloader(modelhub.Options{}))
	}
	imageSvc := image.New(srv.Components())
	srv.RegisterService(imageSvc)
	// Feature: image import. The importer copies an engine image from a
	// source registry into the internal Harbor project. It is wired
	// whenever the Harbor registry is configured.
	if cfg.Image.Harbor.URL != "" {
		imageSvc.SetImporter(registry.NewSkopeoImporter())
	}
	inferSvc := infer.New(srv.Components())
	srv.RegisterService(inferSvc)
	meteringSvc := metering.New(srv.Components())
	srv.RegisterService(meteringSvc)
	billingSvc := billing.New(srv.Components())
	srv.RegisterService(billingSvc)
	auditSvc := audit.New(srv.Components())
	srv.RegisterService(auditSvc)
	// Feature #23: the webhook service owns outbound webhook endpoints,
	// event subscription, HMAC signing, retry and the delivery log.
	webhookSvc := webhook.New(srv.Components())
	srv.RegisterService(webhookSvc)
	// Feature #24: the observability service serves the read-only model
	// performance aggregation over request_logs (admin fleet view and
	// dual-bound single-model drill-down).
	observabilitySvc := observability.New(srv.Components())
	observabilitySvc.SetMaxRangeSeconds(cfg.Observability.MaxRangeSeconds)
	srv.RegisterService(observabilitySvc)
	// Feature #26: the notification service owns the in-console
	// notification center: notifications, per-user preferences and
	// threshold alerts, plus the event consumer and retention runner.
	notificationSvc := notification.New(srv.Components())
	srv.RegisterService(notificationSvc)
	// Feature #27: the tracing service owns the traces + trace_spans
	// tables, the best-effort capture alongside the request log, and the
	// read-only query RPCs (ListTraces, GetTrace) dual-bound to the admin
	// and user surfaces.
	tracingSvc := tracing.New(srv.Components())
	srv.RegisterService(tracingSvc)
	// Feature #18: the accelerator inventory service serves the read-only
	// fleet view from its in-memory projection cache. The cache is
	// constructed once and shared with the snapshot consumer so the
	// consumer's Replace() populates the same cache the RPCs read
	// (BUG-ACCEL-001).
	acceleratorCache := accelerator.NewProjectionCache()
	acceleratorSvc := accelerator.NewWithCache(acceleratorCache)
	srv.RegisterService(acceleratorSvc)
	// Feature #37: the resourcemetrics service serves the read-only
	// per-service CPU/memory/GPU utilization aggregation over the
	// service_resource_metrics table (admin-only).
	resourceMetricsSvc := resourcemetrics.New(srv.Components())
	resourceMetricsSvc.SetMaxRangeSeconds(cfg.ResourceMetrics.MaxRangeSeconds)
	srv.RegisterService(resourceMetricsSvc)
	// Feature #38: the docs service serves the curated user-realm API
	// catalog for the API documentation explorer (end-user-only).
	docsSvc := docs.New()
	docsSvc.SetCatalogVersion(cfg.Docs.CatalogVersion)
	srv.RegisterService(docsSvc)
	// Feature #40: the cluster service owns the cluster registry and the
	// cluster-health projection (admin-only).
	clusterCache := cluster.NewProjectionCache()
	clusterSvc := cluster.New(srv.Components())
	clusterSvc.SetDefaultClusterID(cfg.Cluster.DefaultClusterID)
	srv.RegisterService(clusterSvc)
	// Feature #41: the account service owns the data-export job lifecycle
	// and the account-data bundle (end-user-only).
	accountSvc := account.New(srv.Components())
	accountSvc.SetMaxRangeSeconds(cfg.Account.Export.MaxRangeSeconds)
	srv.RegisterService(accountSvc)
	// Feature #42: the batch service owns the batch-job lifecycle, the
	// JSONL input/output file store, the JSONL validator, the batch
	// worker, and the retention runner.
	batchSvc := batch.New(srv.Components())
	srv.RegisterService(batchSvc)
	// Feature #43: the prompt service owns the prompt lifecycle,
	// versioning, folders, usage counters, and the shared template
	// library.
	promptSvc := prompt.New(srv.Components())
	srv.RegisterService(promptSvc)

	// Feature #20: the async load-test runner is constructed once the
	// database is available (it persists runs and drives real traffic
	// through the synthetic platform credential, AD11/AD12) and is
	// registered as a server runner below.
	var loadTestRunner *infer.LoadTestRunner

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
			// Feature #32: the model service resolves the session's
			// active org and caller, and gates the admin model-version
			// RPCs by the caller's role (AD).
			modelSvc.SetSessionUserResolver(authSvc)
			modelSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
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
			// Features #32/#33/#34/#35: the infer service resolves the
			// session's active org and caller, and gates the admin
			// model-version/service-log/deployment and the user-realm
			// compare RPCs by the caller's role (AD).
			inferSvc.SetSessionUserResolver(authSvc)
			inferSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			// Feature #25: the billing service resolves the session's
			// active org and caller, and gates the admin billing-report
			// RPCs by the caller's role (AD6).
			billingSvc.SetSessionUserResolver(authSvc)
			billingSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
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
			// Feature #23: the webhook service resolves the session's
			// active org and caller, gates the admin webhook RPCs by the
			// caller's role, and records webhook mutations into the audit
			// trail (AD12).
			webhookSvc.SetSessionOrgResolver(authSvc)
			webhookSvc.SetSessionUserResolver(authSvc)
			webhookSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			// Feature #24: the observability service resolves the
			// session's active org and caller, and gates the admin
			// observability RPCs by the caller's role (AD4).
			observabilitySvc.SetSessionOrgResolver(authSvc)
			observabilitySvc.SetSessionUserResolver(authSvc)
			observabilitySvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			// Feature #26: the notification service resolves the
			// session's active org and caller, gates the admin
			// notification RPCs by the caller's role, and records
			// notification mutations into the audit trail (AD12).
			notificationSvc.SetSessionOrgResolver(authSvc)
			notificationSvc.SetSessionUserResolver(authSvc)
			notificationSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			// Feature #27: the tracing service resolves the session's
			// active org and caller, gates the admin tracing RPCs by the
			// caller's role (AD9), and the metering service captures the
			// trace best-effort alongside the request log (AD3).
			tracingSvc.SetSessionOrgResolver(authSvc)
			tracingSvc.SetSessionUserResolver(authSvc)
			tracingSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			meteringSvc.SetTraceCapturer(tracingSvc)
			// Feature #37: the resourcemetrics service resolves the
			// session's active org and caller, gates the admin
			// resource-metrics RPC by the caller's role (AD1), and
			// validates the service_id against the infer service
			// contract (10301).
			resourceMetricsSvc.SetSessionOrgResolver(authSvc)
			resourceMetricsSvc.SetSessionUserResolver(authSvc)
			resourceMetricsSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			resourceMetricsSvc.SetServiceExists(infer.NewServiceExistsProvider(gormDB))
			// Feature #38: the docs service resolves the session's
			// active org and caller, and gates the docs RPC by the
			// caller's role (AD1).
			docsSvc.SetSessionOrgResolver(authSvc)
			docsSvc.SetSessionUserResolver(authSvc)
			docsSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			// Feature #40: the cluster service resolves the session's
			// active org and caller, gates the admin cluster RPCs by the
			// caller's role (AD1), and lists workload placement via the
			// infer module (AD7).
			clusterSvc.SetSessionOrgResolver(authSvc)
			clusterSvc.SetSessionUserResolver(authSvc)
			clusterSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			clusterSvc.SetWorkloadProvider(infer.NewClusterWorkloadProvider(gormDB))
			// Feature #41: the account service resolves the session's
			// active org and caller, and gates the data-export RPCs by
			// the caller's role (AD1).
			accountSvc.SetSessionOrgResolver(authSvc)
			accountSvc.SetSessionUserResolver(authSvc)
			accountSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			// Feature #42: the batch service resolves the session's
			// active org and caller, gates the admin batch RPCs by the
			// caller's role, and records batch actions into the audit
			// trail (AD9).
			batchSvc.SetSessionOrgResolver(authSvc)
			batchSvc.SetSessionUserResolver(authSvc)
			batchSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
			// Feature #43: the prompt service resolves the session's
			// active org and caller, gates the admin prompt RPCs by the
			// caller's role, and records prompt actions into the audit
			// trail (AD9).
			promptSvc.SetSessionOrgResolver(authSvc)
			promptSvc.SetSessionUserResolver(authSvc)
			promptSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))

			// Feature #28/#31: the metering service resolves the session's
			// active org and caller, and gates the admin usage-keys and
			// error-analysis RPCs by the caller's role (AD9).
			meteringSvc.SetSessionUserResolver(authSvc)
			meteringSvc.SetRoleGuard(tenancy.NewRoleGuard(gormDB))
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
			webhookSvc.SetAuditRecorder(auditRecorder)
			notificationSvc.SetAuditRecorder(auditRecorder)
			batchSvc.SetAuditRecorder(auditRecorder)
			promptSvc.SetAuditRecorder(auditRecorder)

			// The auth session derives roles/accessible orgs from
			// org_members (feature #10, AD2/AD11).
			authSvc.SetMembershipResolver(tenancy.NewMembershipResolver(gormDB))
			// The delete-model and delete-image reference guards need the
			// infer repository (AC3, feature #3 D7).
			modelSvc.SetDeleteGuard(infer.NewDeleteModelGuard(gormDB))
			imageSvc.SetDeleteGuard(infer.NewDeleteImageGuard(gormDB))
			imageSvc.SetInUseProvider(infer.NewImageInUseProvider(gormDB))
			// Feature #18: the accelerator service reads warmup-task
			// context for a node's detail view through the image
			// module's narrow provider.
			acceleratorSvc.SetWarmupProvider(image.NewWarmupTasksForNodeProvider(gormDB))
			// Feature #16: the user-realm catalog's read-only autoscaling
			// projection is resolved from the infer module's service
			// status (AD13).
			modelSvc.SetAutoscalingProvider(infer.NewModelAutoscalingProvider(gormDB))
			// Feature #19: the compatibility matrix. The image module
			// reads the live card-type set from the accelerator
			// inventory (AD11), the infer module enforces the matrix at
			// deploy time (AD13), and the model module's masked catalog
			// gains the compatibility summary (AD12).
			imageSvc.SetCardTypesProvider(accelerator.NewCardTypesProvider(acceleratorCache))
			imageSvc.SetCompatibilityLazyDefault(cfg.Image.Compatibility.LazySeedDefault)
			imageSvc.SetSessionOrgResolver(authSvc)
			inferSvc.SetCompatibilityChecker(image.NewCompatibilityChecker(imageSvc))
			modelSvc.SetCompatibilityProvider(image.NewModelCompatibilitySummaryProvider(imageSvc))
			// Feature #33: the service-logs viewer reads container logs
			// from the Kubernetes API through the Controller. The server
			// builds its own k8s client from the controller config so the
			// log RPCs work in a single-process topology; when no
			// kubeconfig is configured the log RPCs fail closed (10301).
			if cfg.Controller.Kubeconfig != "" {
				if k8sClient, k8sErr := k8s.NewClient(&k8s.Config{Kubeconfig: cfg.Controller.Kubeconfig}); k8sErr == nil {
					inferSvc.SetLogFetcher(controller.NewLogFetcher(k8sClient, cfg.Controller.Namespace))
				}
			}
			// Feature #20: the load-test runner persists runs and drives
			// real inference traffic with the synthetic platform
			// credential the auth module seeds (AD11/AD12). The runner is
			// a server.Runner, registered below. A disabled kill switch
			// leaves the runner unwired, so CreateLoadTest fails closed
			// (10311) rather than leaving a pending row with no executor.
			if cfg.LoadTest.SystemCredentialEnabled {
				loadTestRunner = infer.NewLoadTestRunner(
					infer.NewLoadTestRepository(gormDB),
					authSvc,
					cfg.LoadTest.ProgressInterval,
				)
				inferSvc.SetLoadTestRunner(loadTestRunner)
				inferSvc.SetSystemCredentialProvider(authSvc)
			}
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
	if runner := accelerator.NewSnapshotConsumerRunner(srv.Components(), acceleratorCache); runner != nil {
		srv.AddRunner(runner)
	}
	// Feature #40: the cluster health snapshot consumer maintains the
	// in-memory projection cache the cluster RPCs read (AD4).
	if runner := cluster.NewSnapshotConsumerRunner(srv.Components(), clusterCache); runner != nil {
		srv.AddRunner(runner)
	}
	// Feature #20: the load-test runner recovers interrupted runs and
	// executes queued load tests.
	if loadTestRunner != nil {
		srv.AddRunner(loadTestRunner)
	}
	// Feature #45: the routing-policy outbox publisher delivers
	// committed policy revisions to the inference gateway adapter on
	// the dedicated infer.routing.policies subject (AD5). It is wired
	// after Init so the database and MQ components are available; a
	// missing MQ component leaves pending outbox rows durable (the
	// publication state stays pending) rather than falsely marking
	// them published.
	if dbComponent := srv.Components().DB(); dbComponent != nil {
		if gormDB, ok := dbComponent.GormDB().(*gorm.DB); ok {
			if mqComponent := srv.Components().MQ(); mqComponent != nil {
				if mqClient, ok := mqComponent.Client().(mq.Client); ok {
					srv.AddRunner(infer.NewRoutingPolicyPublisher(
						infer.NewRoutingPolicyRepository(gormDB),
						mqClient,
						0,
					))
				}
			}
		}
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
	// Feature #23: the webhook event consumer, delivery runner and
	// retention runner.
	if runner := webhook.NewEventConsumerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := webhook.NewDeliveryRunnerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := webhook.NewWebhookRetentionRunnerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	// Feature #26: the notification event consumer and retention runner.
	if runner := notification.NewEventConsumerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	if runner := notification.NewNotificationRetentionRunnerRunner(srv.Components()); runner != nil {
		srv.AddRunner(runner)
	}
	// Feature #27: the trace retention runner deletes traces (and their
	// spans) older than tracing.retention.traceTTL (30 days, AD5).
	if runner := tracing.NewTraceRetentionRunnerRunner(srv.Components()); runner != nil {
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
	// Feature-25 billing-reports runners: the generator picks up pending
	// reports and renders their CSV; the schedule runner materializes due
	// schedules into pending report runs.
	if cfg.Billing.Reports.Enabled {
		srv.AddRunner(billing.NewReportGeneratorRunner(billingSvc, cfg.Billing.Reports.GeneratorInterval))
		srv.AddRunner(billing.NewScheduleRunner(billingSvc, cfg.Billing.Reports.ScheduleInterval))
	}
	// Feature #41: the data-export generation runner picks up pending
	// exports and renders them (AD2). It is wired after Init so the
	// database is available.
	if dbComponent := srv.Components().DB(); dbComponent != nil {
		if gormDB, ok := dbComponent.GormDB().(*gorm.DB); ok {
			srv.AddRunner(account.NewExportGeneratorRunner(
				accountSvc,
				account.NewDBExportDataProvider(gormDB),
				cfg.Account.Export.GeneratorInterval,
			))
			// Feature #42: the batch worker and the retention runner.
			// The worker drives the inference endpoint with the system
			// credential and meters successes at 0.5x; the retention
			// runner deletes expired result/error files (AD8).
			if cfg.Batch.Worker.Enabled {
				batchRepo := batch.NewRepository(gormDB)
				batchStore := batch.NewLocalFileStore(cfg.Batch.StoreDir)
				var inferClient batch.InferenceClient
				var meterer batch.Meterer
				if cfg.Infer.EndpointBaseURL != "" {
					inferClient = batch.NewHTTPInferenceClient(cfg.Infer.EndpointBaseURL, "")
				}
				if inferClient != nil {
					srv.AddRunner(batch.NewBatchWorker(
						batchRepo, batchStore, inferClient, meterer,
						cfg.Batch.Worker.Concurrency, cfg.Batch.Worker.PollInterval,
					))
				}
			}
			if cfg.Batch.Retention.Enabled {
				batchRepo := batch.NewRepository(gormDB)
				batchStore := batch.NewLocalFileStore(cfg.Batch.StoreDir)
				srv.AddRunner(batch.NewBatchRetentionRunner(
					batchRepo, batchStore,
					cfg.Batch.Retention.FileTTL, cfg.Batch.Retention.Interval,
				))
			}
		}
	}

	srv.Serve()
}
