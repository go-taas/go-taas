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

// Command seed_accelerator publishes a full accelerator inventory snapshot
// to the accelerator.inventory NATS subject so the e2e suite can exercise
// the populated states of the admin Accelerators pages (feature #18) against
// the compose stack, which has no Controller/Kubernetes.
//
// The snapshot mirrors the Controller's inventorySnapshot wire format
// (internal/controller/inventory.go) and the accelerator service's
// snapshotNode (services/accelerator/snapshot_consumer.go). The composite
// health is computed by the accelerator service at ingestion, so the seed
// only supplies the raw signals.
//
// Usage:
//
//	go run ./test/e2e/seed/seed_accelerator.go -url nats://nats:4222
//
// The default URL targets the compose network (service name "nats").
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

// snapshotNode mirrors the accelerator service's snapshotNode wire format.
type snapshotNode struct {
	NodeID            string             `json:"node_id"`
	Name              string             `json:"name"`
	Vendor            string             `json:"vendor"`
	CardTypes         []string           `json:"card_types"`
	GPUsAllocated     int64              `json:"gpus_allocated"`
	GPUsFree          int64              `json:"gpus_free"`
	DriverVersion     string             `json:"driver_version"`
	DevicePluginState string             `json:"device_plugin_state"`
	Readiness         string             `json:"readiness"`
	GPUs              []snapshotGPU      `json:"gpus"`
	Labels            map[string]string  `json:"labels"`
	Taints            []string           `json:"taints"`
	Resources         []snapshotResource `json:"resources"`
}

type snapshotGPU struct {
	Index     int64  `json:"index"`
	Model     string `json:"model"`
	Allocated bool   `json:"allocated"`
	Free      bool   `json:"free"`
	Note      string `json:"note"`
}

type snapshotResource struct {
	CardType    string `json:"card_type"`
	Allocatable int64  `json:"allocatable"`
	Allocated   int64  `json:"allocated"`
}

type snapshot struct {
	Nodes      []snapshotNode `json:"nodes"`
	ReportedAt time.Time      `json:"reported_at"`
}

