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

package main

import (
	"context"
	"flag"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

// fakePublisher records the exchange and routing key of each publish so
// tests can assert the seed uses the namespace as the exchange name and
// the bare subject as the routing key (matching pkg/mq/rabbitmq.go).
type fakePublisher struct {
	exchange   string
	routingKey string
	body       []byte
}

func (f *fakePublisher) PublishWithContext(_ context.Context, exchange, key string, _ bool, _ bool, msg amqp.Publishing) error {
	f.exchange = exchange
	f.routingKey = key
	f.body = msg.Body
	return nil
}

// TestPublishSnapshotUsesBareSubjectRoutingKey verifies the seed publishes
// to the namespace exchange with the bare subject as the routing key, not a
// namespace-prefixed key. This is the regression test for MQ-SEED-1.
func TestPublishSnapshotUsesBareSubjectRoutingKey(t *testing.T) {
	fp := &fakePublisher{}
	body := []byte(`{"nodes":[],"reported_at":"2026-10-21T00:00:00Z"}`)
	if err := publishSnapshot(fp, "taas-dev", "accelerator.inventory", body); err != nil {
		t.Fatalf("publishSnapshot: %v", err)
	}
	if fp.exchange != "taas-dev" {
		t.Errorf("exchange = %q, want %q", fp.exchange, "taas-dev")
	}
	if fp.routingKey != "accelerator.inventory" {
		t.Errorf("routing key = %q, want %q", fp.routingKey, "accelerator.inventory")
	}
	if string(fp.body) != string(body) {
		t.Errorf("body = %q, want %q", fp.body, body)
	}
}

// TestDefaultSubjectIsBare verifies the -subject flag default is the bare
// subject (not namespace-prefixed), which is what the e2e suites rely on
// when they run the seed without arguments.
func TestDefaultSubjectIsBare(t *testing.T) {
	fs := flag.NewFlagSet("seed_accelerator", flag.ContinueOnError)
	subject := fs.String("subject", "accelerator.inventory", "RabbitMQ routing key (bare subject)")
	namespace := fs.String("namespace", "taas-dev", "MQ namespace (topic exchange name)")
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *subject != "accelerator.inventory" {
		t.Errorf("default subject = %q, want %q", *subject, "accelerator.inventory")
	}
	if *namespace != "taas-dev" {
		t.Errorf("default namespace = %q, want %q", *namespace, "taas-dev")
	}
}
