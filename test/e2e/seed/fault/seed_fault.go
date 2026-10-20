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

// Command seed_fault publishes structured failure signals to the
// fault.signals NATS subject so the e2e suite can exercise the populated
// states of the admin Faults pages (feature #53) against the compose
// stack, which has no Controller/Kubernetes.
//
// The signal mirrors the Controller's faultSignal wire format
// (services/fault/signal_consumer.go). The fault module debounces
// crash_loop signals (default 3 restarts in 3 minutes), so the seed
// publishes three crash_loop signals for a service target; the other
// signal types create an incident on a single signal.
//
// With -target <serviceId> the seed publishes a single oom_killed
// signal for that service instead of the standard set, so the e2e suite
// can drive the admin ServiceStatusBanner into its degraded state
// (feature #53, AC8) for a real inference service.
//
// Usage:
//
//	go run ./test/e2e/seed/fault/seed_fault.go -url nats://nats:4222
//	go run ./test/e2e/seed/fault/seed_fault.go -target <serviceId>
//
// The default URL targets the compose network (service name "nats").
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

// faultSignal mirrors the fault module's faultSignal wire format.
type faultSignal struct {
	TargetType string `json:"target_type"`
	TargetName string `json:"target_name"`
	SignalType string `json:"signal_type"`
	Detail     string `json:"detail"`
	ObservedAt string `json:"observed_at"`
}

func main() {
	url := flag.String("url", "nats://nats:4222", "NATS URL")
	// The MQ client prefixes subjects with the configured namespace
	// (configs/config.yaml mq.namespace, default "taas-dev"), so the
	// signal must be published to "<namespace>.fault.signals".
	subject := flag.String("subject", "taas-dev.fault.signals", "NATS subject")
	// A real service id: publish a single oom_killed signal for it
	// (banner seed mode) instead of the standard incident set.
	target := flag.String("target", "", "target service id (banner seed mode)")
	flag.Parse()

	nc, err := nats.Connect(*url)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	now := time.Now().UTC().Format(time.RFC3339)

	if *target != "" {
		if err := publish(nc, *subject, faultSignal{
			TargetType: "service",
			TargetName: *target,
			SignalType: "oom_killed",
			Detail:     "container OOMKilled (e2e banner seed)",
			ObservedAt: now,
		}); err != nil {
			log.Fatalf("publish: %v", err)
		}
		log.Printf("seeded oom_killed signal for service %s", *target)
		return
	}

	// A service crash-loop incident: three signals to pass the debounce
	// threshold (default 3 restarts in 3 minutes).
	crashLoop := faultSignal{
		TargetType: "service",
		TargetName: "svc-crash-loop",
		SignalType: "crash_loop",
		Detail:     "container restarted 5 times in 3 minutes",
		ObservedAt: now,
	}
	for i := 0; i < 3; i++ {
		if err := publish(nc, *subject, crashLoop); err != nil {
			log.Fatalf("publish: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// A node NotReady incident (single signal).
	if err := publish(nc, *subject, faultSignal{
		TargetType: "node",
		TargetName: "node-not-ready",
		SignalType: "node_not_ready",
		Detail:     "node NotReady for 2 minutes",
		ObservedAt: now,
	}); err != nil {
		log.Fatalf("publish: %v", err)
	}

	// An accelerator device-plugin failure incident (single signal).
	if err := publish(nc, *subject, faultSignal{
		TargetType: "accelerator",
		TargetName: "accel-device-plugin",
		SignalType: "device_plugin_unhealthy",
		Detail:     "device plugin unhealthy on node-a",
		ObservedAt: now,
	}); err != nil {
		log.Fatalf("publish: %v", err)
	}

	// An OOMKilled incident (single signal).
	if err := publish(nc, *subject, faultSignal{
		TargetType: "service",
		TargetName: "svc-oom",
		SignalType: "oom_killed",
		Detail:     "container OOMKilled",
		ObservedAt: now,
	}); err != nil {
		log.Fatalf("publish: %v", err)
	}

	// An ImagePullBackOff incident (single signal).
	if err := publish(nc, *subject, faultSignal{
		TargetType: "service",
		TargetName: "svc-image-pull",
		SignalType: "image_pull_backoff",
		Detail:     "image pull backoff",
		ObservedAt: now,
	}); err != nil {
		log.Fatalf("publish: %v", err)
	}

	log.Println("seeded fault signals")
}

// publish marshals and publishes one signal. The caller decides how a
// failure terminates the one-shot seed.
func publish(nc *nats.Conn, subject string, sig faultSignal) error {
	body, err := json.Marshal(sig)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := nc.Publish(subject, body); err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	return nil
}
