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

// Command seed_routing seeds the routing-policy states the e2e suite
// (feature #45) needs against the compose stack: a catalog model with
// an active version plus running and pending inference services of
// that exact model/version. The compose stack has no
// Controller/Kubernetes, so a service created via the API stays
// "pending" forever; this seed writes the rows directly into
// PostgreSQL so the admin Routing Policies page shows DEFAULT,
// ENABLED and UNAVAILABLE states.
//
// Usage:
//
//	go run ./test/e2e/seed/routing.go -dsn "postgres://taas:taas@postgres:5432/taas?sslmode=disable" -org org-default
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

	// A catalog model with an active version (AD2: policies pin the
	// active version).
	modelID := "33333333-3333-3333-3333-333333333333"
	if _, err := db.Exec(`
		INSERT INTO models (id, name, description, created_at, updated_at)
		VALUES ($1, 'routing-model', 'e2e routing-policy model', now(), now())
		ON CONFLICT (id) DO NOTHING`, modelID); err != nil {
		log.Fatalf("seed model: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO model_versions (id, model_id, version, weight_path, is_active, created_at)
		VALUES (gen_random_uuid(), $1, 'v1', 'models/routing-model/v1/', true, now())
		ON CONFLICT (model_id, version) DO UPDATE SET is_active = true`, modelID); err != nil {
		log.Fatalf("seed model version: %v", err)
	}

	// A running service (ready target) and a pending one, both of the
	// exact model/version.
	runningID := "44444444-4444-4444-4444-444444444444"
	pendingID := "55555555-5555-5555-5555-555555555555"
	for _, svc := range []struct {
		id    string
		name  string
		state string
	}{{runningID, "routing-svc-running", "running"}, {pendingID, "routing-svc-pending", "pending"}} {
		if _, err := db.Exec(`
			INSERT INTO inference_services
				(id, organization_id, name, model_id, model_version, image_id, replicas,
				 accelerator, accelerator_type, state, endpoints, autoscaling,
				 autoscaling_state, autoscaling_current_replicas, autoscaling_desired_replicas,
				 autoscaling_current_concurrency, autoscaling_target_concurrency,
				 autoscaling_error_reason, created_at, updated_at)
			VALUES ($1, $3, $2, $4, 'v1', 'img-1', 1,
				'nvidia', 'A800', $5, '[]'::jsonb, '{}'::jsonb,
				'', 1, 1, 0, 0, '', now(), now())
			ON CONFLICT (id) DO UPDATE
				SET state = $5, organization_id = $3, updated_at = now()`,
			svc.id, svc.name, *org, modelID, svc.state); err != nil {
			log.Fatalf("seed service %s: %v", svc.name, err)
		}
	}

	log.Printf("seeded routing model %s with running %s and pending %s services (org %s)", modelID, runningID, pendingID, *org)
}
