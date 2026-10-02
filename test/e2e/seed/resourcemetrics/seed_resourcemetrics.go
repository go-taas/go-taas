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

// Command seed_resourcemetrics seeds service_resource_metrics rows
// directly into the compose stack's PostgreSQL so the service-resource-
// metrics e2e suite (feature #37) can exercise the populated states of
// the admin Service Metrics page (/admin/services/:serviceId/metrics)
// against the compose stack.
//
// The compose stack's Controller is the only writer of
// service_resource_metrics through its per-service sampler, but the
// compose stack has no running inference pods carrying the
// go-taas.io/service-id and go-taas.io/replica-index labels, so the
// sampler writes nothing. This seed writes the rows directly into
// PostgreSQL (the resourcemetrics module is a read-only aggregation over
// service_resource_metrics, so seeding the table is the only way to
// populate the page).
//
// Usage:
//
//	go run ./test/e2e/seed/resourcemetrics/seed_resourcemetrics.go -dsn "postgres://taas:taas@postgres:5432/taas?sslmode=disable" -service "11111111-1111-1111-1111-111111111111"
//
// The default DSN targets the compose network (service name "postgres").
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := flag.String("dsn", "postgres://taas:taas@postgres:5432/taas?sslmode=disable", "PostgreSQL DSN")
	service := flag.String("service", "11111111-1111-1111-1111-111111111111", "inference service id")
	clear := flag.Bool("clear", false, "delete all service_resource_metrics rows for the given service (for the empty-state e2e case)")
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
		if _, err := db.Exec(`DELETE FROM service_resource_metrics WHERE service_id = $1`, *service); err != nil {
			log.Fatalf("clear service_resource_metrics: %v", err)
		}
		log.Printf("cleared service_resource_metrics for service %s", *service)
		return
	}

	// Delete the service's existing rows first so the seed is idempotent
	// across runs.
	if _, err := db.Exec(`DELETE FROM service_resource_metrics WHERE service_id = $1`, *service); err != nil {
		log.Fatalf("clear service_resource_metrics: %v", err)
	}

	// Ensure the inference service exists so the resourcemetrics module's
	// service-existence check (10301) passes. The service is created with
	// the minimal columns the infer module reads.
	if _, err := db.Exec(`
		INSERT INTO inference_services
			(id, organization_id, name, model_id, model_version, image_id,
			 replicas, accelerator, accelerator_type, state, endpoints,
			 autoscaling, autoscaling_state, autoscaling_current_replicas,
			 autoscaling_desired_replicas, autoscaling_current_concurrency,
			 autoscaling_target_concurrency, autoscaling_error_reason,
			 created_at, updated_at)
		VALUES ($1, 'org-default', 'e2e-metrics-svc', '11111111-1111-1111-1111-111111111111',
			'v1', 'img-vllm-nvidia-v063', 2, 'nvidia', 'gpu', 'running',
			'[]', '{}', '', 0, 0, 0, 0, '', now(), now())
		ON CONFLICT (id) DO NOTHING`, *service); err != nil {
		log.Fatalf("seed inference service: %v", err)
	}

	// Seed sample rows across the last 3 hours so the default 24h range
	// shows data. Two replicas (replica-1, replica-2) with distinct CPU /
	// memory / GPU values so the per-replica breakdown and the metric
	// switcher are exercised. GPU is best-effort: replica-1 carries a
	// gpu_percent, replica-2 carries NULL so the GPU card shows
	// "Unavailable" only when no replica has GPU data (the page derives
	// hasGPU from the series/cards).
	now := time.Now().UTC().Truncate(time.Hour)
	gpu := 42.0
	seq := 0
	seed := func(replica string, at time.Time, cpu float64, mem int64, gpuVal *float64) {
		seq++
		id := uuid.NewString()
		if _, err := db.Exec(`
			INSERT INTO service_resource_metrics
				(id, service_id, replica_index, sampled_at, cpu_percent, memory_bytes, gpu_percent)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO NOTHING`,
			id, *service, replica, at, cpu, mem, gpuVal); err != nil {
			log.Fatalf("seed service_resource_metrics: %v", err)
		}
	}

	// replica-1: rising CPU, moderate memory, GPU present.
	seed("replica-1", now.Add(-3*time.Hour), 50, 1024, &gpu)
	seed("replica-1", now.Add(-2*time.Hour), 60, 1536, &gpu)
	seed("replica-1", now.Add(-1*time.Hour), 70, 2048, &gpu)
	// replica-2: higher CPU, more memory, GPU absent (best-effort NULL).
	seed("replica-2", now.Add(-3*time.Hour), 70, 2048, nil)
	seed("replica-2", now.Add(-2*time.Hour), 80, 2560, nil)
	seed("replica-2", now.Add(-1*time.Hour), 90, 3072, nil)

	log.Printf("seeded service_resource_metrics for service %s (2 replicas, 3 buckets)", *service)
	_ = fmt.Sprintf
}
