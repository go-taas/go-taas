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

// Command seed_clusters seeds inference clusters directly into the
// compose stack's PostgreSQL so the multi-cluster-management e2e suite
// (feature #40) can exercise the populated states of the admin Cluster
// pages (/admin/clusters, /admin/clusters/:clusterId) against the
// compose stack.
//
// The compose stack's Controller per-cluster health collection loop is
// not wired, so the cluster-health projection stays empty (health
// "unknown", nodeCount 0) and the workload provider returns no services.
// This seed writes cluster rows directly into PostgreSQL (the cluster
// module reads the clusters table).
//
// Usage:
//
//	go run ./test/e2e/seed/clusters/seed_clusters.go -dsn "postgres://taas:taas@postgres:5432/taas?sslmode=disable"
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
	clear := flag.Bool("clear", false, "delete all seeded clusters (for the empty-state e2e case)")
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
		if _, err := db.Exec(`DELETE FROM clusters`); err != nil {
			log.Fatalf("clear clusters: %v", err)
		}
		log.Printf("cleared clusters")
		return
	}

	// Delete existing seeded rows first so the seed is idempotent across
	// runs (the clusters table is not org-scoped).
	if _, err := db.Exec(`DELETE FROM clusters`); err != nil {
		log.Fatalf("clear clusters: %v", err)
	}

	// Seed an active cluster and a disabled cluster so the list, detail,
	// and disable flows are exercised.
	now := time.Now().UTC()
	activeID := "dddddddd-dddd-dddd-dddd-dddddddddddd"
	disabledID := "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	seedCluster := func(id, name, region, state string, at time.Time) {
		if _, err := db.Exec(`
			INSERT INTO clusters (id, name, region, kubeconfig_ref, state, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (id) DO NOTHING`,
			id, name, region, "kube/"+name, state, at); err != nil {
			log.Fatalf("seed cluster: %v", err)
		}
	}
	seedCluster(activeID, "e2e-cluster-a", "cn-north", "active", now.Add(-2*time.Hour))
	seedCluster(disabledID, "e2e-cluster-b", "cn-south", "disabled", now.Add(-1*time.Hour))

	log.Printf("seeded clusters %s (active), %s (disabled)", activeID, disabledID)
	_ = fmt.Sprintf
}
