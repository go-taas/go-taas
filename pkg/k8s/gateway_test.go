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
			ObjectMeta: metav1.ObjectMeta{Name: "envoy-aibrix-system-aibrix-eg-903790dc"},
			Spec: corev1.ServiceSpec{
				ClusterIP: "10.43.253.124",
				Ports: []corev1.ServicePort{
					{Name: "admin", Port: 19001},
					{Name: "http", Port: 80},
				},
			},
		},
	}
	assert.Equal(t, "http://10.43.253.124:80",
		gatewayURLFromServices(svcs, "envoy-aibrix-system-aibrix-eg"))

	// A load-balancer address wins: the quickstart snippet is copied by a
	// tenant outside the cluster.
	svcs[1].Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "10.86.56.191"}}
	assert.Equal(t, "http://10.86.56.191:80",
		gatewayURLFromServices(svcs, "envoy-aibrix-system-aibrix-eg"))
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
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-aibrix-system-aibrix-eg-abc", Namespace: "yunzhi-system"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.43.253.124",
			Ports:     []corev1.ServicePort{{Port: 80}},
		},
	}
	client := &Client{clientset: fake.NewSimpleClientset(svc)}
	resolver := NewGatewayResolver(client, "envoy-aibrix-system-aibrix-eg", "aibrix-system")
	assert.Equal(t, "http://10.43.253.124:80", resolver.InferenceGatewayURL(context.Background()))

	// An empty gateway name disables discovery.
	assert.Empty(t, NewGatewayResolver(client, "", "").InferenceGatewayURL(context.Background()))
}
