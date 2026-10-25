package k8s

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-taas/go-taas/pkg/logger"
)

// GatewayResolver discovers the serving gateway's in-cluster base URL so
// the tenant quickstart page works without hand-editing configuration.
type GatewayResolver struct {
	client *Client
	// name is the configured gateway name prefix (runtime.aibrix.gateway).
	name string
	// namespace optionally narrows the search.
	namespace string
}

// NewGatewayResolver builds the resolver. name is matched as a prefix
// against Service names (the Gateway API provisions <name>-<hash>
// Services); an empty name disables discovery.
func NewGatewayResolver(client *Client, name, namespace string) *GatewayResolver {
	return &GatewayResolver{client: client, name: strings.TrimSpace(name), namespace: strings.TrimSpace(namespace)}
}

// InferenceGatewayURL implements the infer.GatewayResolver seam. It
// returns "http://<address>:<port>" for the first Service whose name
// starts with the configured gateway name (preferring the namespace it is
// configured to live in), or "" when nothing matches.
func (r *GatewayResolver) InferenceGatewayURL(ctx context.Context) string {
	if r.client == nil || r.name == "" {
		return ""
	}
	cs := r.client.Clientset()

	// Pass 1: the configured namespace, pass 2: cluster-wide. A Gateway API
	// installation puts the generated Service wherever its GatewayClass
	// allows, which is not necessarily the control plane's namespace.
	for _, ns := range r.searchNamespaces() {
		svcs, err := cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		if url := gatewayURLFromServices(svcs.Items, r.name); url != "" {
			return url
		}
	}
	logger.S().Debugw("k8s: no serving gateway service found", "gateway", r.name)
	return ""
}

// searchNamespaces returns the namespaces to search, most specific first.
func (r *GatewayResolver) searchNamespaces() []string {
	if r.namespace != "" {
		return []string{r.namespace, metav1.NamespaceAll}
	}
	return []string{metav1.NamespaceAll}
}

// gatewayURLFromServices builds the base URL from the first Service whose
// name starts with the gateway name. The load-balancer address wins over
// the ClusterIP: the quickstart snippet is copied by a tenant outside the
// cluster, so an in-cluster address would be useless. The proxy port (80)
// wins over the admin/metrics ports a gateway also exposes.
func gatewayURLFromServices(svcs []corev1.Service, name string) string {
	for i := range svcs {
		svc := &svcs[i]
		if !strings.HasPrefix(svc.Name, name) {
			continue
		}
		port, ok := proxyPort(svc.Spec.Ports)
		if !ok {
			continue
		}
		host := externalHost(svc)
		if host == "" {
			// No load balancer: the ClusterIP still works for in-cluster
			// callers (batch, playground).
			host = svc.Spec.ClusterIP
			if host == "" || host == corev1.ClusterIPNone {
				continue
			}
		}
		// Omit the scheme's default port: ":80" is noise in a URL the
		// console shows verbatim.
		if port == 80 {
			return fmt.Sprintf("http://%s", host)
		}
		return fmt.Sprintf("http://%s:%d", host, port)
	}
	return ""
}

// externalHost returns the load-balancer address of a Service, or "" when
// it has none (ClusterIP / pending).
func externalHost(svc *corev1.Service) string {
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			return ing.IP
		}
		if ing.Hostname != "" {
			return ing.Hostname
		}
	}
	return ""
}

// proxyPort picks the Service port that carries the data-plane traffic:
// port 80 when present, else 8080, else the first port.
func proxyPort(ports []corev1.ServicePort) (int32, bool) {
	if len(ports) == 0 {
		return 0, false
	}
	for _, want := range []int32{80, 8080} {
		for _, p := range ports {
			if p.Port == want {
				return p.Port, true
			}
		}
	}
	return ports[0].Port, true
}
