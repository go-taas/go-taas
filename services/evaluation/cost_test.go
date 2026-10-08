// Copyright 2025 The go-taas Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package evaluation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/go-taas/go-taas/pkg/config"
	evaluationv1 "github.com/go-taas/go-taas/proto/taas/evaluation/v1"
)

// fakePrices is a scriptable PriceReader.
type fakePrices struct {
	entry *PriceEntry
	err   error
}

func (f *fakePrices) ApplicablePrice(_ context.Context, _, _ string, _ int64) (*PriceEntry, error) {
	return f.entry, f.err
}

func TestEstimateCaseCost(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Nil reader -> unpriced.
	var nilReader *fakePrices
	_ = nilReader
	a := NewDefaultCostAttributor(nil)
	cost, priced := a.EstimateCaseCost(ctx, "model-a", "default", 1000, 2000, at)
	require.False(t, priced)
	require.Zero(t, cost)

	// Reader error -> unpriced.
	a = NewDefaultCostAttributor(&fakePrices{err: errors.New("boom")})
	cost, priced = a.EstimateCaseCost(ctx, "model-a", "default", 1000, 2000, at)
	require.False(t, priced)
	require.Zero(t, cost)

	// No applicable entry -> unpriced.
	a = NewDefaultCostAttributor(&fakePrices{})
	cost, priced = a.EstimateCaseCost(ctx, "model-a", "default", 1000, 2000, at)
	require.False(t, priced)
	require.Zero(t, cost)

	// Priced: 1000 in @ 3.0/M + 2000 out @ 15.0/M = 0.003 + 0.03 =
	// 0.033 -> rounds to 0.03 -> 3 minor units.
	a = NewDefaultCostAttributor(&fakePrices{entry: &PriceEntry{InputPricePerMillion: 3, OutputPricePerMillion: 15}})
	cost, priced = a.EstimateCaseCost(ctx, "model-a", "default", 1000, 2000, at)
	require.True(t, priced)
	require.Equal(t, int64(3), cost)

	// Zero tokens still priced at 0.
	cost, priced = a.EstimateCaseCost(ctx, "model-a", "default", 0, 0, at)
	require.True(t, priced)
	require.Zero(t, cost)
}

func TestDBPriceReaderApplicablePrice(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:TestDBPriceReader?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE price_entries (
		model_id TEXT, accelerator_type TEXT, effective_from INTEGER,
		input_price_per_million REAL, output_price_per_million REAL, cached_price_per_million REAL)`).Error)
	now := time.Now().UTC()
	insert := func(effectiveFrom int64, in, out float64) {
		require.NoError(t, db.Exec(`INSERT INTO price_entries
			(model_id, accelerator_type, effective_from, input_price_per_million, output_price_per_million, cached_price_per_million)
			VALUES ('model-a', 'default', ?, ?, ?, 0)`, effectiveFrom, in, out).Error)
	}
	insert(now.Add(-48*time.Hour).Unix(), 1, 2) // older entry
	insert(now.Add(-1*time.Hour).Unix(), 3, 4)  // effective entry
	insert(now.Add(1*time.Hour).Unix(), 9, 9)   // future entry, must be ignored

	reader := NewDBPriceReader(db)
	entry, err := reader.ApplicablePrice(context.Background(), "model-a", "default", now.Unix())
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Equal(t, float64(3), entry.InputPricePerMillion)
	require.Equal(t, float64(4), entry.OutputPricePerMillion)

	// No entry for an unknown model.
	entry, err = reader.ApplicablePrice(context.Background(), "model-b", "default", now.Unix())
	require.NoError(t, err)
	require.Nil(t, entry)

	// The default estimator over the DB reader prices a call.
	a := NewDefaultCostAttributor(reader)
	cost, priced := a.EstimateCaseCost(context.Background(), "model-a", "default", 1_000_000, 1_000_000, now)
	require.True(t, priced)
	require.Equal(t, int64(700), cost) // 3 + 4 per 1M tokens
}

func TestRetentionRunnerPurgesExpired(t *testing.T) {
	db := newRunnerTestDB(t)
	setTestConfig(t)
	svc := newTestService(db)
	ctx := newServiceTestCtx("org-a")

	// Create a suite + case + run, then execute it so rows exist.
	id := mustCreateSuite(ctx, t, svc, "s")
	mustAddCase(ctx, t, svc, id, "c1")
	svc.SetCompletionProvider(&fakeCompletions{result: &CompletionResult{Completion: "42"}})
	svc.SetAPIKeyValidator(&fakeAPIKeys{valid: map[string]bool{"key-1": true}})
	_, err := svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 1})
	require.NoError(t, err)
	runner := NewRunner(svc)
	_, err = runner.sweep(context.Background())
	require.NoError(t, err)

	repo := NewRepository(db)
	// Nothing expired yet: the run stays.
	rr := NewRetentionRunner(repo, time.Hour, time.Minute)
	rr.RunOnce(context.Background())
	var runs int64
	require.NoError(t, db.Model(&EvaluationRun{}).Count(&runs).Error)
	require.Equal(t, int64(1), runs)

	// Expire it: retained_until in the past -> purged.
	require.NoError(t, db.Exec(`UPDATE evaluation_runs SET retained_until = ?`, time.Now().UTC().Add(-time.Minute)).Error)
	rr.RunOnce(context.Background())
	require.NoError(t, db.Model(&EvaluationRun{}).Count(&runs).Error)
	require.Zero(t, runs)
	var cases int64
	require.NoError(t, db.Model(&EvaluationRunCase{}).Count(&cases).Error)
	require.Zero(t, cases)
}

func TestRetentionRunnerFromConfigDefaults(t *testing.T) {
	config.SetConfigForTest(&config.Configuration{})
	t.Cleanup(func() { config.SetConfigForTest(nil) })

	db := newRunnerTestDB(t)
	rr := NewRetentionRunnerFromConfig(nil, NewRepository(db))
	require.Equal(t, "evaluation-retention", rr.RunnerName())

	// Non-positive ttl/interval fall back to defaults.
	rr2 := NewRetentionRunner(NewRepository(db), 0, 0)
	require.Equal(t, 90*24*time.Hour, rr2.ttl)
	require.Equal(t, time.Hour, rr2.interval)
}
