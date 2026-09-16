// Command controller runs the resource reconciliation loop: it consumes
// desired-state change messages from the message queue and applies them
// to the Kubernetes cluster.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-taas/go-taas/internal/controller"
	"github.com/go-taas/go-taas/pkg/config"
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

	k8sClient, err := k8s.NewClient(&k8s.Config{})
	if err != nil {
		logger.S().Fatalw("init kubernetes client failed", "err", err)
	}

	ctrl := controller.New(mqClient, controller.NewK8sReconciler(k8sClient))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.S().Info("controller started")
	if err := ctrl.Run(ctx); err != nil {
		logger.S().Fatalw("controller exited with error", "err", err)
	}
	logger.S().Info("controller stopped")
}
