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

// Command seed_dataexport seeds data_exports rows directly into the
// compose stack's PostgreSQL so the data-export-privacy e2e suite
// (feature #41) can exercise the populated states of the end-user Data
// Export page (/account/export) against the compose stack.
//
// The compose stack's export-generation runner aggregates data from the
// shared database, but the compose stack has no inference pipeline to
// produce usage/billing/request-log data, so exports created through the
// API stay pending forever. This seed writes data_exports rows directly
// into PostgreSQL (the account module reads the data_exports table),
// including a ready export so the download flow can be exercised.
//
// Usage:
//
//	go run ./test/e2e/seed/dataexport/seed_dataexport.go -dsn "postgres://taas:taas@postgres:5432/taas?sslmode=disable" -org "org-e2e-de-..."
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
	org := flag.String("org", "org-default", "organization that owns the exports")
	clear := flag.Bool("clear", false, "delete all data_exports rows for the given org (for the empty-state e2e case)")
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
		if _, err := db.Exec(`DELETE FROM data_exports WHERE organization_id = $1`, *org); err != nil {
			log.Fatalf("clear data_exports: %v", err)
		}
		log.Printf("cleared data_exports for org %s", *org)
		return
	}

	// Delete the org's existing rows first so the seed is idempotent
	// across runs.
	if _, err := db.Exec(`DELETE FROM data_exports WHERE organization_id = $1`, *org); err != nil {
		log.Fatalf("clear data_exports: %v", err)
	}

	// Seed a ready usage export (so the download flow is exercised) and a
	// pending billing export (so the polling/disabled-download state is
	// exercised).
	now := time.Now().UTC()
	readyID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	pendingID := "11111111-2222-3333-4444-555555555555"
	// Delete the fixed-ID rows first (they may exist from a previous org's
	// seed, and ON CONFLICT (id) would otherwise skip the insert and keep
	// the old org).
	if _, err := db.Exec(`DELETE FROM data_exports WHERE id IN ($1, $2)`, readyID, pendingID); err != nil {
		log.Fatalf("clear fixed data_exports: %v", err)
	}
	seedExport := func(id, typ, format, status string, rowCount int64, file string, at time.Time) {
		if _, err := db.Exec(`
			INSERT INTO data_exports
				(id, organization_id, type, since, until, format, status, row_count, file, error, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '', $10)
			ON CONFLICT (id) DO NOTHING`,
			id, *org, typ, now.Add(-30*24*3600).Unix(), now.Unix(), format, status, rowCount, file, at); err != nil {
			log.Fatalf("seed data_export: %v", err)
		}
	}
	seedExport(readyID, "usage", "json", "ready", 5, `{"usage":[{"tokens":100}]}`, now.Add(-2*time.Hour))
	seedExport(pendingID, "billing", "csv", "pending", 0, "", now.Add(-1*time.Hour))

	log.Printf("seeded data_exports for org %s: %s (ready), %s (pending)", *org, readyID, pendingID)
	_ = fmt.Sprintf
}
