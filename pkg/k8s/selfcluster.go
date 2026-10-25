package k8s

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-taas/go-taas/services/cluster"
)

// acceleratorLabelPrefixes are the label key prefixes that describe the
// node's accelerator, used to suggest valid image-warmup selectors.
var acceleratorLabelPrefixes = []string{"nvidia.com/", "iluvatar.", "metax-tech.com/", "go-taas.com/"}

// SelfClusterProvider reads the identity of the cluster the platform
// runs in from the Kubernetes API: its name, topology region, node count
// and the accelerator label keys present on its nodes.
type SelfClusterProvider struct {
	client *Client
	// name overrides the derived cluster name (cluster.selfName).
	name string
}

// NewSelfClusterProvider builds the provider. name may be empty, in which
// case the name is derived from the cluster identity.
func NewSelfClusterProvider(client *Client, name string) *SelfClusterProvider {
	return &SelfClusterProvider{client: client, name: name}
}

// SelfCluster implements the cluster.SelfClusterProvider seam.
func (p *SelfClusterProvider) SelfCluster(ctx context.Context) (*cluster.SelfClusterInfo, error) {
	if p.client == nil {
		return nil, fmt.Errorf("k8s: no client")
	}
	cs := p.client.Clientset()
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list nodes: %w", err)
	}
	info := &cluster.SelfClusterInfo{
		Name:      p.resolveName(ctx),
		NodeCount: int64(len(nodes.Items)),
	}
	labelSet := map[string]struct{}{}
	for _, node := range nodes.Items {
		if info.Region == "" {
			info.Region = firstNonEmpty(
				node.Labels["topology.kubernetes.io/region"],
				node.Labels["failure-domain.beta.kubernetes.io/region"],
				node.Labels["topology.kubernetes.io/zone"],
			)
		}
		for key := range node.Labels {
			if hasAcceleratorPrefix(key) {
				labelSet[key] = struct{}{}
			}
		}
	}
	labels := make([]string, 0, len(labelSet))
	for key := range labelSet {
		labels = append(labels, key)
	}
	sort.Strings(labels)
	info.AcceleratorLabels = labels
	return info, nil
}

// resolveName returns the configured cluster name, else one derived from
// the cluster identity: a "go-taas.com/cluster-name" node label when the
// operator set one, else the kube-system namespace UID prefix (stable
// across restarts), else the in-cluster API host.
func (p *SelfClusterProvider) resolveName(ctx context.Context) string {
	if p.name != "" {
		return p.name
	}
	cs := p.client.Clientset()
	if nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 50}); err == nil {
		for _, node := range nodes.Items {
			if v := node.Labels["go-taas.com/cluster-name"]; v != "" {
				return v
			}
		}
	}
	if ns, err := cs.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{}); err == nil {
		if uid := string(ns.UID); uid != "" {
			return "cluster-" + shortUID(uid)
		}
	}
	if host := os.Getenv("KUBERNETES_SERVICE_HOST"); host != "" {
		return "cluster-" + strings.ReplaceAll(host, ".", "-")
	}
	return "local-cluster"
}

// shortUID returns the first segment of a Kubernetes UID, enough to make
// the derived name readable and unique.
func shortUID(uid string) string {
	if i := strings.Index(uid, "-"); i > 0 {
		return uid[:i]
	}
	if len(uid) > 8 {
		return uid[:8]
	}
	return uid
}

// hasAcceleratorPrefix reports whether a node label describes an
// accelerator.
func hasAcceleratorPrefix(key string) bool {
	for _, prefix := range acceleratorLabelPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// firstNonEmpty returns the first non-empty value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
