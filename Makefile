# Copyright 2025 The go-taas Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

GO ?= go

# ---- Docker image build variables -------------------------------------
# Override on the command line, e.g.:
#   make docker-build IMAGE_REPO=ghcr.io/go-taas/go-taas IMAGE_TAG=dev
IMAGE_REPO ?= ghcr.io/go-taas/go-taas
IMAGE_TAG  ?= dev
DOCKERFILE ?= build/docker/Dockerfile

# Binaries published as images; each maps to a Dockerfile build target.
IMAGE_TARGETS ?= taas-server controller

# Network build-args for image builds. Override on the command line for
# restricted networks, e.g.:
#   make compose-up GOPROXY=https://goproxy.cn,direct NPM_REGISTRY=https://registry.npmmirror.com
GOPROXY ?= https://proxy.golang.org,direct
NPM_REGISTRY ?= https://registry.npmjs.org

# Shared docker build flags (metadata + network proxies).
DOCKER_BUILD_ARGS := --build-arg VERSION=$(IMAGE_TAG) \
	--build-arg COMMIT=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown) \
	--build-arg BUILD_TIME=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
	--build-arg GOPROXY=$(GOPROXY) \
	--build-arg NPM_REGISTRY=$(NPM_REGISTRY)

.PHONY: all pbgen pbgen-ensure deps lint ut fvt build test clean \
	docker-build docker-push docker-build-multi compose-up compose-down \
	compose-ps compose-logs

all: build

## pbgen: update buf dependencies and regenerate protobuf code
## Generated code (*.pb.go, *.pb.gw.go, docs/api/) is NOT committed;
## only *.proto files are tracked. Regenerate after every proto change.
pbgen:
	buf dep update
	buf generate

# Generated protobuf code is required by every Go target. It is not
# committed, so ensure it exists (regenerate only when missing).
proto/taas/auth/v1/auth.pb.go:
	buf dep update
	buf generate

.PHONY: pbgen-ensure
pbgen-ensure: proto/taas/auth/v1/auth.pb.go

## deps: download go module dependencies
deps:
	$(GO) mod download

## lint: run golangci-lint over the repository
lint: pbgen-ensure
	golangci-lint run

## ut: run unit tests
ut: pbgen-ensure
	$(GO) test -count=1 ./...

## fvt: run full-verification tests (in-process full stack)
fvt: pbgen-ensure
	$(GO) test -count=1 ./test/fvt/...

## build: compile all binaries
build: pbgen-ensure
	$(GO) build ./...

## test: run unit tests with race detector
test: pbgen-ensure
	$(GO) test -race -count=1 ./...

## clean: remove build artifacts
clean:
	$(GO) clean

## docker-build: build local container images for every binary target
## (single-architecture, native arch)
docker-build:
	@for target in $(IMAGE_TARGETS); do \
		echo ">> building $(IMAGE_REPO)/$${target}:$(IMAGE_TAG)"; \
		docker build --target "$${target}" \
			$(DOCKER_BUILD_ARGS) \
			-f $(DOCKERFILE) -t "$(IMAGE_REPO)/$${target}:$(IMAGE_TAG)" . || exit 1; \
	done

## docker-push: push previously built images (requires docker-build and
## a prior 'docker login')
docker-push:
	@for target in $(IMAGE_TARGETS); do \
		echo ">> pushing $(IMAGE_REPO)/$${target}:$(IMAGE_TAG)"; \
		docker push "$(IMAGE_REPO)/$${target}:$(IMAGE_TAG)" || exit 1; \
	done

## compose-up: build the taas-server image (console included) and start
## the local deployment-verification stack (PostgreSQL, Redis, NATS,
## taas-server). The admin console is served by taas-server at
## http://localhost:9091/ — same origin as the API.
compose-up:
	docker build --target taas-server \
		$(DOCKER_BUILD_ARGS) \
		-f $(DOCKERFILE) -t "$(IMAGE_REPO)/taas-server:$(IMAGE_TAG)" .
	docker compose -f deploy/compose/docker-compose.yaml up -d
	@echo ">> console: http://localhost:9091/  (API: /api/v1/..., gRPC: 9090, metrics: 9092)"

## compose-down: stop and remove the local verification stack
compose-down:
	docker compose -f deploy/compose/docker-compose.yaml down -v

## compose-ps: show the status of the verification stack
compose-ps:
	docker compose -f deploy/compose/docker-compose.yaml ps

## compose-logs: follow the logs of the verification stack (or one
## service: make compose-logs SERVICE=taas-server)
compose-logs:
	docker compose -f deploy/compose/docker-compose.yaml logs -f $(SERVICE)

## docker-build-multi: build and push multi-arch (amd64/arm64) images
## using Docker Buildx (requires 'docker buildx create' once per host)
docker-build-multi:
	@for target in $(IMAGE_TARGETS); do \
		echo ">> building+pushing $(IMAGE_REPO)/$${target}:$(IMAGE_TAG) (amd64/arm64)"; \
		docker buildx build --target "$${target}" \
			--platform linux/amd64,linux/arm64 \
			$(DOCKER_BUILD_ARGS) \
			-f $(DOCKERFILE) -t "$(IMAGE_REPO)/$${target}:$(IMAGE_TAG)" \
			--push . || exit 1; \
	done
