package k8s

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// NodeMatcher validates node selectors against the live node set and can
// describe the accelerator labels the cluster actually carries.
type NodeMatcher struct {
	client *Client
}

// NewNodeMatcher builds a NodeMatcher.
func NewNodeMatcher(client *Client) *NodeMatcher {
	return &NodeMatcher{client: client}
}

// MatchNodes returns how many nodes match the selector and, when the
// selector matches nothing, the accelerator label key=value pairs
// available in the cluster so the caller can tell the user which
// selector a node actually carries.
func (m *NodeMatcher) MatchNodes(ctx context.Context, selector map[string]string) (int, []string, error) {
	if m.client == nil {
		return 0, nil, fmt.Errorf("k8s: no client")
	}
	nodes, err := m.client.Clientset().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, nil, fmt.Errorf("k8s: list nodes: %w", err)
	}
	if len(selector) == 0 {
		return len(nodes.Items), nil, nil
	}
	set := labels.SelectorFromSet(selector)
	matched := 0
	for _, node := range nodes.Items {
		if set.Matches(labels.Set(node.Labels)) {
			matched++
		}
	}
	if matched > 0 {
		return matched, nil, nil
	}
	return 0, availableAcceleratorLabels(nodes.Items), nil
}

// availableAcceleratorLabels collects the accelerator label key=value
// pairs present on the nodes, sorted and de-duplicated (e.g.
// "nvidia.com/gpu.product=NVIDIA-GeForce-RTX-4090").
func availableAcceleratorLabels(nodes []corev1.Node) []string {
	seen := map[string]struct{}{}
	for _, node := range nodes {
		for key, value := range node.Labels {
			if !selectableAcceleratorLabel(key) {
				continue
			}
			if value == "" {
				continue
			}
			seen[key+"="+value] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for pair := range seen {
		out = append(out, pair)
	}
	sort.Strings(out)
	return out
}
