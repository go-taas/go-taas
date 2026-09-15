// Package k8s provides the Kubernetes client used by the controller. It
// bundles the typed clientset, the dynamic client and the controller-
// runtime manager handle behind one construction path driven by the
// platform configuration.
package k8s

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Config controls how the Kubernetes clients are built.
type Config struct {
	// Kubeconfig is an explicit kubeconfig path. When empty the in-cluster
	// configuration is used, falling back to ~/.kube/config for local
	// development.
	Kubeconfig string
}

// Client bundles the Kubernetes client interfaces used by the controller.
type Client struct {
	config    *rest.Config
	clientset kubernetes.Interface
	dynamic   dynamic.Interface
}

// NewClient builds a Client from cfg.
func NewClient(cfg *Config) (*Client, error) {
	restCfg, err := loadRestConfig(cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("k8s: load config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: build clientset: %w", err)
	}

	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: build dynamic client: %w", err)
	}

	return &Client{config: restCfg, clientset: clientset, dynamic: dyn}, nil
}

// Clientset returns the typed Kubernetes clientset.
func (c *Client) Clientset() kubernetes.Interface { return c.clientset }

// Dynamic returns the dynamic Kubernetes client.
func (c *Client) Dynamic() dynamic.Interface { return c.dynamic }

// loadRestConfig resolves the REST config from the explicit kubeconfig
// path, the in-cluster environment, or the default kubeconfig location.
func loadRestConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	return clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
}
