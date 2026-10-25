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
HELM ?= helm

# ---- Helm chart variables ---------------------------------------------
# The Kubernetes chart for the control plane; its values file drives every
# third-party dependency connection (see deploy/helm/go-taas/README.md).
HELM_CHART ?= deploy/helm/go-taas
HELM_RELEASE ?= go-taas
HELM_NAMESPACE ?= taas

# ---- Docker image build variables -------------------------------------
# Override on the command line, e.g.:
#   make docker-build IMAGE_REPO=ghcr.io/go-taas/go-taas IMAGE_TAG=dev
IMAGE_REPO ?= ghcr.io/go-taas/go-taas
IMAGE_TAG  ?= dev
DOCKERFILE ?= build/docker/Dockerfile

# Binaries published as images; each maps to a Dockerfile build target.
# "taas-console" is the standalone nginx image of the console (the same
# Vite bundle embedded into taas-server) for independent frontend
# deployments.
IMAGE_TARGETS ?= taas-server taas-controller taas-console

# Network build-args for image builds. Override on the command line for
# restricted networks, e.g.:
#   make compose-up GOPROXY=https://goproxy.cn,direct NPM_REGISTRY=https://registry.npmmirror.com
GOPROXY ?= https://goproxy.cn,direct
NPM_REGISTRY ?= https://registry.npmjs.org
# Debian apt mirror for the taas-server runtime image; empty keeps the
# upstream deb.debian.org sources. Override on restricted networks, e.g.
#   make compose-up APT_MIRROR=http://mirrors.tuna.tsinghua.edu.cn
APT_MIRROR ?=

# Shared docker build flags (metadata + network proxies).
DOCKER_BUILD_ARGS := --build-arg VERSION=$(IMAGE_TAG) \
	--build-arg COMMIT=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown) \
	--build-arg BUILD_TIME=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
	--build-arg GOPROXY=$(GOPROXY) \
	--build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
	--build-arg APT_MIRROR=$(APT_MIRROR)

# Docker build network. Defaults to the host network so the build container
# can reach the module proxy / npm registry / buf remote on hosts whose
# build-container DNS is unreachable (e.g. a container DNS pointing at an
# IPv6 link-local resolver). Override with DOCKER_NETWORK= for the default
# bridge network (e.g. in CI).
DOCKER_NETWORK ?= --network=host

.PHONY: all pbgen pbgen-ensure deps lint ut fvt build test clean \
	docker-build docker-push docker-build-multi compose-up compose-down \
	compose-ps compose-logs compose-cluster-up compose-cluster-down \
	helm-lint helm-template github-teams-plan github-teams-verify

all: build

## pbgen: regenerate protobuf code
## Generated code (*.pb.go, *.pb.gw.go, site/docs/api/) is NOT committed;
## only *.proto files are tracked. Regenerate after every proto change.
## buf generate resolves the googleapis dependency from the checked-in
## buf.lock, so it does not need to reach the buf remote (BSR).
pbgen:
	buf generate

# Generated protobuf code is required by every Go target. It is not
# committed, so ensure it exists (regenerate only when missing).
proto/taas/auth/v1/auth.pb.go:
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

## wasm: build the data-plane gateway plugin (TinyGo, separate module)
wasm:
	$(MAKE) -C wasm/gateway build GOPROXY=$(GOPROXY)

## wasm-test: unit-test the data-plane gateway plugin
wasm-test:
	$(MAKE) -C wasm/gateway test

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
		docker build $(DOCKER_NETWORK) --target "$${target}" \
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

## compose-up: create the cluster resources, build the taas-server and
## taas-controller images and start the local deployment-verification stack
## (PostgreSQL, Redis, RabbitMQ, Keycloak, taas-server, controller). The admin
## console is served by taas-server at http://localhost:9091/ — same origin
## as the API. Keycloak (the local OIDC IdP) is at http://localhost:8080
## (admin / admin); its realm, client and test user are imported from
## deploy/compose/keycloak.
## The controller reconciles against the cluster in ~/.kube/config (or
## $KUBECONFIG); see deploy/compose/docker-compose.yaml.
## NOTE: for the SSO flow the host must resolve `keycloak` to 127.0.0.1:
##   echo '127.0.0.1 keycloak' | sudo tee -a /etc/hosts
compose-up:
	$(MAKE) compose-cluster-up
	docker build $(DOCKER_NETWORK) --target taas-server \
		$(DOCKER_BUILD_ARGS) \
		-f $(DOCKERFILE) -t "$(IMAGE_REPO)/taas-server:$(IMAGE_TAG)" .
	docker build $(DOCKER_NETWORK) --target taas-controller \
		$(DOCKER_BUILD_ARGS) \
		-f $(DOCKERFILE) -t "$(IMAGE_REPO)/taas-controller:$(IMAGE_TAG)" .
	docker compose --env-file .env -f deploy/compose/docker-compose.yaml up -d
	@echo ">> console: http://localhost:9091/  (API: /api/v1/..., gRPC: 9090, metrics: 9092)"
	@echo ">> keycloak: http://localhost:8080/  (admin / admin)"

## compose-cluster-up: create the Kubernetes resources the compose stack
## needs (MinIO bucket, JuiceFS filesystem, namespace, storageclass,
## secret, PVC, redis NodePort, node labels). Idempotent.
compose-cluster-up:
	bash deploy/compose/scripts/cluster-up.sh

## compose-down: stop and remove the local verification stack (including
## the database volume) and clean up the Kubernetes resources the stack
## created (namespace, storageclass, secret, PVC, redis NodePort, MinIO
## bucket, JuiceFS filesystem, node labels).
compose-down:
	docker compose --env-file .env -f deploy/compose/docker-compose.yaml down -v
	$(MAKE) compose-cluster-down

## compose-cluster-down: remove the Kubernetes resources the compose stack
## created. Idempotent.
compose-cluster-down:
	bash deploy/compose/scripts/cluster-down.sh

## compose-ps: show the status of the verification stack
compose-ps:
	docker compose --env-file .env -f deploy/compose/docker-compose.yaml ps

## compose-logs: follow the logs of the verification stack (or one
## service: make compose-logs SERVICE=taas-server)
compose-logs:
	docker compose --env-file .env -f deploy/compose/docker-compose.yaml logs -f $(SERVICE)

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

## helm-lint: lint the Kubernetes chart (values schema included)
helm-lint:
	$(HELM) lint $(HELM_CHART)

## helm-template: render the Kubernetes chart with the default values,
## verifying every manifest against the cluster API when one is reachable
helm-template:
	$(HELM) template $(HELM_RELEASE) $(HELM_CHART)

## helm-install: install the chart from a local checkout (the release
## namespace is created if needed)
helm-install:
	$(HELM) upgrade --install $(HELM_RELEASE) $(HELM_CHART) \
		--namespace $(HELM_NAMESPACE) --create-namespace

## helm-uninstall: remove the chart release (the generated credentials
## Secret and the managed inference namespace are kept)
helm-uninstall:
	$(HELM) uninstall $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)

## github-teams-plan: preview the organization teams and repository
## permissions required by GOVERNANCE.md (read-only; nothing is created)
github-teams-plan:
	./scripts/github/setup-teams.sh plan

## github-teams-verify: check that the teams, repository permissions,
## CODEOWNERS team references and default-branch ruleset match the docs
github-teams-verify:
	./scripts/github/setup-teams.sh verify
