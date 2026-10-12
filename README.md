<p align="center">
  <img src="assets/brand/logo.svg" width="110" alt="" />
</p>

# Go TaaS

**Token as a Service — an open-source LLM inference and metering/billing platform for AI agents and SDKs, built on Kubernetes**

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](./LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8.svg)](https://golang.org/)
[![PRs Welcome](https://img.shields.io/badge/PRs-Welcome-brightgreen.svg)](./CONTRIBUTING.md)

**Go TaaS** is an open-source platform for serving and billing large language models on Kubernetes. Administrators deploy open-source models (Qwen, DeepSeek, LLaMA, etc.) on heterogeneous accelerators — NVIDIA GPU, Iluvatar CoreX, and MetaX — and price them per model and card type. Agents and SDKs consume the models through an OpenAI-compatible API, with per-API-Key isolation, token metering, and settlement.

> **Status**: Feature-complete and end-to-end verified. All 45 backlog feature points (rows 1–46; fine-tuning was descoped by the product owner) are implemented across both consoles, with unit + FVT suites and 45 Nightwatch end-to-end suites green on the Docker Compose stack. See the [roadmap](#roadmap) for what shipped in each phase.

**English | [简体中文](./README.zh-cn.md)**

## Table of Contents

- [Features](#features)
- [Architecture](#architecture)
- [Tech Stack](#tech-stack)
- [Getting Started](#getting-started)
- [Usage](#usage)
- [Documentation](#documentation)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [Community](#community)
- [License](#license)

## Features

Every capability below ships a console surface on the end-user and/or admin
console and is covered by an end-to-end test suite.

**Platform foundations**

| Area | Capability |
| --- | --- |
| Heterogeneous compute | Unified support for NVIDIA GPU, Iluvatar CoreX, and MetaX; node labeling, Device Plugins, and driver deployment are handled by each vendor's GPU Operator. A live **accelerator inventory** reports per-node GPU fleet health, and a **compatibility matrix** records which model × engine × card-type combinations are supported. |
| Control plane / data plane separation | Management APIs and inference APIs are served by separate gateways, so platform operations stay available while the inference path scales, upgrades, or degrades. |
| Two consoles, one product | A dedicated **end-user console** at `/` (API `/api/v1/*`) for tenants, and a separate **admin console** at `/admin` (API `/api/v1/admin/*`) for operators — each with its own session, login flow, and role-based routing. |
| Tiered storage and networking | Longhorn for cluster state, MinIO + JuiceFS for weights and images; standard Ethernet for the control plane, optional RDMA (RoCE / InfiniBand) for inference. |

**Models and inference**

| Area | Capability |
| --- | --- |
| Model catalog & one-click deployment | Register and version models, then deploy with one click from either console or the API; the platform provisions inference workloads through a message queue and a Kubernetes controller, with no hand-written YAML. Version history and rollback are built in. |
| Inference image management | Register, version, and map engine images across NVIDIA / CoreX / MetaX, with node-level pre-pulling to shorten cold starts. |
| OpenAI-compatible API | `/v1/chat/completions`, `/v1/completions`, and `/v1/embeddings` served by an Envoy-based gateway; authentication, metering, and routing run in Wasm plugins, so inference traffic never passes through business processes. |
| Routing & resilience | Ordered **routing policies** over healthy deployments with bounded same-model failover and retryable-failure categories — no silent model or version substitution. |
| Autoscaling | Inference autoscaling with scale-to-zero, driven by load signals. |
| Load testing | Run a load test against an inference service and view throughput, latency, and tokens-per-second results. |
| Batch inference | Submit a JSONL batch of requests, process it asynchronously at a discounted rate, monitor progress, and download result/error files. |

**Billing, metering and analytics**

| Area | Capability |
| --- | --- |
| Token metering and billing | Token usage (prompt / completion / cached / reasoning) is metered at the gateway, recorded as tamper-proof vouchers, and settled asynchronously per API Key. |
| Flexible pricing | A model × card-type price matrix with separate input/output token rates, tiered pricing, and packages; balance (prepaid) and quota (postpaid) account modes. |
| Payments and invoices | Payments, invoices, and auto-recharge top-ups, with a full billing ledger. |
| Usage and cost analytics | Usage dashboards, per-request cost attribution, per-API-key usage analytics, cost analytics with trends and cost-per-token, and usage/cost forecasting. |
| Billing reports | Generate and download per-org / per-key / per-model usage and billing reports as CSV, with scheduled report history. |

**Tenancy, access and governance**

| Area | Capability |
| --- | --- |
| Multi-tenancy | Organization/project isolation across the whole platform, with organization members, roles, and invitations (RBAC). |
| Per-tenant model authorization | Restrict which catalog models each tenant may consume. |
| API Keys | Full lifecycle (create / list / revoke), salted-hash storage, per-key rate limits, org spend limits, and per-key **model scoping** so a leaked or over-broad key cannot reach unauthorized models. |
| SSO federation | Pluggable identity providers — LDAP/LDAPS, OIDC/OAuth 2.0 (GitHub, Google, GitLab), SAML 2.0, and WeChat — with account binding, JIT provisioning, and attribute-to-role mapping. |
| Audit & data governance | Audit logging and activity export, deployment history & audit, and GDPR-style data export (usage, billing, request logs) as JSON/CSV. |

**Observability and automation**

| Area | Capability |
| --- | --- |
| Observability | Per-model latency / throughput / error-rate / token-throughput dashboards, per-service CPU / memory / GPU resource metrics, and inference service log viewing. |
| Request tracing | Drill into individual inference requests end-to-end (request → gateway → inference service) with per-phase latency breakdown (TTFT, generation, total) and request-id lookup. |
| Health & errors | Platform component health and dependency status, plus error analysis (code/rate trends, top causes, and drill-down). |
| Events & automation | Outbound webhooks (deployment, autoscaling, billing, spend-limit, and balance-low events) with signing secrets, retry policy, and a delivery log; plus a notification center with per-user preferences and threshold alerts. |

**Prompt tooling**

| Area | Capability |
| --- | --- |
| Prompt library | Create, version, organize, search, and reuse prompt templates; open a prompt in the playground; per-prompt usage stats. |
| Playground & comparison | Interactive playground, plus side-by-side model comparison on the same prompt with latency / token / cost per model. |
| Prompt evaluation | Curate variable-based test cases, run a saved prompt version against a model, score with deterministic checks or manual review, and compare repeatable run results. |
| API docs explorer | An in-console interactive API reference with endpoint lists, request/response examples, and try-it-in-console. |

## Architecture

```mermaid
flowchart TD
    subgraph agents["Agents"]
        direction LR
        USER["Agents / SDK"]
    end

    subgraph operators["Operations"]
        direction LR
        ADMIN["Admin Console / CLI"]
    end

    subgraph k8s["Kubernetes Cluster (control plane and inference services in one cluster)"]
        direction TB
        subgraph cp["Control Plane (standard network)"]
            direction TB
            CGW["Control Gateway (HTTP)<br/>platform management API"]
            GRPC["gRPC Server (single Deployment)<br/>20 microservices<br/>auth · model · infer · billing · metering · tenancy · …"]
            MQ["Message Queue (Kafka / NATS)"]
            CTRL["Controller<br/>(K8s resource reconciliation)"]
            CGW --> GRPC
            GRPC <-.-> MQ
            MQ <-.-> CTRL
        end
        subgraph dp["Data Plane (RDMA network)"]
            direction TB
            DGW["Inference Gateway<br/>Envoy + Wasm (auth · metering · routing)"]
            E["Inference Services<br/>vLLM / SGLang / CoreX / MetaX"]
            DGW --> E
        end
        subgraph store["Storage and Stateful Components"]
            direction LR
            LH[("Longhorn")]
            PG[("PostgreSQL")]
            RD[("Redis")]
            MINIO[("MinIO")]
            JFS[("JuiceFS")]
            LH --- PG
            LH --- RD
            MINIO --> JFS
        end
        GRPC -.-> store
        CTRL --> E
    end

    ADMIN --> CGW
    USER --> DGW
    DGW -.->|metering events| MQ
    E -.->|mount weights| JFS
    classDef consumer fill:#FFFFFF,stroke:#A9BABD,stroke-width:1.5px,color:#17272B
    classDef edge fill:#007F86,stroke:#00565C,stroke-width:2px,color:#FFFFFF
    classDef core fill:#0B4F55,stroke:#083B40,stroke-width:2px,color:#FFFFFF
    classDef svc fill:#CFE9EA,stroke:#4E9CA0,stroke-width:1.5px,color:#0B383D
    classDef store fill:#E7ECEE,stroke:#9AACB1,stroke-width:1.5px,color:#23343A
    classDef gpu fill:#FADDD1,stroke:#D2704E,stroke-width:2px,color:#71260E
    classDef dpg fill:#F6CCC0,stroke:#C4623F,stroke-width:2px,color:#5F200A
    class ADMIN,USER consumer
    class CGW edge
    class GRPC core
    class MQ,CTRL svc
    class LH,PG,RD,MINIO,JFS store
    class DGW dpg
    class E gpu
    style agents fill:#FAFCFC,stroke:#C6D2D4,stroke-width:1.5px,color:#3B4C50
    style operators fill:#FAFCFC,stroke:#C6D2D4,stroke-width:1.5px,color:#3B4C50
    style k8s fill:#F6FAFA,stroke:#B4C7CA,stroke-width:1.5px,color:#2F5B60
    style cp fill:#EDF7F7,stroke:#9CC7C9,stroke-width:1.5px,color:#2A6A6F
    style dp fill:#FDF2ED,stroke:#E4A48B,stroke-width:1.5px,color:#8A3419
    style store fill:#F5F8F9,stroke:#BCC9CD,stroke-width:1.5px,color:#47595E
```

- Traffic is split by audience: administrators enter through the **Control Gateway** (HTTP, generated from the Protobuf contract by `grpc-gateway`), while agents and SDKs call the **Inference Gateway** (Envoy + Wasm).
- The two gateways are deployed as separate workloads, so control plane APIs and inference APIs scale, upgrade, and fail independently.
- APIs are partitioned by audience: the end-user console and its tenants call `/api/v1/*`, while `/api/v1/admin/*` is reserved for the admin console. A request that carries a session from the wrong surface is rejected before any handler runs.
- All microservice gRPC servers (`auth`, `model`, `image`, `infer`, `billing`, `metering`, `tenancy`, `account`, `audit`, `observability`, `tracing`, `notification`, `webhook`, `batch`, `prompt`, `evaluation`, `cluster`, `accelerator`, `resourcemetrics`, `docs`) run in a **single Deployment** and hand work to the Controller through a **message queue** (Kafka / NATS), decoupling API serving from Kubernetes reconciliation.
- Authentication, metering, and routing run in **Wasm plugins** at the Inference Gateway, so inference traffic never passes through business processes.
- The Inference Gateway verifies every API Key by **calling the `auth` module over gRPC**, backed by a two-level cache (Wasm-local TTL cache, then Redis); on timeout or `auth` unavailability it fails closed.
- Inference services and the control plane share **one Kubernetes cluster**, isolated through Namespaces, node labels, and taints/tolerations.

See the [Architecture Design](./docs/design/architecture.md) for component responsibilities, gateway design, and collaboration flows.

## Tech Stack

| Layer | Choice |
| --- | --- |
| Backend | Go, gRPC + grpc-gateway, GORM |
| Data | PostgreSQL, Redis (Valkey-compatible), Kafka / NATS |
| Control plane gateway | grpc-gateway — HTTP/JSON and OpenAPI generated from Protobuf |
| Data plane gateway | Envoy + Wasm |
| Storage | Longhorn for cluster state; MinIO + JuiceFS for inference weights and images |
| Network | Standard Ethernet for the control plane; RDMA (RoCE / InfiniBand) optional for inference |
| Orchestration | Kubernetes + client-go |
| Inference engines | NVIDIA: vLLM / SGLang / TensorRT-LLM; Iluvatar CoreX; MetaX |
| Frontend (`web/`) | React 18 + TypeScript + Vite + Phosphor Icons — a single SPA serving both the end-user and admin consoles |
| Testing | Go unit tests + FVT suites (`test/fvt`) and Nightwatch browser end-to-end suites (`test/e2e`) |

## Getting Started

### Prerequisites

- Go 1.26+
- golangci-lint v2
- buf
- Node.js 20+ (frontend, `web/`)
- Docker + Docker Compose (for the local stack)
- Git

### Build from Source

```bash
git clone https://github.com/go-taas/go-taas.git
cd go-taas

make pbgen   # generate Protobuf code (gRPC stubs + grpc-gateway + OpenAPI)
make deps    # update dependencies
make lint    # static checks
make ut      # unit tests
make fvt     # functional verification tests
make build   # build the binary
make test    # full test target
```

### Docker Compose

A one-command experience environment: control plane (with console), PostgreSQL, Redis, the message queue, and a JuiceFS client that mounts the shared model-weights filesystem.

```bash
cp .env.example .env   # fill in Harbor + JuiceFS/MinIO values (see below)
make compose-up    # build images (console included) and start the local stack
make compose-ps    # show stack status
make compose-logs  # follow logs (or: make compose-logs SERVICE=taas-server)
make compose-down  # stop and remove the stack
```

The stack reads `.env` (git-ignored; see `.env.example` for the full list):

- `HARBOR_URL` / `HARBOR_USERNAME` / `HARBOR_PASSWORD` — the internal
  container registry the image-import flow pushes engine images into
  (all imports land in the `taas` project).
- `JUICE_FS_*` — the JuiceFS model-weights filesystem (MinIO bucket +
  Redis metadata) that the control plane mounts at `/data/weights` and
  the inference pods read through the cluster StorageClass.
- `WEIGHTS_STORAGE_CLASS` — the JuiceFS-backed StorageClass the
  controller provisions the shared `model-weights` PVC from.

`make compose-up` creates the Kubernetes resources the stack needs
automatically (via `deploy/compose/scripts/cluster-up.sh`): the MinIO
bucket, the JuiceFS filesystem, the `taas` namespace, the
`juicefs-taas-models` StorageClass + secret, the `model-weights` PVC, the
Redis NodePort (the cluster Redis is ClusterIP-only, so it is published
on the node IP at `:30379`), and the accelerator node labels.
`make compose-down` removes the compose stack (including the database
volume) and cleans up all of those cluster resources (via
`deploy/compose/scripts/cluster-down.sh`).

The console is served by `taas-server`, same origin as the API (no CORS setup
needed): the **end-user console** at `http://localhost:9091/` and the **admin
console** at `http://localhost:9091/admin`. End-user APIs live under
`/api/v1/*` (login and signup under `/api/v1/auth/*`); management APIs live
under `/api/v1/admin/*`. The gateway listens on `9091` (HTTP/JSON + console),
`9090` (gRPC) and `9092` (metrics/healthz).

A standalone `console` image (nginx serving the same Vite bundle, with `/api/`
reverse-proxied to the gateway) is also published for deployments that scale
or release the frontend independently of the control plane:

```bash
make docker-build    # builds taas-server, controller and console
# run it (point TASS_PROXY_PASS at the gateway):
docker run -p 8080:8080 -e TASS_PROXY_PASS=http://<gateway-host>:9091 \
  ghcr.io/go-taas/go-taas/console:latest
```

On restricted networks, point the image build at local mirrors:

```bash
make compose-up GOPROXY=https://goproxy.cn,direct NPM_REGISTRY=https://registry.npmmirror.com
```

### Production Deployment (planned)

One-command deployment based on Kubernetes + Helm, supporting separation of the platform and inference clusters; see the dedicated deployment repository.

## Usage

### Python (OpenAI SDK)

```python
from openai import OpenAI

client = OpenAI(
    base_url="https://<your-taas-host>/v1",
    api_key="sk-xxxxxxxx",
)

resp = client.chat.completions.create(
    model="qwen2.5-7b",
    messages=[{"role": "user", "content": "Hello, introduce yourself."}],
)
print(resp.choices[0].message.content)
```

### cURL

```bash
curl https://<your-taas-host>/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-xxxxxxxx" \
  -d '{
    "model": "qwen2.5-7b",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

## Documentation

| Document | Language |
| --- | --- |
| [Architecture Design](./docs/design/architecture.md) | English |
| [架构设计文档](./docs/design/architecture.zh-cn.md) | 中文 |

The [`docs/`](./docs) directory carries the full documentation set:

- [`docs/design/`](./docs/design) — product/UI design documents, one per feature point (EN + `zh-cn`).
- [`docs/architecture/`](./docs/architecture) — architecture and detailed design documents, one per feature point (EN + `zh-cn`).
- [`docs/api/`](./docs/api) — generated OpenAPI/Swagger specs per service (auth, model, infer, billing, …).

## Roadmap

All four phases below have shipped and are end-to-end verified on the Docker
Compose stack; remaining work is operational hardening (tracked outside the
feature backlog).

| Phase | Status | Delivered |
| --- | --- | --- |
| Phase 1 (MVP) | ✅ Shipped | Model catalog & one-click deployment, API Key lifecycle, control/inference gateway split, gateway-side auth and metering. |
| Phase 2 | ✅ Shipped | Price matrix, balance/quota, tiered pricing, async settlement, usage/cost analytics, payments/invoices/auto-recharge, audit logging, RBAC, notifications. |
| Phase 3 | ✅ Shipped | Accelerator inventory & health, Iluvatar / MetaX engine and image adaptation, model × engine × card-type compatibility matrix. |
| Phase 4 | ✅ Shipped | Autoscaling, routing policies, load testing, observability & tracing, webhooks, notification center, prompt library & evaluation, batch inference, multi-cluster management, API docs explorer, data export. |
| Next | 🚧 Planned | Helm one-click install, RDMA networking, community operations (no standalone console surface, tracked separately from the feature backlog). |

## Contributing

Contributions of all kinds are welcome — code, documentation, bug reports, and feature ideas. See [CONTRIBUTING.md](./CONTRIBUTING.md) for the development setup, commit conventions, and pull request guidelines.

- Follow [Conventional Commits](https://www.conventionalcommits.org/) (validated by commitlint)
- Run `make lint` and `make ut` before submitting
- Use the issue templates for bug reports and feature requests

## Community

- [GitHub Issues](https://github.com/go-taas/go-taas/issues) — bug reports and feature requests
- [GitHub Discussions](https://github.com/go-taas/go-taas/discussions) — questions and open discussion

## License

Go TaaS is licensed under the [Apache License 2.0](./LICENSE).