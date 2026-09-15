package config

import (
	"path/filepath"

	"github.com/spf13/pflag"
)

// Arguments holds the command-line flags shared by all platform binaries.
type Arguments struct {
	// BindingHost is the IP the servers bind to ("" = all interfaces).
	BindingHost string `json:"bindingHost"`
	// ServicePort is the gRPC serving port.
	ServicePort int `json:"servicePort"`
	// MonitorPort is the HTTP port for metrics and health probes.
	MonitorPort int `json:"monitorPort"`
	// GatewayPort is the HTTP port of the control-plane gateway.
	GatewayPort int `json:"gatewayPort"`
	// ConfigPath is the glob pattern of configuration files.
	ConfigPath string `json:"configPath"`
	// DisableAuth turns off authentication (development only).
	DisableAuth bool `json:"disableAuth"`
	// NoDatabase disables the database component (development only).
	NoDatabase bool `json:"noDatabase"`
	// LogLevel is the initial zap log level (-1..5).
	LogLevel int `json:"logLevel"`
}

var arguments *Arguments

// Parse parses the command-line arguments. It is idempotent: the second
// and later calls return the same result as the first.
func (args *Arguments) Parse() *Arguments {
	if arguments != nil {
		return arguments
	}
	args.define()
	pflag.Parse()
	arguments = args
	return args
}

func (args *Arguments) define() {
	name, usage := "binding-host", "Bind servers to the specified IP."
	pflag.StringVar(&args.BindingHost, name, args.BindingHost, usage)
	name, usage = "service-port", "The server's gRPC serving port."
	pflag.IntVar(&args.ServicePort, name, args.ServicePort, usage)
	name, usage = "monitor-port", "The port for metrics and health probes."
	pflag.IntVar(&args.MonitorPort, name, args.MonitorPort, usage)
	name, usage = "gateway-port", "The control-plane gateway's HTTP serving port."
	pflag.IntVar(&args.GatewayPort, name, args.GatewayPort, usage)
	name, usage = "config-path", "Glob pattern of configuration files."
	pflag.StringVar(&args.ConfigPath, name, args.ConfigPath, usage)
	name, usage = "disable-auth", "Disable authentication (development only)."
	pflag.BoolVar(&args.DisableAuth, name, args.DisableAuth, usage)
	name, usage = "no-database", "Start without the database component (development only)."
	pflag.BoolVar(&args.NoDatabase, name, args.NoDatabase, usage)
	name, usage = "log-level", "Initial log level, -1 (debug) to 5 (fatal)."
	pflag.IntVar(&args.LogLevel, name, args.LogLevel, usage)
}

// DefaultConfigPath is the default configuration glob used by binaries.
func DefaultConfigPath() string {
	return filepath.Join("configs", "*.yaml")
}
