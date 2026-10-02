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
