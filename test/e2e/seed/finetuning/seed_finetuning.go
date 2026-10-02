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

// Command seed_finetuning seeds fine-tuning datasets and jobs directly
// into the compose stack's PostgreSQL so the model-fine-tuning e2e suite
// (feature #39) can exercise the populated states of the admin
// Fine-tuning pages (/admin/finetuning, /admin/finetuning/:jobId)
// against the compose stack.
//
// The compose stack's Controller fine-tuning job executor is not wired,
// so jobs created through the API stay pending forever. This seed writes
// datasets and jobs directly into PostgreSQL (the finetuning module reads
// the datasets and finetuning_jobs tables), including a succeeded job so
// the deploy flow can be exercised.
//
// Usage:
//
//	go run ./test/e2e/seed/finetuning/seed_finetuning.go -dsn "postgres://taas:taas@postgres:5432/taas?sslmode=disable"
//
// The default DSN targets the compose network (service name "postgres").
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := flag.String("dsn", "postgres://taas:taas@postgres:5432/taas?sslmode=disable", "PostgreSQL DSN")
	clear := flag.Bool("clear", false, "delete all seeded datasets and jobs (for the empty-state e2e case)")
	flag.Parse()

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping: %v", err)
	}

	if *clear {
		if _, err := db.Exec(`DELETE FROM finetuning_jobs`); err != nil {
			log.Fatalf("clear finetuning_jobs: %v", err)
		}
		if _, err := db.Exec(`DELETE FROM datasets`); err != nil {
			log.Fatalf("clear datasets: %v", err)
		}
		log.Printf("cleared finetuning datasets and jobs")
		return
	}

	// Delete existing seeded rows first so the seed is idempotent across
	// runs (the finetuning tables are not org-scoped).
	if _, err := db.Exec(`DELETE FROM finetuning_jobs`); err != nil {
		log.Fatalf("clear finetuning_jobs: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM datasets`); err != nil {
		log.Fatalf("clear datasets: %v", err)
	}
	// The deploy flow (AC4) creates an inference service named after the
	// succeeded job; delete it so a re-run can deploy again (the deploy
	// returns 10302 when the service already exists).
	if _, err := db.Exec(`DELETE FROM inference_services WHERE name = 'e2e-succeeded-job'`); err != nil {
		log.Fatalf("clear deployed e2e-succeeded-job service: %v", err)
	}

	// Ensure the base model and its version exist so the finetuning
	// module's model resolver (10101) passes.
	baseModelID := "11111111-1111-1111-1111-111111111111"
	if _, err := db.Exec(`
		INSERT INTO models (id, name, description, created_at, updated_at)
		VALUES ($1, 'e2e-base-model', 'e2e fine-tuning base model', now(), now())
		ON CONFLICT (id) DO NOTHING`, baseModelID); err != nil {
		log.Fatalf("seed base model: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO model_versions (id, model_id, version, weight_path, is_active, created_at)
		VALUES ('99999999-0000-0000-0000-000000000001', $1, 'v1', 'models/e2e-base-model/v1/', true, now())
		ON CONFLICT (model_id, version) DO NOTHING`, baseModelID); err != nil {
		log.Fatalf("seed base model version: %v", err)
	}

	// Seed a dataset.
	datasetID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	if _, err := db.Exec(`
		INSERT INTO datasets (id, name, format, object_path, created_at)
		VALUES ($1, 'e2e-train', 'jsonl', 'data/e2e-train.jsonl', now())
		ON CONFLICT (id) DO NOTHING`, datasetID); err != nil {
		log.Fatalf("seed dataset: %v", err)
	}

	// Seed a pending job (the compose stack has no controller executor, so
	// it stays pending) and a succeeded job (so the deploy flow can be
	// exercised). Hyperparameters are stored as jsonb.
	now := time.Now().UTC()
	pendingID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	succeededID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	seedJob := func(id, name, state string, at time.Time) {
		if _, err := db.Exec(`
			INSERT INTO finetuning_jobs
				(id, name, base_model_id, base_model_version, dataset_id,
				 hyperparameters, state, failure_reason, fine_tuned_model_id, created_at, updated_at)
			VALUES ($1, $2, '11111111-1111-1111-1111-111111111111', 'v1', $3,
				'{"epochs":3,"batch_size":4,"learning_rate":0.001}', $4, '', '', $5, $6)
			ON CONFLICT (id) DO NOTHING`,
			id, name, datasetID, state, at, at); err != nil {
			log.Fatalf("seed job: %v", err)
		}
	}
	seedJob(pendingID, "e2e-pending-job", "pending", now.Add(-2*time.Hour))
	seedJob(succeededID, "e2e-succeeded-job", "succeeded", now.Add(-1*time.Hour))

	log.Printf("seeded finetuning dataset %s and jobs %s (pending), %s (succeeded)", datasetID, pendingID, succeededID)
	_ = fmt.Sprintf
}
