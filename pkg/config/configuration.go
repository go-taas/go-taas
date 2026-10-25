package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"

	"github.com/go-taas/go-taas/pkg/logger"
)

// configInstance guards the process-wide configuration so hot reloads never
// expose a partially-copied struct to readers.
var configInstance struct {
	sync.RWMutex
	configuration *Configuration
}

// GetConfig returns the process-wide configuration. It returns nil when no
// configuration has been loaded yet; callers in the startup path should
// always call ParseConfigs first.
func GetConfig() *Configuration {
	configInstance.RLock()
	defer configInstance.RUnlock()
	return configInstance.configuration
}

// SetConfigForTest installs cfg as the process-wide configuration. It
// exists for tests that exercise config-dependent code paths without
// loading files; production code must use ParseConfigs.
func SetConfigForTest(cfg *Configuration) {
	configInstance.Lock()
	defer configInstance.Unlock()
	configInstance.configuration = cfg
}

// ParseConfigs reads the group of YAML files matched by pathPattern, merges
// them, applies environment overrides and publishes the result via
// GetConfig. It panics on malformed configuration because a broken config
// must stop the process before it serves any traffic.
func ParseConfigs(pathPattern string) {
	matches, err := filepath.Glob(pathPattern)
	if err != nil {
		logger.S().Panicf("ParseConfigs - failed to glob config files (%v): %v", pathPattern, err)
	}

	v := viper.New()
	v.SetConfigType("yaml")

	// Accept environment variable overrides: CONFIG_DB_MASTER_HOST=... .
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.SetEnvPrefix("CONFIG")

	// Feature #19: the compatibility matrix seeds on first boot by
	// default (AD3). The default is applied here (not in applyDefaults)
	// so an explicit `seedOnBoot: false` in a config file or a
	// CONFIG_IMAGE_COMPATIBILITY_SEEDONBOOT=false override is preserved,
	// while an absent key still defaults to true.
	v.SetDefault("image.compatibility.seedOnBoot", true)

	// Feature #20: the load-test system credential is enabled by
	// default (AD11). Applied here (not in applyDefaults) so an explicit
	// `systemCredentialEnabled: false` in a config file or a
	// CONFIG_LOADTEST_SYSTEMCREDENTIALENABLED=false override is
	// preserved, while an absent key still defaults to true.
	v.SetDefault("loadtest.systemCredentialEnabled", true)

	// Feature #50: the optimization peak windows apply Monday-Friday
	// by default (AD13). Applied here (not in applyDefaults) so an
	// explicit `peakWeekdaysOnly: false` in a config file or a
	// CONFIG_OPTIMIZATION_PEAKWEEKDAYSONLY=false override is
	// preserved, while an absent key still defaults to true.
	v.SetDefault("optimization.peakWeekdaysOnly", true)

	// Feature #51: the quota in-flight report consumer is enabled by
	// default (AD14). Applied here (not in applyDefaults) so an
	// explicit `concurrencyConsumer.enabled: false` in a config file or
	// a CONFIG_QUOTA_CONCURRENCYCONSUMER_ENABLED=false override is
	// preserved, while an absent key still defaults to true.
	v.SetDefault("quota.concurrencyConsumer.enabled", true)

	// Feature-22 (AD6/AD10): the first-boot SSO seed is enabled by
	// default so a fresh install is usable out of the box. Applied here
	// (not in applyDefaults) so an explicit `seedSso.enabled: false` in
	// a config file or a CONFIG_AUTH_SEEDSSO_ENABLED=false override is
	// preserved, while an absent key still defaults to true.
	v.SetDefault("auth.seedSso.enabled", true)

	for _, configFile := range matches {
		// Skip hidden files such as editor backups.
		if strings.HasPrefix(filepath.Base(configFile), ".") {
			logger.S().Infof("ParseConfigs - ignored %s", configFile)
			continue
		}
		// Skip files that vanished between Glob and Open (hot-reload race).
		if stat, statErr := os.Stat(configFile); statErr != nil || !stat.Mode().IsRegular() {
			logger.S().Warnf("ParseConfigs - skip config file %s: %v", configFile, statErr)
			continue
		}

		logger.S().Infof("ParseConfigs - loading config file %s", configFile)
		f, openErr := os.Open(configFile) // #nosec G304 -- path comes from a validated glob
		if openErr != nil {
			logger.S().Warnf("ParseConfigs - skip config file %s: %v", configFile, openErr)
			continue
		}
		if mergeErr := v.MergeConfig(f); mergeErr != nil {
			_ = f.Close()
			logger.S().Panicf("ParseConfigs - merge config file(%s) failed: %v", configFile, mergeErr)
		}
		_ = f.Close()
	}

	cfg := &Configuration{}
	if err := v.Unmarshal(cfg); err != nil {
		logger.S().Panicf("ParseConfigs - parse merged config failed: %v", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		logger.S().Panicf("ParseConfigs - config validate failed: %v", err)
	}

	configInstance.Lock()
	configInstance.configuration = cfg
	configInstance.Unlock()
}

// applyDefaults fills in the defaults for fields that may be omitted
// from configuration files. Explicit zero values that are meaningful
// (enabled=false) are preserved by only defaulting the worker count
// when unset and the enabled flag through the file presence; the
// consumer Runner treats workers<=0 as 1.
func (c *Configuration) applyDefaults() {
	if c.Auth.SessionTTL == 0 {
		c.Auth.SessionTTL = 24 * time.Hour
	}
	// Feature-22 (AD3): the admin-role set defaults to the tenancy and
	// platform role vocabulary. A deployment can override it.
	if len(c.Auth.AdminRoles) == 0 {
		c.Auth.AdminRoles = []string{"platform-admin", "org-admin", "admin", "owner"}
	}
	// Feature-22 (AD6/AD10): the first-boot SSO seed defaults to the
	// compose Keycloak values so a fresh install is usable out of the
	// box. The Helm chart overrides issuer/redirectUri to point at the
	// bundled Keycloak and the exposed URL.
	if c.Auth.SeedSso.ProviderID == "" {
		c.Auth.SeedSso.ProviderID = "keycloak"
	}
	if c.Auth.SeedSso.Issuer == "" {
		c.Auth.SeedSso.Issuer = "http://keycloak:8080/realms/go-taas"
	}
	if c.Auth.SeedSso.ClientID == "" {
		c.Auth.SeedSso.ClientID = "go-taas-console"
	}
	if c.Auth.SeedSso.ClientSecret == "" {
		c.Auth.SeedSso.ClientSecret = "go-taas-console-secret"
	}
	if c.Auth.SeedSso.RedirectURI == "" {
		c.Auth.SeedSso.RedirectURI = "http://localhost:9091/api/v1/auth/sso/*"
	}
	if c.Auth.SeedSso.AdminUsername == "" {
		c.Auth.SeedSso.AdminUsername = "admin"
	}
	if c.Auth.SeedSso.AdminPassword == "" {
		c.Auth.SeedSso.AdminPassword = "admin"
	}
	if c.Auth.SeedSso.AdminOrgID == "" {
		c.Auth.SeedSso.AdminOrgID = "org-admin"
	}
	if c.Image.WarmupStatusConsumer.Workers == 0 {
		c.Image.WarmupStatusConsumer.Workers = 2
	}
	// Feature #19: the compatibility matrix defaults. seedOnBoot is true
	// by default; lazySeedDefault is "experimental" (the vendor-match
	// rule's experimental branch; the unsupported branch is always
	// unsupported regardless of this value).
	if c.Image.Compatibility.LazySeedDefault == "" {
		c.Image.Compatibility.LazySeedDefault = "experimental"
	}
	if c.Accelerator.CollectInterval == 0 {
		c.Accelerator.CollectInterval = 30 * time.Second
	}
	if c.Accelerator.SnapshotConsumer.Workers == 0 {
		c.Accelerator.SnapshotConsumer.Workers = 1
	}
	// Feature #20: load-test runner defaults.
	if c.LoadTest.ProgressInterval == 0 {
		c.LoadTest.ProgressInterval = 5 * time.Second
	}
	if c.LoadTest.Retention == 0 {
		c.LoadTest.Retention = 90 * 24 * time.Hour
	}
	if c.Model.Auth.CacheTTL == 0 {
		c.Model.Auth.CacheTTL = 5 * time.Second
	}
	if c.Tenancy.DefaultOrgID == "" {
		c.Tenancy.DefaultOrgID = "org-default"
	}
	if c.Tenancy.DefaultOrgDisplayName == "" {
		c.Tenancy.DefaultOrgDisplayName = "Default Organization"
	}
	if c.Tenancy.InvitationTTL == 0 {
		c.Tenancy.InvitationTTL = 7 * 24 * time.Hour
	}
	if c.Metering.Settlement.Interval == 0 {
		c.Metering.Settlement.Interval = 60 * time.Second
	}
	if c.Metering.Settlement.GracePeriod == 0 {
		c.Metering.Settlement.GracePeriod = 5 * time.Minute
	}
	if c.Metering.Settlement.Workers == 0 {
		c.Metering.Settlement.Workers = 2
	}
	if c.Metering.Retention.VoucherTTL == 0 {
		c.Metering.Retention.VoucherTTL = 2160 * time.Hour
	}
	if c.Metering.Retention.RequestLogTTL == 0 {
		c.Metering.Retention.RequestLogTTL = 720 * time.Hour
	}
	if c.Metering.Retention.BatchSize == 0 {
		c.Metering.Retention.BatchSize = 1000
	}
	if c.Metering.Retention.Interval == 0 {
		c.Metering.Retention.Interval = time.Hour
	}
	if c.Metering.EventConsumer.Workers == 0 {
		c.Metering.EventConsumer.Workers = 2
	}
	if c.Billing.Currency == "" {
		c.Billing.Currency = "USD"
	}
	if c.Billing.EventConsumer.Workers == 0 {
		c.Billing.EventConsumer.Workers = 2
	}
	if c.Billing.SettlementsConsumer.Workers == 0 {
		c.Billing.SettlementsConsumer.Workers = 2
	}
	if c.Billing.Reconciliation.Interval == 0 {
		c.Billing.Reconciliation.Interval = 60 * time.Second
	}
	if c.Billing.Reconciliation.GracePeriod == 0 {
		c.Billing.Reconciliation.GracePeriod = 5 * time.Minute
	}
	if c.Billing.Reconciliation.Workers == 0 {
		c.Billing.Reconciliation.Workers = 2
	}
	if c.Billing.CycleReset.Interval == 0 {
		c.Billing.CycleReset.Interval = time.Minute
	}
	// Feature-25: the billing-reports runner defaults (AD1, AD4).
	if c.Billing.Reports.GeneratorInterval == 0 {
		c.Billing.Reports.GeneratorInterval = 5 * time.Second
	}
	if c.Billing.Reports.ScheduleInterval == 0 {
		c.Billing.Reports.ScheduleInterval = time.Minute
	}
	// Feature-36: the usage & cost forecasting defaults (AD3, AD4).
	if c.Billing.Forecast.HorizonDefaultDays == 0 {
		c.Billing.Forecast.HorizonDefaultDays = 30
	}
	if c.Billing.Forecast.HorizonMaxDays == 0 {
		c.Billing.Forecast.HorizonMaxDays = 90
	}
	if c.Billing.Forecast.RangeMaxDays == 0 {
		c.Billing.Forecast.RangeMaxDays = 92
	}
	if c.Audit.Retention.EventTTL == 0 {
		c.Audit.Retention.EventTTL = 365 * 24 * time.Hour
	}
	if c.Audit.Retention.BatchSize == 0 {
		c.Audit.Retention.BatchSize = 1000
	}
	if c.Audit.Retention.Interval == 0 {
		c.Audit.Retention.Interval = time.Hour
	}
	if c.Audit.ExportMaxRows == 0 {
		c.Audit.ExportMaxRows = 10000
	}
	// Feature #23: webhook module defaults (AD4, AD8, AD11).
	if c.Webhook.Delivery.Workers == 0 {
		c.Webhook.Delivery.Workers = 4
	}
	if c.Webhook.Delivery.PollInterval == 0 {
		c.Webhook.Delivery.PollInterval = 5 * time.Second
	}
	if c.Webhook.Delivery.Timeout == 0 {
		c.Webhook.Delivery.Timeout = 10 * time.Second
	}
	if c.Webhook.Retention.DeliveryTTL == 0 {
		c.Webhook.Retention.DeliveryTTL = 2160 * time.Hour
	}
	if c.Webhook.Retention.BatchSize == 0 {
		c.Webhook.Retention.BatchSize = 1000
	}
	if c.Webhook.Retention.Interval == 0 {
		c.Webhook.Retention.Interval = time.Hour
	}
	// Feature #24: the observability max range defaults to 92 days
	// (AD7), mirroring the metering maxRangeSeconds constant.
	if c.Observability.MaxRangeSeconds == 0 {
		c.Observability.MaxRangeSeconds = 92 * 24 * 3600
	}
	// Feature #30: the system-status staleness threshold defaults to 60
	// seconds (AD5).
	if c.Observability.Status.StaleAfterSeconds == 0 {
		c.Observability.Status.StaleAfterSeconds = 60
	}
	// Feature #26: notification module defaults (Section 9).
	if c.Notification.Consumer.Workers == 0 {
		c.Notification.Consumer.Workers = 4
	}
	if c.Notification.Retention.NotificationTTL == 0 {
		c.Notification.Retention.NotificationTTL = 2160 * time.Hour
	}
	if c.Notification.Retention.BatchSize == 0 {
		c.Notification.Retention.BatchSize = 1000
	}
	if c.Notification.Retention.Interval == 0 {
		c.Notification.Retention.Interval = time.Hour
	}
	// Feature #27: tracing module defaults (Section 9). Traces are kept
	// for 30 days (720h), aligned with request logs (AD5).
	if c.Tracing.Retention.TraceTTL == 0 {
		c.Tracing.Retention.TraceTTL = 720 * time.Hour
	}
	// Feature #37: the resourcemetrics max range defaults to 92 days
	// (AD5), mirroring the observability/metering maxRangeSeconds.
	if c.ResourceMetrics.MaxRangeSeconds == 0 {
		c.ResourceMetrics.MaxRangeSeconds = 92 * 24 * 3600
	}
	// Feature #37: the controller's per-service resource sampling
	// interval defaults to 30s (AD2).
	if c.Controller.ResourceMetrics.SampleInterval == 0 {
		c.Controller.ResourceMetrics.SampleInterval = 30 * time.Second
	}
	// Feature #38: the docs catalog version defaults to v1 (AD3, AD7).
	if c.Docs.CatalogVersion == "" {
		c.Docs.CatalogVersion = "v1"
	}
	// Feature #40: the controller's per-cluster health collection
	// interval defaults to 30s (AD4).
	if c.Controller.Cluster.CollectInterval == 0 {
		c.Controller.Cluster.CollectInterval = 30 * time.Second
	}
	// Feature #41: the data-export generation runner defaults (Section
	// 9).
	if c.Account.Export.GeneratorInterval == 0 {
		c.Account.Export.GeneratorInterval = 5 * time.Second
	}
	if c.Account.Export.MaxRangeSeconds == 0 {
		c.Account.Export.MaxRangeSeconds = 92 * 24 * 3600
	}
	// Feature #42: batch module defaults (Section 9).
	if c.Batch.Worker.PollInterval == 0 {
		c.Batch.Worker.PollInterval = 5 * time.Second
	}
	if c.Batch.Worker.Concurrency == 0 {
		c.Batch.Worker.Concurrency = 4
	}
	if c.Batch.Retention.FileTTL == 0 {
		c.Batch.Retention.FileTTL = 720 * time.Hour
	}
	if c.Batch.Retention.Interval == 0 {
		c.Batch.Retention.Interval = time.Hour
	}
	if c.Batch.MaxFileBytes == 0 {
		c.Batch.MaxFileBytes = 524288000
	}
	if c.Batch.MaxLines == 0 {
		c.Batch.MaxLines = 50000
	}
	if c.Batch.StoreDir == "" {
		c.Batch.StoreDir = "/data/batch"
	}
	// Feature #43: prompt module defaults (Section 9).
	if c.Prompt.MaxNameLength == 0 {
		c.Prompt.MaxNameLength = 128
	}
	if c.Prompt.MaxContentLength == 0 {
		c.Prompt.MaxContentLength = 6144
	}
	if c.Evaluation.Worker.PollInterval == 0 {
		c.Evaluation.Worker.PollInterval = 2 * time.Second
	}
	if c.Evaluation.Worker.MaxConcurrentRuns == 0 {
		c.Evaluation.Worker.MaxConcurrentRuns = 4
	}
	if c.Evaluation.Worker.MaxConcurrentCasesPerRun == 0 {
		c.Evaluation.Worker.MaxConcurrentCasesPerRun = 1
	}
	if c.Evaluation.Retention.TTL == 0 {
		c.Evaluation.Retention.TTL = 2160 * time.Hour
	}
	if c.Evaluation.Retention.Interval == 0 {
		c.Evaluation.Retention.Interval = time.Hour
	}
	if c.Evaluation.MaxCasesPerRun == 0 {
		c.Evaluation.MaxCasesPerRun = 50
	}
	if c.Evaluation.MaxVariableValueLength == 0 {
		c.Evaluation.MaxVariableValueLength = 10000
	}
	if c.Evaluation.MaxInFlightRunsPerOrg == 0 {
		c.Evaluation.MaxInFlightRunsPerOrg = 10
	}
	// The image-import Harbor project defaults to "taas" so imported
	// images always land in the platform's own project.
	if c.Image.Harbor.Project == "" {
		c.Image.Harbor.Project = "taas"
	}
	// The controller's weights PVC and mount path defaults.
	if c.Controller.Weights.PVCName == "" {
		c.Controller.Weights.PVCName = "model-weights"
	}
	if c.Controller.Weights.MountPath == "" {
		c.Controller.Weights.MountPath = "/data/weights"
	}
	// Feature #47: distributed-inference-runtime defaults (Section 9).
	// The collection and scheme-broadcast intervals default to 30s and the
	// AIBrix control plane defaults to the shipped namespace/gateway.
	if c.Runtime.CollectInterval == 0 {
		c.Runtime.CollectInterval = 30 * time.Second
	}
	if c.Runtime.SchemeBroadcastInterval == 0 {
		c.Runtime.SchemeBroadcastInterval = 30 * time.Second
	}
	if c.Runtime.HealthConsumer.Workers == 0 {
		c.Runtime.HealthConsumer.Workers = 1
	}
	if c.Runtime.AIBrix.Namespace == "" {
		c.Runtime.AIBrix.Namespace = "aibrix-system"
	}
	if c.ResourceMetrics.PrometheusTimeout <= 0 {
		c.ResourceMetrics.PrometheusTimeout = 10 * time.Second
	}
	if c.ResourceMetrics.GPUUtilizationMetric == "" {
		c.ResourceMetrics.GPUUtilizationMetric = "DCGM_FI_DEV_GPU_UTIL"
	}
	if c.ResourceMetrics.GPUMemoryUsedMetric == "" {
		c.ResourceMetrics.GPUMemoryUsedMetric = "DCGM_FI_DEV_FB_USED"
	}
	if c.ResourceMetrics.GPUMemoryTotalMetric == "" {
		c.ResourceMetrics.GPUMemoryTotalMetric = "DCGM_FI_DEV_FB_TOTAL"
	}
	if c.ResourceMetrics.GPUMemoryFreeMetric == "" {
		c.ResourceMetrics.GPUMemoryFreeMetric = "DCGM_FI_DEV_FB_FREE"
	}
	if c.ResourceMetrics.Engine.TTFTBucket == "" {
		c.ResourceMetrics.Engine.TTFTBucket = "vllm:time_to_first_token_seconds_bucket"
	}
	if c.ResourceMetrics.Engine.TPOTBucket == "" {
		c.ResourceMetrics.Engine.TPOTBucket = "vllm:inter_token_latency_seconds_bucket"
	}
	if c.ResourceMetrics.Engine.KVCacheUsage == "" {
		c.ResourceMetrics.Engine.KVCacheUsage = "vllm:kv_cache_usage_perc"
	}
	if c.ResourceMetrics.Engine.RequestsRunning == "" {
		c.ResourceMetrics.Engine.RequestsRunning = "vllm:num_requests_running"
	}
	if c.ResourceMetrics.Engine.RequestsWaiting == "" {
		c.ResourceMetrics.Engine.RequestsWaiting = "vllm:num_requests_waiting"
	}
	if c.ResourceMetrics.Engine.GenerationTokens == "" {
		c.ResourceMetrics.Engine.GenerationTokens = "vllm:generation_tokens_total"
	}
	if c.ResourceMetrics.Engine.Quantile <= 0 || c.ResourceMetrics.Engine.Quantile > 1 {
		c.ResourceMetrics.Engine.Quantile = 0.95
	}
	if c.ResourceMetrics.InferenceNamespace == "" {
		// controller.namespace is optional and often unset, so falling
		// back to it alone would leave the PromQL namespace matcher empty
		// and every query would match nothing.
		c.ResourceMetrics.InferenceNamespace = c.Controller.Namespace
		if c.ResourceMetrics.InferenceNamespace == "" {
			c.ResourceMetrics.InferenceNamespace = "taas-infer"
		}
	}
	if c.Runtime.DataPlane.RouteNamespace == "" {
		c.Runtime.DataPlane.RouteNamespace = c.Controller.Namespace
	}
	if c.Runtime.DataPlane.ParentRefSection == "" {
		c.Runtime.DataPlane.ParentRefSection = "https"
	}
	if c.Runtime.DataPlane.BackendPort == 0 {
		c.Runtime.DataPlane.BackendPort = 80
	}
	if c.Runtime.AIBrix.GatewayName == "" {
		c.Runtime.AIBrix.GatewayName = "aibrix-eg"
	}
	if c.Runtime.AIBrix.Gateway == "" {
		c.Runtime.AIBrix.Gateway = "envoy-aibrix-system-aibrix-eg"
	}
	// Feature #48: external-model-provider defaults (Section 9). The probe
	// worker's enabled flag ships as true in configs/config.yaml (the
	// repo's convention for boolean kill switches); here we default the
	// scalar fields.
	if c.Provider.ProbeInterval == 0 {
		c.Provider.ProbeInterval = 60 * time.Second
	}
	if c.Provider.ProbeWorker.Workers == 0 {
		c.Provider.ProbeWorker.Workers = 2
	}
	if c.Provider.DefaultRequestTimeoutSeconds == 0 {
		c.Provider.DefaultRequestTimeoutSeconds = 10
	}
	// Feature #49: intelligent-model-routing defaults (Section 9). The
	// retention runner's enabled flag ships as true in
	// configs/config.yaml (the repo's convention for boolean kill
	// switches); here we default the scalar fields.
	if c.Routing.DecisionRetentionDays == 0 {
		c.Routing.DecisionRetentionDays = 7
	}
	if c.Routing.Retention.BatchSize == 0 {
		c.Routing.Retention.BatchSize = 1000
	}
	if c.Routing.Retention.Interval == 0 {
		c.Routing.Retention.Interval = time.Hour
	}
	if c.Routing.LatencyWindow == 0 {
		c.Routing.LatencyWindow = 15 * time.Minute
	}
	// Feature #50: cost-optimization-recommendations defaults (Section
	// 9). All rule thresholds are config constants (design D3); the
	// maps default empty (R1/R3 activate only for annotated models and
	// card types, AD12/AD14).
	if c.Optimization.RefreshIntervalMinutes == 0 {
		c.Optimization.RefreshIntervalMinutes = 60
	}
	if c.Optimization.WindowDays == 0 {
		c.Optimization.WindowDays = 30
	}
	if c.Optimization.TopNPerOrg == 0 {
		c.Optimization.TopNPerOrg = 50
	}
	if c.Optimization.Currency == "" {
		c.Optimization.Currency = "CNY"
	}
	if c.Optimization.R1.MinRequests == 0 {
		c.Optimization.R1.MinRequests = 1000
	}
	if c.Optimization.R1.MinBilledTokens == 0 {
		c.Optimization.R1.MinBilledTokens = 10000000
	}
	if c.Optimization.R1.ContextRatio == 0 {
		c.Optimization.R1.ContextRatio = 0.8
	}
	if c.Optimization.R1.MinSaving == 0 {
		c.Optimization.R1.MinSaving = 100
	}
	if c.Optimization.R2.MinRequests == 0 {
		c.Optimization.R2.MinRequests = 1000
	}
	if c.Optimization.R2.OfflineRatio == 0 {
		c.Optimization.R2.OfflineRatio = 0.9
	}
	if c.Optimization.R2.PromptTokenFloor == 0 {
		c.Optimization.R2.PromptTokenFloor = 512
	}
	if c.Optimization.R3.IdleRequestsPerDay == 0 {
		c.Optimization.R3.IdleRequestsPerDay = 10
	}
	if c.Optimization.R3.IdleWindowDays == 0 {
		c.Optimization.R3.IdleWindowDays = 7
	}
	if c.Optimization.R4.MinPromptTokens == 0 {
		c.Optimization.R4.MinPromptTokens = 1000000
	}
	if c.Optimization.R4.MaxCacheHitRate == 0 {
		c.Optimization.R4.MaxCacheHitRate = 0.05
	}
	if c.Optimization.R4.DefaultHitAssumption == 0 {
		c.Optimization.R4.DefaultHitAssumption = 0.5
	}
	if len(c.Optimization.PeakHours) == 0 {
		c.Optimization.PeakHours = []string{"09:00-18:00"}
	}
	if c.Optimization.Timezone == "" {
		c.Optimization.Timezone = "UTC"
	}
	if len(c.Optimization.PrefixCacheEngines) == 0 {
		c.Optimization.PrefixCacheEngines = []string{"vllm", "sglang"}
	}
	// Feature #51: service-quota-management defaults (Section 9). The
	// four org quota dimensions default to 0 = unlimited (design D2);
	// the consumer runner is on by default (AD14).
	if c.Quota.Timezone == "" {
		c.Quota.Timezone = "UTC"
	}
	if c.Quota.ConcurrencyConsumer.Workers == 0 {
		c.Quota.ConcurrencyConsumer.Workers = 2
	}
	if c.Quota.ConcurrencyConsumer.StaleAfter == 0 {
		c.Quota.ConcurrencyConsumer.StaleAfter = 2 * time.Minute
	}
	// Feature #52: vGPU slicing-pool defaults (Section 9). The
	// membership projection is as fresh as the accelerator-inventory
	// interval; the HAMI contract names are the pinned defaults.
	if c.VGPU.MembershipInterval == 0 {
		c.VGPU.MembershipInterval = 30 * time.Second
	}
	if c.VGPU.HAMI.MemResource == "" {
		c.VGPU.HAMI.MemResource = "nvidia.com/gpumem"
	}
	if c.VGPU.HAMI.CoreResource == "" {
		c.VGPU.HAMI.CoreResource = "nvidia.com/gpucores"
	}
	if c.VGPU.HAMI.PolicyAnnotation == "" {
		c.VGPU.HAMI.PolicyAnnotation = "hami.io/gpu-scheduler-policy"
	}
	if c.VGPU.HAMI.PoolLabelPrefix == "" {
		c.VGPU.HAMI.PoolLabelPrefix = "go-taas.com/vgpu-pool"
	}
	// Feature #53: fault-diagnosis defaults (Section 9). The debounce
	// windows, the retention window, and the default auto-recovery
	// toggles.
	if c.Fault.Debounce.CrashLoopRestarts == 0 {
		c.Fault.Debounce.CrashLoopRestarts = 3
	}
	if c.Fault.Debounce.CrashLoopWindow == 0 {
		c.Fault.Debounce.CrashLoopWindow = 3 * time.Minute
	}
	if c.Fault.Retention == 0 {
		c.Fault.Retention = 720 * time.Hour
	}
	if len(c.Fault.Recovery.AutoRestart) == 0 {
		c.Fault.Recovery.AutoRestart = map[string]bool{
			"service_crash_loop": true,
			"oom":                true,
			"image_pull_failure": true,
		}
	}
	if len(c.Fault.Recovery.AutoReschedule) == 0 {
		c.Fault.Recovery.AutoReschedule = map[string]bool{
			"node_not_ready":            true,
			"accelerator_device_plugin": true,
		}
	}
	if c.Fault.SignalsConsumer.Workers == 0 {
		c.Fault.SignalsConsumer.Workers = 2
	}
}

// String returns a string representation of the configuration for logging.
// Secrets are not masked here; callers must only log it at debug level.
func (c *Configuration) String() string {
	if content, err := json.Marshal(c); err == nil {
		return string(content)
	}
	return "<Configuration>"
}

// WatchConfig watches the configuration files matched by pathPattern and
// invokes onChange with the freshly loaded configuration on every change.
// It is intended for development-time hot reload; production deployments
// should restart pods instead. The returned stop function releases the
// watcher.
func WatchConfig(pathPattern string, onChange func(*Configuration)) (stop func(), err error) {
	matches, err := filepath.Glob(pathPattern)
	if err != nil {
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	// Watch the parent directories: many editors replace files atomically,
	// which the watcher would otherwise miss.
	dirs := make(map[string]struct{})
	for _, m := range matches {
		dirs[filepath.Dir(m)] = struct{}{}
	}
	for dir := range dirs {
		if err := watcher.Add(dir); err != nil {
			_ = watcher.Close()
			return nil, err
		}
	}

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove) == 0 {
					continue
				}
				// Re-parse the whole glob: a change in one file may affect
				// the merged result in non-obvious ways.
				ParseConfigs(pathPattern)
				if onChange != nil {
					onChange(GetConfig())
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				logger.S().Warnf("WatchConfig - watcher error: %v", err)
			}
		}
	}()

	return func() { _ = watcher.Close() }, nil
}
