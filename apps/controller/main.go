// Command controller runs the resource reconciliation loop: it consumes
// desired-state change messages from the message queue and applies them
// to the Kubernetes cluster.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/internal/controller"
	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/database"
	"github.com/go-taas/go-taas/pkg/k8s"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

var _, _, _ = version, commit, buildTime

func main() {
	args := (&config.Arguments{
		ConfigPath: config.DefaultConfigPath(),
	}).Parse()

	config.ParseConfigs(args.ConfigPath)
	cfg := config.GetConfig()
	if err := logger.Init(logger.Config{
		Level:    server.LogLevelFromConfig(cfg.Log.Level),
		Encoding: cfg.Log.Encoding,
	}); err != nil {
		panic(err)
	}

	mqClient, err := mq.NewClient(&cfg.MQ)
	if err != nil {
		logger.S().Fatalw("init mq failed", "err", err)
	}
	defer func() { _ = mqClient.Close() }()

	k8sClient, err := k8s.NewClient(&k8s.Config{Kubeconfig: cfg.Controller.Kubeconfig})
	if err != nil {
		logger.S().Fatalw("init kubernetes client failed", "err", err)
	}

	// Feature #47: the shared serving-scheme cache. It defaults to plain
	// and is populated by the runtime.scheme consumer so a restarted
	// controller reconverges without a database.
	schemeCache := controller.NewSchemeCache()

	reconciler := controller.NewK8sReconciler(k8sClient, mqClient, cfg.Infer.EndpointBaseURL, cfg.Controller.Namespace, controller.WeightsConfig{
		StorageClass: cfg.Controller.Weights.StorageClass,
		PVCName:      cfg.Controller.Weights.PVCName,
		MountPath:    cfg.Controller.Weights.MountPath,
	}, schemeCache, cfg.Controller.AcceleratorLabelKey)
	// Feature #52 (AD13): the vGPU slicing-pool HAMI contract. The
	// placement-policy annotation, the pool's scheduling hint, and the
	// pool's node label selector are config-driven.
	// The serving gateway resolves a request's model to an HTTPRoute in its
	// own namespace, so the controller writes model routes next to the
	// Gateway the AIBrix install declares.
	reconciler.SetGatewayRouting(cfg.Runtime.AIBrix.Namespace, cfg.Runtime.AIBrix.GatewayName)
	reconciler.SetVGPUConfig(
		cfg.VGPU.HAMI.PolicyAnnotation,
		"binpack",
		map[string]string{cfg.VGPU.HAMI.PoolLabelPrefix: "pool-a"},
	)
	ctrl := controller.New(mqClient, reconciler)
	// Feature #18: the accelerator inventory collector lists nodes and
	// publishes the full snapshot on the configured interval.
	ctrl.SetInventoryCollector(controller.NewInventoryCollector(
		k8sClient.Clientset(), mqClient, cfg.Accelerator.CollectInterval,
	))
	// Feature #47: the distributed-inference-runtime consumer of the
	// serving scheme, the model-change consumer that reconciles AIBrix
	// ModelClaims, and the component + ModelClaim collector that publishes
	// the runtime.health snapshot.
	if cfg.Runtime.SchemeConsumer.Enabled {
		ctrl.SetRuntimeSchemeConsumer(controller.NewRuntimeSchemeConsumer(mqClient, schemeCache))
	}
	if cfg.Runtime.ModelChangeConsumer.Enabled {
		ctrl.SetRuntimeModelChangeConsumer(controller.NewRuntimeModelChangeConsumer(
			mqClient, k8sClient.Dynamic(), cfg.Controller.Namespace,
		))
	}
	ctrl.SetRuntimeCollector(controller.NewRuntimeCollector(
		k8sClient.Clientset(), k8sClient.Dynamic(), mqClient,
		controller.RuntimeCollectorConfig{
			Interval:        cfg.Runtime.CollectInterval,
			Namespace:       cfg.Controller.Namespace,
			AIBrixNamespace: cfg.Runtime.AIBrix.Namespace,
			Gateway:         cfg.Runtime.AIBrix.Gateway,
		},
		schemeCache,
	))
	// Feature #37: the per-service resource sampling loop reads
	// CPU/memory from the Kubernetes metrics-server and GPU utilization
	// from the accelerator signals, and writes sample rows to the
	// service_resource_metrics table. It is wired when the database is
	// reachable; the metrics-server client is built from the same
	// kubeconfig.
	//
	// The connection is retried: a pod that starts before PostgreSQL is
	// ready used to lose the sampler for the rest of its life (one failed
	// InitDB silently disabled metrics until the next restart).
	// The sampler exists for installations without Prometheus. When
	// Prometheus is the source (the metrics page reads it), writing the
	// same numbers into PostgreSQL is pure churn: skip it and say so.
	if cfg.ResourceMetrics.PrometheusURL != "" {
		logger.S().Infow("controller: resource sampling disabled, the metrics page reads Prometheus",
			"prometheus", cfg.ResourceMetrics.PrometheusURL)
	} else if db, dbErr := initDBWithRetry(&cfg.Databases.Master, controllerDBRetryWindow); dbErr == nil {
		defer func() {
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}()
		metricsClient, mcErr := metricsclientset.NewForConfig(k8sClient.RESTConfig())
		if mcErr == nil {
			ctrl.SetResourceSampler(controller.NewResourceSampler(
				db,
				controller.NewK8sResourceSampleProvider(
					k8sClient.Clientset(), metricsClient, cfg.Controller.Namespace, nil,
				),
				cfg.Controller.ResourceMetrics.SampleInterval,
			))
		} else {
			logger.S().Warnw("controller: resource sampling disabled, metrics client unavailable", "err", mcErr)
		}
	} else {
		logger.S().Warnw("controller: resource sampling disabled, database unreachable", "err", dbErr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.S().Info("controller started")
	if err := ctrl.Run(ctx); err != nil {
		logger.S().Fatalw("controller exited with error", "err", err)
	}
	logger.S().Info("controller stopped")
}

// controllerDBRetryWindow bounds how long startup keeps trying to reach
// the database. Long enough to outlast a dependency rolling update, short
// enough that a genuinely broken configuration still fails visibly.
const controllerDBRetryWindow = 90 * time.Second

// initDBWithRetry opens the database, retrying until the window expires.
// Features that need the database are wired from the result, so a
// transient outage at boot must not decide they are unavailable forever.
func initDBWithRetry(cfg *config.DBConfig, window time.Duration) (*gorm.DB, error) {
	deadline := time.Now().Add(window)
	var lastErr error
	for attempt := 1; ; attempt++ {
		db, err := database.InitDB(cfg)
		if err == nil {
			return db, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		logger.S().Infow("controller: database not ready, retrying", "attempt", attempt, "err", err)
		time.Sleep(2 * time.Second)
	}
}
