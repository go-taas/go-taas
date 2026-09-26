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

// Command seed_loadtest seeds a running inference service pointing at a
// mock OpenAI-compatible endpoint so the load-testing e2e suite (feature
// #20) can exercise the populated states of the admin Load Tests pages
// against the compose stack, which has no Controller/Kubernetes to set a
// service to running.
//
// The compose stack's taas-server is the only writer of inference_services
// through the API, but a created service stays "pending" forever without a
// controller. This seed writes the running row directly into PostgreSQL so
// CreateLoadTest has a valid target (AD4: state=running with an endpoint).
//
// Usage:
//
//	go run ./test/e2e/seed/seed_loadtest.go -dsn "postgres://taas:taas@postgres:5432/taas?sslmode=disable" -endpoint "http://mock-infer:8000"
//
// The default DSN targets the compose network (service name "postgres").
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
	endpoint := flag.String("endpoint", "http://mock-infer:8000", "mock inference endpoint")
	org := flag.String("org", "org-default", "organization that owns the service")
	clear := flag.Bool("clear", false, "delete all load_tests rows (for the empty-state e2e case)")
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
		if _, err := db.Exec(`DELETE FROM load_tests`); err != nil {
			log.Fatalf("clear load_tests: %v", err)
		}
		log.Printf("cleared load_tests table")
		return
	}

	// Ensure a model exists for the service to reference.
	modelID := "11111111-1111-1111-1111-111111111111"
	if _, err := db.Exec(`
		INSERT INTO models (id, name, description, created_at, updated_at)
		VALUES ($1, 'loadtest-model', 'e2e load-testing model', now(), now())
		ON CONFLICT (id) DO NOTHING`, modelID); err != nil {
		log.Fatalf("seed model: %v", err)
	}

	// Upsert a running inference service pointing at the mock endpoint.
	serviceID := "22222222-2222-2222-2222-222222222222"
	_, err = db.Exec(`
		INSERT INTO inference_services
			(id, organization_id, name, model_id, model_version, image_id, replicas,
			 accelerator, accelerator_type, state, endpoints, autoscaling,
			 autoscaling_state, autoscaling_current_replicas, autoscaling_desired_replicas,
			 autoscaling_current_concurrency, autoscaling_target_concurrency,
			 autoscaling_error_reason, created_at, updated_at)
		VALUES ($1, $4, 'loadtest-svc', $2, 'v1', 'img-1', 1,
		        'nvidia', 'A800', 'running', $3::jsonb, '{}'::jsonb,
		        '', 1, 1, 0, 0, '', now(), now())
		ON CONFLICT (id) DO UPDATE
			SET state = 'running', endpoints = $3::jsonb, organization_id = $4, updated_at = now()`,
		serviceID, modelID, `["`+*endpoint+`"]`, *org)
	if err != nil {
		log.Fatalf("seed service: %v", err)
	}

	log.Printf("seeded running inference service %s -> %s (model %s, org %s)", serviceID, *endpoint, modelID, *org)
	_ = time.Now()
}
