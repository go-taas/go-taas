package infer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/mq"
)

func TestConcurrencyConsumerAppliesReport(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	svc := seedService(t, repo, "org", "svc", StateRunning, time.Now())
	consumer := NewConcurrencyConsumer(mq.NewFake(), repo, 1)

	body, err := json.Marshal(concurrencyReport{ServiceID: svc.ID, InFlight: 12})
	require.NoError(t, err)
	require.NoError(t, consumer.handle(context.Background(), mq.Message{Body: body}))

	row, err := repo.FindByIDAndOrganization(context.Background(), "org", svc.ID)
	require.NoError(t, err)
	assert.Equal(t, 12, row.AutoscalingCurrentConcurrency)
}

func TestConcurrencyConsumerMalformed(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	consumer := NewConcurrencyConsumer(mq.NewFake(), repo, 1)

	// Malformed body is skipped, not retried.
	require.NoError(t, consumer.handle(context.Background(), mq.Message{Body: []byte("{not json")}))
	// Missing service id is skipped.
	body, _ := json.Marshal(concurrencyReport{InFlight: 5})
	require.NoError(t, consumer.handle(context.Background(), mq.Message{Body: body}))
}

func TestConcurrencyConsumerUnknownService(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	consumer := NewConcurrencyConsumer(mq.NewFake(), repo, 1)

	body, _ := json.Marshal(concurrencyReport{ServiceID: "missing", InFlight: 5})
	// Unknown service is skipped (late report), not retried.
	require.NoError(t, consumer.handle(context.Background(), mq.Message{Body: body}))
}

func TestModelAutoscalingProvider(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	provider := NewModelAutoscalingProvider(db)

	// No ready service: nil projection.
	proj, err := provider.ModelAutoscaling(context.Background(), "org", "model-1")
	require.NoError(t, err)
	assert.Nil(t, proj)

	// A running service with autoscaling enabled.
	svc := seedService(t, repo, "org", "svc", StateRunning, time.Now())
	svc.ModelID = "model-1"
	svc.AutoscalingState = "steady"
	svc.AutoscalingCurrentReplicas = 3
	require.NoError(t, repo.UpdateFields(context.Background(), svc.ID, map[string]any{
		"model_id":                     "model-1",
		"autoscaling_state":            "steady",
		"autoscaling_current_replicas": 3,
		"autoscaling":                  `{"enabled":true,"min_replicas":1,"max_replicas":10,"target_concurrency":32,"scale_to_zero":false,"cooldown_seconds":300}`,
	}))

	proj, err = provider.ModelAutoscaling(context.Background(), "org", "model-1")
	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.True(t, proj.GetAutoscaled())
	assert.Equal(t, int32(3), proj.GetCurrentReplicas())
	assert.Equal(t, int32(1), proj.GetMinReplicas())
	assert.Equal(t, int32(10), proj.GetMaxReplicas())
	assert.Equal(t, "steady", proj.GetState())
}

func TestUserAutoscalingState(t *testing.T) {
	svc := &InferenceService{}
	assert.Equal(t, "fixed", userAutoscalingState(svc, false))

	svc.AutoscalingState = "scaled-to-zero"
	assert.Equal(t, "scaled-to-zero", userAutoscalingState(svc, true))

	svc.AutoscalingState = "cold-starting"
	assert.Equal(t, "warming-up", userAutoscalingState(svc, true))

	svc.AutoscalingState = "scaling-up"
	assert.Equal(t, "scaling", userAutoscalingState(svc, true))

	svc.AutoscalingState = "scaling-down"
	assert.Equal(t, "scaling", userAutoscalingState(svc, true))

	svc.AutoscalingState = "steady"
	assert.Equal(t, "steady", userAutoscalingState(svc, true))
}
