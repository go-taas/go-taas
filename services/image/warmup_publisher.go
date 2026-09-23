package image

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-taas/go-taas/pkg/mq"
)

// warmupTaskEvent is the task dispatch published on image.warmups after
// the task row is durably written (architecture Section 4.5.1). It
// carries the resolved image reference so the controller never needs a
// database round-trip.
type warmupTaskEvent struct {
	TaskID       string            `json:"task_id"`
	ImageID      string            `json:"image_id"`
	Reference    string            `json:"reference"`
	NodeSelector map[string]string `json:"node_selector"`
	PublishedAt  time.Time         `json:"published_at"`
}

// buildWarmupTaskEvent composes the dispatch event for a task row with
// its resolved image reference.
func buildWarmupTaskEvent(task *WarmupTask, reference string, nodeSelector map[string]string) warmupTaskEvent {
	return warmupTaskEvent{
		TaskID:       task.ID,
		ImageID:      task.ImageID,
		Reference:    reference,
		NodeSelector: nodeSelector,
		PublishedAt:  time.Now().UTC(),
	}
}

// publishWarmupTask publishes the event on the warmups subject. Headers
// carry task_id and image_id for broker-side routing/debugging.
func publishWarmupTask(ctx context.Context, client mq.Client, evt warmupTaskEvent) error {
	body, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	headers := map[string]string{
		"task_id":  evt.TaskID,
		"image_id": evt.ImageID,
	}
	return client.Publish(ctx, mq.DefaultSubjects().ImageWarmups, body, headers)
}
