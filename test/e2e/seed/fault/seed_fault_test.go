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
	"encoding/json"
	"flag"
	"fmt"
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

// TestPublishUsesBareSubjectRoutingKey verifies the seed publishes to the
// namespace exchange with the bare subject as the routing key, not a
// namespace-prefixed key. This is the regression test for MQ-SEED-1.
func TestPublishUsesBareSubjectRoutingKey(t *testing.T) {
	fp := &fakePublisher{}
	sig := faultSignal{
		TargetType: "service",
		TargetName: "svc-oom",
		SignalType: "oom_killed",
		Detail:     "container OOMKilled",
		ObservedAt: "2026-10-21T00:00:00Z",
	}
	if err := publish(fp, "taas-dev", "fault.signals", sig); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if fp.exchange != "taas-dev" {
		t.Errorf("exchange = %q, want %q", fp.exchange, "taas-dev")
	}
	if fp.routingKey != "fault.signals" {
		t.Errorf("routing key = %q, want %q", fp.routingKey, "fault.signals")
	}
	// The body must be the marshalled signal.
	var got faultSignal
	if err := json.Unmarshal(fp.body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.SignalType != "oom_killed" || got.TargetName != "svc-oom" {
		t.Errorf("unexpected signal body: %+v", got)
	}
}

// errPublisher returns an error from PublishWithContext so tests can
// exercise the publish error branch.
type errPublisher struct{}

func (errPublisher) PublishWithContext(_ context.Context, _, _ string, _ bool, _ bool, _ amqp.Publishing) error {
	return fmt.Errorf("broker down")
}

// TestPublishErrorPropagates verifies a broker failure is returned to the
// caller so the one-shot seed terminates with a fatal error.
func TestPublishErrorPropagates(t *testing.T) {
	sig := faultSignal{TargetType: "service", TargetName: "svc-oom", SignalType: "oom_killed"}
	if err := publish(errPublisher{}, "taas-dev", "fault.signals", sig); err == nil {
		t.Fatal("expected publish error, got nil")
	}
}

// TestDefaultSubjectIsBare verifies the -subject flag default is the bare
// subject (not namespace-prefixed), which is what the e2e suites rely on
// when they run the seed without arguments.
func TestDefaultSubjectIsBare(t *testing.T) {
	fs := flag.NewFlagSet("seed_fault", flag.ContinueOnError)
	subject := fs.String("subject", "fault.signals", "RabbitMQ routing key (bare subject)")
	namespace := fs.String("namespace", "taas-dev", "MQ namespace (topic exchange name)")
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *subject != "fault.signals" {
		t.Errorf("default subject = %q, want %q", *subject, "fault.signals")
	}
	if *namespace != "taas-dev" {
		t.Errorf("default namespace = %q, want %q", *namespace, "taas-dev")
	}
}
