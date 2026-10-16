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

// Command seed_optimization seeds the usage/price/service fixtures the
// cost-optimization recommendations e2e suite (feature #50) needs
// against the compose stack: three catalog models (expensive, cheap,
// mid), the price matrix, priced charge records, offline-shaped request
// logs and an idle running service, so the engine's R1/R2/R3/R4 rules
// fire deterministically after a refresh.
//
// Usage:
//
//	go run ./test/e2e/seed/optimization -dsn "postgres://taas:taas@postgres:5432/taas?sslmode=disable" -org org-default
package main

import (
	"database/sql"
	"flag"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := flag.String("dsn", "postgres://taas:taas@postgres:5432/taas?sslmode=disable", "PostgreSQL DSN")
	org := flag.String("org", "org-default", "organization that owns the data")
	flag.Parse()

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping: %v", err)
	}

	// Three catalog models: model-a (expensive), model-b (cheap, the
	// best R1 candidate), model-c (mid-priced, the R1 runner-up).
	models := []struct {
		id   string
		name string
	}{
		{"11111111-1111-1111-1111-111111111111", "aaa-opt-model-a"},
		{"22222222-2222-2222-2222-222222222222", "aaa-opt-model-b"},
		{"33333333-3333-3333-3333-333333333333", "aaa-opt-model-c"},
	}
	for _, m := range models {
		if _, err := db.Exec(`
			INSERT INTO models (id, name, description, external, provider_id, upstream_model_id, published, created_at, updated_at)
			VALUES ($1, $2, 'e2e optimization model', false, '', '', true, now(), now())
			ON CONFLICT (id) DO UPDATE SET name = $2, updated_at = now()`, m.id, m.name); err != nil {
			log.Fatalf("seed model %s: %v", m.name, err)
		}
		if _, err := db.Exec(`
			INSERT INTO model_versions (id, model_id, version, weight_path, is_active, created_at)
			VALUES (gen_random_uuid(), $1, 'v1', 'models/opt/v1/', true, now())
			ON CONFLICT (model_id, version) DO UPDATE SET is_active = true`, m.id); err != nil {
			log.Fatalf("seed model version %s: %v", m.name, err)
		}
	}

	// The price matrix: model-a expensive, model-b cheap, model-c mid.
	now := time.Now().UTC().Unix()
	prices := []struct {
		model string
		in    float64
		out   float64
		cache float64
	}{
		{models[0].id, 10, 20, 1},
		{models[1].id, 1, 2, 0.1},
		{models[2].id, 4, 8, 0.4},
	}
	for _, p := range prices {
		if _, err := db.Exec(`
			INSERT INTO price_entries (id, model_id, accelerator_type, input_price_per_million, output_price_per_million, cached_price_per_million, currency, effective_from, tiers_json, created_at, updated_at)
			VALUES (gen_random_uuid(), $1, 'default', $2, $3, $4, 'CNY', $5, '[]'::jsonb, now(), now())
			ON CONFLICT (model_id, accelerator_type, effective_from) DO UPDATE SET input_price_per_million = $2, output_price_per_million = $3, cached_price_per_million = $4`,
			p.model, p.in, p.out, p.cache, now-86400); err != nil {
			log.Fatalf("seed price for %s: %v", p.model, err)
		}
	}

	// A priced charge record on model-a with enough requests/tokens for
	// R1/R2/R4 eligibility against the shipped default thresholds
	// (R1: 1000 requests, 10M billed tokens, ¥100 saving; R2: 1000
	// requests; R4: 1M prompt tokens).
	hour := now - 3600
	if _, err := db.Exec(`
		INSERT INTO charge_records (id, organization_id, api_key_id, model_id, accelerator_type, period_start, period_end, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, request_count, amount, currency, price_id, tier_index, priced, month_to_date_tokens, charged_at)
		VALUES (gen_random_uuid(), $1, 'key-1', $2, 'default', $3, $4, 10000000, 5000000, 0, 0, 1000, 5000, 'CNY', NULL, -1, true, 0, now())
		ON CONFLICT (api_key_id, model_id, accelerator_type, period_start) DO UPDATE SET request_count = 1000, prompt_tokens = 10000000, completion_tokens = 5000000, amount = 5000`,
		*org, models[0].id, hour, hour+3600); err != nil {
		log.Fatalf("seed charge: %v", err)
	}

	// Offline-shaped request logs (outside the peak window) with large
	// prompts for the R2 batch rule and R4 cache rule. 1200 rows cross
	// the R2 minRequests (1000) and R4 minPromptTokens (1M).
	offPeak := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`
		INSERT INTO request_logs (id, request_id, organization_id, api_key_id, model_id, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, latency_ms, status, error, created_at)
		SELECT gen_random_uuid(), 'req-opt-' || g, $1, 'key-1', $2, 1024, 64, 0, 0, 100, 'success', '', $3::timestamptz + (g || ' minutes')::interval
		FROM generate_series(1, 1200) AS g
		ON CONFLICT (request_id) DO NOTHING`,
		*org, models[0].id, offPeak); err != nil {
		log.Fatalf("seed request logs: %v", err)
	}

	// An idle running service with autoscaling off (R3 candidate) and a
	// vllm image (R4 engine gate).
	if _, err := db.Exec(`
		INSERT INTO images (id, name, tag, digest, accelerator, engine, description, created_at, updated_at)
		VALUES ('img-vllm-1', 'ghcr.io/go-taas/vllm', 'v0.6.3', '', 'nvidia', 'vllm', '', now(), now())
		ON CONFLICT DO NOTHING`); err != nil {
		log.Fatalf("seed image: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO inference_services
			(id, organization_id, name, model_id, model_version, image_id, replicas,
			 accelerator, accelerator_type, state, endpoints, autoscaling,
			 autoscaling_state, autoscaling_current_replicas, autoscaling_desired_replicas,
			 autoscaling_current_concurrency, autoscaling_target_concurrency,
			 autoscaling_error_reason, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'svc-idle', $2, 'v1', 'img-vllm-1', 2,
			'nvidia', 'A800', 'running', '[]'::jsonb, '{}'::jsonb,
			'', 2, 2, 0, 0, '', now(), now())
		ON CONFLICT (organization_id, name) DO UPDATE SET state = 'running', organization_id = $1, updated_at = now()`,
		*org, models[0].id); err != nil {
		log.Fatalf("seed idle service: %v", err)
	}
	// The global autoscaling policy: on but scale-to-zero off.
	if _, err := db.Exec(`
		INSERT INTO autoscaling_policy (id, enabled, min_replicas, max_replicas, target_concurrency, scale_to_zero, cooldown_seconds, updated_at)
		VALUES (1, true, 1, 10, 32, false, 300, now())
		ON CONFLICT (id) DO UPDATE SET enabled = true, scale_to_zero = false`); err != nil {
		log.Fatalf("seed autoscaling policy: %v", err)
	}

	log.Printf("seeded optimization fixtures for org %s (models a/b/c, prices, charges, request logs, idle service)", *org)
}
