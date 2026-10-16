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

// Command seed_routinggroups seeds the catalog models the intelligent
// model routing e2e suite (feature #49) needs against the compose
// stack: two real catalog models (self-hosted) that can be candidates
// of a routing group, plus running inference services so the playground
// virtual path can serve them. The compose stack has no
// Controller/Kubernetes, so a service created via the API stays
// "pending" forever; this seed writes the rows directly into
// PostgreSQL so the routing-group playground path has a running
// candidate to serve.
//
// Usage:
//
//	go run ./test/e2e/seed/routinggroups -dsn "postgres://taas:taas@postgres:5432/taas?sslmode=disable" -org org-default
package main

import (
	"database/sql"
	"flag"
	"log"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := flag.String("dsn", "postgres://taas:taas@postgres:5432/taas?sslmode=disable", "PostgreSQL DSN")
	org := flag.String("org", "org-default", "organization that owns the services")
	flag.Parse()

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping: %v", err)
	}

	// Two real catalog models that can be routing-group candidates.
	// The "aaa-" prefix keeps them on the first page of the admin
	// routing-groups candidate picker (the persisted compose database
	// accumulates many models across runs).
	models := []struct {
		id   string
		name string
	}{
		{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "aaa-routing-cand-a"},
		{"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "aaa-routing-cand-b"},
	}
	for _, m := range models {
		if _, err := db.Exec(`
			INSERT INTO models (id, name, description, external, provider_id, upstream_model_id, published, created_at, updated_at)
			VALUES ($1, $2, 'e2e routing candidate', false, '', '', true, now(), now())
			ON CONFLICT (id) DO UPDATE SET name = $2, updated_at = now()`, m.id, m.name); err != nil {
			log.Fatalf("seed model %s: %v", m.name, err)
		}
		if _, err := db.Exec(`
			INSERT INTO model_versions (id, model_id, version, weight_path, is_active, created_at)
			VALUES (gen_random_uuid(), $1, 'v1', 'models/routing-cand/v1/', true, now())
			ON CONFLICT (model_id, version) DO UPDATE SET is_active = true`, m.id); err != nil {
			log.Fatalf("seed model version %s: %v", m.name, err)
		}
	}

	// A running inference service for each candidate so the playground
	// virtual path has a running candidate to serve.
	for _, m := range models {
		svcID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
		if m.id == models[1].id {
			svcID = "dddddddd-dddd-dddd-dddd-dddddddddddd"
		}
		if _, err := db.Exec(`
			INSERT INTO inference_services
				(id, organization_id, name, model_id, model_version, image_id, replicas,
				 accelerator, accelerator_type, state, endpoints, autoscaling,
				 autoscaling_state, autoscaling_current_replicas, autoscaling_desired_replicas,
				 autoscaling_current_concurrency, autoscaling_target_concurrency,
				 autoscaling_error_reason, created_at, updated_at)
			VALUES ($1, $3, $2, $4, 'v1', 'img-1', 1,
				'nvidia', 'A800', 'running', '[]'::jsonb, '{}'::jsonb,
				'', 1, 1, 0, 0, '', now(), now())
			ON CONFLICT (id) DO UPDATE
				SET state = 'running', organization_id = $3, updated_at = now()`,
			svcID, "routing-cand-svc-"+m.name, *org, m.id); err != nil {
			log.Fatalf("seed service for %s: %v", m.name, err)
		}
	}

	log.Printf("seeded routing candidates %s and %s with running services (org %s)", models[0].name, models[1].name, *org)
}