func main() {
	url := flag.String("url", "nats://nats:4222", "NATS URL")
	// The MQ client prefixes subjects with the configured namespace
	// (configs/config.yaml mq.namespace, default "taas-dev"), so the
	// snapshot must be published to "<namespace>.accelerator.inventory".
	subject := flag.String("subject", "taas-dev.accelerator.inventory", "NATS subject")
	// omitCard drops every node whose CardTypes contains the named card
	// type, so the compatibility-matrix suite can exercise the
	// not_in_fleet derivation (feature #19, AC6/AC10) by publishing a
	// snapshot that no longer carries a card type the matrix already has
	// cells for.
	omitCard := flag.String("omitCard", "", "card type to omit from the snapshot")
	flag.Parse()

	nc, err := nats.Connect(*url)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	nodes := []snapshotNode{
		// Healthy NVIDIA node with 8 A800 GPUs, 2 allocated / 6 free.
		{
			NodeID:            "node-nvidia-a800-01",
			Name:              "gpu-node-a800-01",
			Vendor:            "nvidia",
			CardTypes:         []string{"A800"},
			GPUsAllocated:     2,
			GPUsFree:          6,
			DriverVersion:     "535.183.01",
			DevicePluginState: "healthy",
			Readiness:         "ready",
			Labels: map[string]string{
				"go-taas.io/accelerator":    "nvidia",
				"go-taas.io/driver-version": "535.183.01",
				"go-taas.io/device-plugin":  "healthy",
			},
			Taints: []string{},
			Resources: []snapshotResource{
				{CardType: "A800", Allocatable: 8, Allocated: 2},
			},
			GPUs: []snapshotGPU{
				{Index: 0, Model: "A800", Allocated: true, Free: false, Note: ""},
				{Index: 1, Model: "A800", Allocated: true, Free: false, Note: ""},
				{Index: 2, Model: "A800", Allocated: false, Free: true, Note: ""},
				{Index: 3, Model: "A800", Allocated: false, Free: true, Note: ""},
				{Index: 4, Model: "A800", Allocated: false, Free: true, Note: ""},
				{Index: 5, Model: "A800", Allocated: false, Free: true, Note: ""},
				{Index: 6, Model: "A800", Allocated: false, Free: true, Note: ""},
				{Index: 7, Model: "A800", Allocated: false, Free: true, Note: ""},
			},
		},
		// Healthy NVIDIA node with 4 H800 GPUs, all free.
		{
			NodeID:            "node-nvidia-h800-01",
			Name:              "gpu-node-h800-01",
			Vendor:            "nvidia",
			CardTypes:         []string{"H800"},
			GPUsAllocated:     0,
			GPUsFree:          4,
			DriverVersion:     "535.183.01",
			DevicePluginState: "healthy",
			Readiness:         "ready",
			Labels: map[string]string{
				"go-taas.io/accelerator":    "nvidia",
				"go-taas.io/driver-version": "535.183.01",
				"go-taas.io/device-plugin":  "healthy",
			},
			Taints: []string{},
			Resources: []snapshotResource{
				{CardType: "H800", Allocatable: 4, Allocated: 0},
			},
			GPUs: []snapshotGPU{
				{Index: 0, Model: "H800", Allocated: false, Free: true, Note: ""},
				{Index: 1, Model: "H800", Allocated: false, Free: true, Note: ""},
				{Index: 2, Model: "H800", Allocated: false, Free: true, Note: ""},
				{Index: 3, Model: "H800", Allocated: false, Free: true, Note: ""},
			},
		},
		// Degraded NVIDIA node: device plugin degraded, driver present.
		{
			NodeID:            "node-nvidia-degraded-01",
			Name:              "gpu-node-degraded-01",
			Vendor:            "nvidia",
			CardTypes:         []string{"A800"},
			GPUsAllocated:     1,
			GPUsFree:          3,
			DriverVersion:     "535.183.01",
			DevicePluginState: "degraded",
			Readiness:         "ready",
			Labels: map[string]string{
				"go-taas.io/accelerator":    "nvidia",
				"go-taas.io/driver-version": "535.183.01",
				"go-taas.io/device-plugin":  "degraded",
			},
			Taints: []string{"gpu=degraded:NoSchedule"},
			Resources: []snapshotResource{
				{CardType: "A800", Allocatable: 4, Allocated: 1},
			},
			GPUs: []snapshotGPU{
				{Index: 0, Model: "A800", Allocated: true, Free: false, Note: "device plugin degraded"},
				{Index: 1, Model: "A800", Allocated: false, Free: true, Note: ""},
				{Index: 2, Model: "A800", Allocated: false, Free: true, Note: ""},
				{Index: 3, Model: "A800", Allocated: false, Free: true, Note: ""},
			},
		},
		// Iluvatar node, healthy, BI-V150, all free.
		{
			NodeID:            "node-iluvatar-biv150-01",
			Name:              "gpu-node-iluvatar-01",
			Vendor:            "iluvatar",
			CardTypes:         []string{"BI-V150"},
			GPUsAllocated:     0,
			GPUsFree:          8,
			DriverVersion:     "4.2.0",
			DevicePluginState: "healthy",
			Readiness:         "ready",
			Labels: map[string]string{
				"go-taas.io/accelerator":    "iluvatar",
				"go-taas.io/driver-version": "4.2.0",
				"go-taas.io/device-plugin":  "healthy",
			},
			Taints: []string{},
			Resources: []snapshotResource{
				{CardType: "BI-V150", Allocatable: 8, Allocated: 0},
			},
			GPUs: []snapshotGPU{
				{Index: 0, Model: "BI-V150", Allocated: false, Free: true, Note: ""},
				{Index: 1, Model: "BI-V150", Allocated: false, Free: true, Note: ""},
				{Index: 2, Model: "BI-V150", Allocated: false, Free: true, Note: ""},
				{Index: 3, Model: "BI-V150", Allocated: false, Free: true, Note: ""},
				{Index: 4, Model: "BI-V150", Allocated: false, Free: true, Note: ""},
				{Index: 5, Model: "BI-V150", Allocated: false, Free: true, Note: ""},
				{Index: 6, Model: "BI-V150", Allocated: false, Free: true, Note: ""},
				{Index: 7, Model: "BI-V150", Allocated: false, Free: true, Note: ""},
			},
		},
		// MetaX node, healthy, MXC500, all free.
		{
			NodeID:            "node-metax-mxc500-01",
			Name:              "gpu-node-metax-01",
			Vendor:            "metax",
			CardTypes:         []string{"MXC500"},
			GPUsAllocated:     0,
			GPUsFree:          4,
			DriverVersion:     "3.1.0",
			DevicePluginState: "healthy",
			Readiness:         "ready",
			Labels: map[string]string{
				"go-taas.io/accelerator":    "metax",
				"go-taas.io/driver-version": "3.1.0",
				"go-taas.io/device-plugin":  "healthy",
			},
			Taints: []string{},
			Resources: []snapshotResource{
				{CardType: "MXC500", Allocatable: 4, Allocated: 0},
			},
			GPUs: []snapshotGPU{
				{Index: 0, Model: "MXC500", Allocated: false, Free: true, Note: ""},
				{Index: 1, Model: "MXC500", Allocated: false, Free: true, Note: ""},
				{Index: 2, Model: "MXC500", Allocated: false, Free: true, Note: ""},
				{Index: 3, Model: "MXC500", Allocated: false, Free: true, Note: ""},
			},
		},
		// Unspecified node (no accelerator label), ready.
		{
			NodeID:            "node-unspecified-01",
			Name:              "plain-node-01",
			Vendor:            "unspecified",
			CardTypes:         []string{},
			GPUsAllocated:     0,
			GPUsFree:          0,
			DriverVersion:     "",
			DevicePluginState: "unknown",
			Readiness:         "ready",
			Labels:            map[string]string{},
			Taints:            []string{},
			Resources:         []snapshotResource{},
			GPUs:              []snapshotGPU{},
		},
		// NotReady unspecified node.
		{
			NodeID:            "node-unspecified-notready-01",
			Name:              "plain-node-notready-01",
			Vendor:            "unspecified",
			CardTypes:         []string{},
			GPUsAllocated:     0,
			GPUsFree:          0,
			DriverVersion:     "",
			DevicePluginState: "unknown",
			Readiness:         "not_ready",
			Labels:            map[string]string{},
			Taints:            []string{},
			Resources:         []snapshotResource{},
			GPUs:              []snapshotGPU{},
		},
	}

	snap := snapshot{Nodes: nodes, ReportedAt: time.Now().UTC()}
	if *omitCard != "" {
		filtered := nodes[:0]
		for _, n := range nodes {
			keep := true
			for _, ct := range n.CardTypes {
				if ct == *omitCard {
					keep = false
					break
				}
			}
			if keep {
				filtered = append(filtered, n)
			}
		}
		snap.Nodes = filtered
	}
	body, err := json.Marshal(snap)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := nc.Publish(*subject, body); err != nil {
		log.Fatalf("publish: %v", err)
	}
	if err := nc.Flush(); err != nil {
		log.Fatalf("flush: %v", err)
	}
	_ = ctx
	log.Printf("published %s snapshot with %d nodes", *subject, len(nodes))
}
