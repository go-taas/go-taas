package accelerator

import (
	"context"

	"github.com/go-taas/go-taas/services/image"
)

// WarmupTasksForNodeProvider returns warmup tasks that targeted a node.
// It is the narrow cross-module read seam (Section 5.3): the accelerator
// module stays free of an image dependency, mirroring the
// model.SetDeleteGuard / image.SetDeleteGuard pattern.
type WarmupTasksForNodeProvider interface {
	// ListWarmupTasksForNode returns warmup tasks whose node_selector
	// matches the node or whose node_results mention the node, newest
	// first, capped at limit.
	ListWarmupTasksForNode(ctx context.Context, nodeID string, limit int) ([]*image.WarmupTask, error)
}
