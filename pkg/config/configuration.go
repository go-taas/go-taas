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
	if c.Image.WarmupStatusConsumer.Workers == 0 {
		c.Image.WarmupStatusConsumer.Workers = 2
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
