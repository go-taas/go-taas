package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestGatewayURLFromServices verifies the data-plane port wins over the
// gateway's admin/metrics ports, and that only Services whose name starts
// with the configured gateway name are considered.
func TestGatewayURLFromServices(t *testing.T) {
	svcs := []corev1.Service{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated"},
			Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Port: 80}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "envoy-kserve-system-kserve-eg-903790dc"},
			Spec: corev1.ServiceSpec{
				ClusterIP: "10.0.0.10",
				Ports: []corev1.ServicePort{
					{Name: "admin", Port: 19001},
					{Name: "http", Port: 80},
				},
			},
		},
	}
	// Port 80 is the default and is omitted from the URL.
	assert.Equal(t, "http://10.0.0.10",
		gatewayURLFromServices(svcs, "envoy-kserve-system-kserve-eg"))

	// A load-balancer address wins: the quickstart snippet is copied by a
	// tenant outside the cluster.
	svcs[1].Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.0.2.11"}}
	assert.Equal(t, "http://192.0.2.11",
		gatewayURLFromServices(svcs, "envoy-kserve-system-kserve-eg"))

	// A non-default port is kept.
	svcs[1].Spec.Ports = []corev1.ServicePort{{Name: "http", Port: 8080}}
	assert.Equal(t, "http://192.0.2.11:8080",
		gatewayURLFromServices(svcs, "envoy-kserve-system-kserve-eg"))
	assert.Empty(t, gatewayURLFromServices(svcs, "no-such-gateway"))
	// A headless Service has no routable address.
	assert.Empty(t, gatewayURLFromServices([]corev1.Service{{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-abc"},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Ports: []corev1.ServicePort{{Port: 80}}},
	}}, "gw"))
}

// TestGatewayResolverFindsService verifies discovery through the API.
func TestGatewayResolverFindsService(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-kserve-system-kserve-eg-abc", Namespace: "yunzhi-system"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.10",
			Ports:     []corev1.ServicePort{{Port: 80}},
		},
	}
	client := &Client{clientset: fake.NewSimpleClientset(svc)}
	resolver := NewGatewayResolver(client, "envoy-kserve-system-kserve-eg", "kserve-system")
	assert.Equal(t, "http://10.0.0.10", resolver.InferenceGatewayURL(context.Background()))

	// An empty gateway name disables discovery.
	assert.Empty(t, NewGatewayResolver(client, "", "").InferenceGatewayURL(context.Background()))
}
