# Go TaaS Architecture Design

| Attribute | Content |
| --- | --- |
| Project name | Go TaaS (Token as a Service) |
| Overall positioning | A one-stop, open-source LLM inference service and metering/billing platform built on Kubernetes, providing OpenAI-compatible inference access and metered settlement for agents / SDKs |
| Document scope | Defines the system topology, the 24 microservice modules and their responsibility boundaries, control plane/data plane partitioning, cluster networking, and collaboration flows; implementation details such as data models, storage parameters, and runtime configuration are carried by each module's detailed design documents |
| Core architecture principles | Separate control plane and data plane gateways; the 24 microservice gRPC servers deployed as a single unit and decoupled from the Controller through a message queue; inference services and the control plane deployed in the same Kubernetes cluster; admission control applied at the gateway and control-plane create paths rather than in a dedicated quota service |

---

## 1. Architecture Overview and Topology Design

```mermaid
flowchart TD
    subgraph clients["Clients"]
        direction LR
        USER["Agents / SDK<br/>OpenAI-compatible callers"]
        ADMIN["End-user console (/) · Admin console (/admin)<br/>CLI · automation"]
    end

    subgraph k8s["Kubernetes Cluster (control plane and inference services in one cluster)"]
        direction TB

        subgraph net["Cluster Network"]
            direction LR
            CNI["Pod network (CNI)<br/>ClusterIP Services · CoreDNS discovery"]
            ING["Envoy Gateway<br/>TLS termination · north-south entry"]
            NPOL["NetworkPolicy<br/>namespace isolation (control / data / tenant)"]
            RDMA["RDMA fabric (RoCE / InfiniBand)<br/>optional, data plane only"]
        end

        subgraph cp["Control Plane (standard network)"]
            direction TB
            CGW["Control Gateway (HTTP · grpc-gateway)<br/>/api/v1/* · /api/v1/admin/* · static console"]
            MQ["Message Queue (Kafka / NATS)"]
            CTRL["Controller<br/>(K8s resource reconciliation)"]

            subgraph grpc["gRPC Server — one Deployment, 24 microservices"]
                direction TB
                S_AUTH["auth<br/>authentication · SSO federation · API keys"]
                S_TENANCY["tenancy<br/>org/project isolation · RBAC · org quotas"]
                S_ACCOUNT["account<br/>account-data export"]
                S_MODEL["model<br/>model catalog · versions · weights · authorization"]
                S_IMAGE["image<br/>engine images · pre-pull · compatibility matrix"]
                S_INFER["infer<br/>service lifecycle · autoscaling · routing policies · load tests"]
                S_RUNTIME["runtime<br/>distributed serving runtime (AIBrix)"]
                S_ROUTING["routing<br/>routing groups · virtual models · decision trail"]
                S_PROVIDER["provider<br/>external model providers · connectivity probe"]
                S_METERING["metering<br/>token metering · vouchers · usage aggregation"]
                S_BILLING["billing<br/>pricing · charging · bills · payments · reports"]
                S_BATCH["batch<br/>batch inference jobs"]
                S_AUDIT["audit<br/>audit log · activity export"]
                S_WEBHOOK["webhook<br/>outbound events · delivery log"]
                S_NOTIFICATION["notification<br/>notification center · threshold alerts"]
                S_OBSERVABILITY["observability<br/>model dashboards · system health · error analysis"]
                S_TRACING["tracing<br/>request traces · latency breakdown"]
                S_RESOURCEMETRICS["resourcemetrics<br/>per-service CPU / memory / GPU metrics"]
                S_ACCELERATOR["accelerator<br/>accelerator inventory · health"]
                S_CLUSTER["cluster<br/>multi-cluster registry · health"]
                S_OPTIMIZATION["optimization<br/>cost optimization recommendations"]
                S_PROMPT["prompt<br/>prompt library · versioning"]
                S_EVALUATION["evaluation<br/>prompt evaluation · regression runs"]
                S_DOCS["docs<br/>API docs catalog"]
            end
        end

        subgraph dp["Data Plane (RDMA network)"]
            direction TB
            DGW["Inference Gateway<br/>Envoy + Wasm (auth · metering · routing)"]
            E["Inference Services<br/>vLLM / SGLang / TensorRT-LLM<br/>on heterogeneous accelerator cards"]
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

        ING --> CGW
        ING --> DGW
        CGW --> grpc
        grpc <-.-> MQ
        MQ <-.-> CTRL
        grpc -.-> store
        CTRL --> E
    end

    ADMIN --> ING
    USER --> ING
    DGW -.->|metering events| MQ
    E -.->|mount weights| JFS
    classDef consumer fill:#FFFFFF,stroke:#A9BABD,stroke-width:1.5px,color:#17272B
    classDef edge fill:#007F86,stroke:#00565C,stroke-width:2px,color:#FFFFFF
    classDef svc fill:#CFE9EA,stroke:#4E9CA0,stroke-width:1.5px,color:#0B383D
    classDef store fill:#E7ECEE,stroke:#9AACB1,stroke-width:1.5px,color:#23343A
    classDef net fill:#EFF3F5,stroke:#9AACB1,stroke-width:1.5px,color:#33454A
    classDef gpu fill:#FADDD1,stroke:#D2704E,stroke-width:2px,color:#71260E
    classDef dpg fill:#F6CCC0,stroke:#C4623F,stroke-width:2px,color:#5F200A
    class ADMIN,USER consumer
    class CGW,ING edge
    class MQ,CTRL,S_AUTH,S_TENANCY,S_ACCOUNT,S_MODEL,S_IMAGE,S_INFER,S_RUNTIME,S_ROUTING,S_PROVIDER,S_METERING,S_BILLING,S_BATCH,S_AUDIT,S_WEBHOOK,S_NOTIFICATION,S_OBSERVABILITY,S_TRACING,S_RESOURCEMETRICS,S_ACCELERATOR,S_CLUSTER,S_OPTIMIZATION,S_PROMPT,S_EVALUATION,S_DOCS svc
    class CNI,NPOL,RDMA net
    class LH,PG,RD,MINIO,JFS store
    class DGW dpg
    class E gpu
    style clients fill:#FAFCFC,stroke:#C6D2D4,stroke-width:1.5px,color:#3B4C50
    style k8s fill:#F6FAFA,stroke:#B4C7CA,stroke-width:1.5px,color:#2F5B60
    style net fill:#F2F6F7,stroke:#AFBEC2,stroke-width:1.5px,color:#3B4C50
    style cp fill:#EDF7F7,stroke:#9CC7C9,stroke-width:1.5px,color:#2A6A6F
    style grpc fill:#E3F1F2,stroke:#7FB6B9,stroke-width:1.5px,color:#0B383D
    style dp fill:#FDF2ED,stroke:#E4A48B,stroke-width:1.5px,color:#8A3419
    style store fill:#F5F8F9,stroke:#BCC9CD,stroke-width:1.5px,color:#47595E
```

### 1.1 Control Plane / Data Plane Separation

- **Control plane**: hosts the platform's own APIs — SSO federated login, users and organizations, API Keys, model and image management, inference service orchestration, pricing, and bills. Its entry point is the **Control Gateway** (HTTP), auto-generated from the Protobuf contract by `grpc-gateway` and backed by the unified gRPC Server.
- **Data plane**: hosts inference service APIs — OpenAI-compatible endpoints such as `/v1/chat/completions` and `/v1/embeddings`. Its entry point is the **Inference Gateway**, built on **Envoy** and serving programmatic callers such as **agents / SDKs**; authentication, metering, and routing are all performed at the gateway by **Wasm plugins**.
- **Independent deployment**: the two gateways are deployed as separate workloads. Control plane APIs and inference APIs are fully isolated in scaling, upgrade cadence, and failure domains — an overloaded or upgrading Inference Gateway does not affect platform management capabilities, and vice versa.

### 1.2 Service Topology and Communication Mechanisms

- **Unified gRPC service process**: the gRPC servers of **all 24 microservices** (`auth`, `tenancy`, `account`, `model`, `image`, `infer`, `runtime`, `routing`, `provider`, `metering`, `billing`, `batch`, `audit`, `webhook`, `notification`, `observability`, `tracing`, `resourcemetrics`, `accelerator`, `cluster`, `optimization`, `prompt`, `evaluation`, `docs`) are deployed together in a single Deployment; modules call each other in-process, with no separate process per module;
- **Message queue decouples gRPC servers from the Controller**: the message queue (Kafka / NATS) carries **message passing between the microservice gRPC servers and the Controller**. gRPC servers only expose APIs, write desired state, and publish messages; the Controller consumes messages and drives Kubernetes resources, so the two can scale and restart independently;
- **Auto-generated RESTful APIs**: control plane HTTP/JSON APIs and OpenAPI documents are auto-generated from the Protobuf contract by `grpc-gateway`;
- **The data plane bypasses business processes**: inference requests are proxied by Envoy directly to inference instances; authentication and metering happen inside Wasm plugins, so business processes never become a forwarding bottleneck and streaming responses are not copied twice.

### 1.3 Deployment and Resource Model

- **Single cluster**: inference services and the control plane are deployed in the **same Kubernetes cluster**, sharing one control plane, message queue, and observability stack;
- **Cluster networking**: the cluster runs a CNI pod network with ClusterIP Services and CoreDNS service discovery for east-west traffic; north-south traffic enters through an Envoy Gateway with TLS termination; NetworkPolicies isolate the control plane, data plane, and tenant namespaces; an optional RDMA fabric (RoCE / InfiniBand) carries the inference data path while the control plane stays on standard Ethernet (see §5.2);
- **Quota and admission control**: the platform does not introduce a standalone quota microservice; admission control lives where the traffic is — per-API-key request-rate limits and per-organization service quotas (max concurrent inference requests, max inference services, max batch jobs per day, max output tokens per request) are enforced at the inference gateway and on the control-plane create paths, and organization spend limits are enforced by the billing chain. Within those bounds, administrators create inference services against the **cluster's available resources**, and cluster capacity is naturally bounded by the Kubernetes scheduler and node resources;
- **Workload isolation**: inference workloads and control plane workloads are isolated through Namespaces, node labels, and taints/tolerations, so they coexist stably within one cluster.

---

## 2. Component Responsibilities

The dependencies and responsibility boundaries between components are as follows:

```mermaid
flowchart TD
    subgraph edge["Edge"]
        direction LR
        CGW["Control Gateway (HTTP)<br/>grpc-gateway · /api/v1/* · /api/v1/admin/*"]
        DGW["Inference Gateway<br/>Envoy + Wasm (auth · metering · routing)"]
    end

    subgraph mods["Microservices — one Deployment (24 modules)"]
        direction TB
        subgraph m_acc["Access and tenancy"]
            direction LR
            AUTH["auth"]
            TENANCY["tenancy"]
            ACCOUNT["account"]
        end
        subgraph m_mid["Models and inference"]
            direction LR
            MODEL["model"]
            IMAGE["image"]
            INFER["infer"]
            RUNTIME["runtime"]
            ROUTING["routing"]
            PROVIDER["provider"]
            ACCELERATOR["accelerator"]
            CLUSTER["cluster"]
            BATCH["batch"]
        end
        subgraph m_bill["Metering, billing and cost"]
            direction LR
            METERING["metering"]
            BILLING["billing"]
            OPTIMIZATION["optimization"]
        end
        subgraph m_obs["Observability and automation"]
            direction LR
            OBSERVABILITY["observability"]
            TRACING["tracing"]
            RESOURCEMETRICS["resourcemetrics"]
            AUDIT["audit"]
            WEBHOOK["webhook"]
            NOTIFICATION["notification"]
        end
        subgraph m_prompt["Prompt tooling and docs"]
            direction LR
            PROMPT["prompt"]
            EVALUATION["evaluation"]
            DOCS["docs"]
        end
    end

    CGW --> mods
    DGW --> RT["Inference services"]
    DGW -.->|gRPC verify API key| AUTH
    DGW -.->|metering events| MQ["Message queue (Kafka / NATS)"]

    AUTH --> PG[(PostgreSQL)]
    AUTH --> RD[(Redis)]
    AUTH --> IDP["External identity providers<br/>LDAP · OIDC · SAML · WeChat"]
    TENANCY --> PG
    ACCOUNT --> PG
    MODEL --> PG
    MODEL --> JFS[(JuiceFS)]
    IMAGE --> PG
    IMAGE --> REG[("Image registry / MinIO")]
    IMAGE --> MQ
    INFER --> PG
    INFER --> MQ
    RUNTIME --> MQ
    ROUTING --> PG
    PROVIDER --> PG
    PROVIDER --> EXT["External LLM providers<br/>DeepSeek · OpenAI-compatible"]
    BATCH --> PG
    BATCH --> FSTORE[("Batch file store<br/>input / result / error JSONL")]
    METERING --> PG
    METERING --> RD
    METERING --> MQ
    BILLING --> PG
    BILLING --> MQ
    OPTIMIZATION --> PG
    OBSERVABILITY --> PG
    TRACING --> PG
    RESOURCEMETRICS --> PG
    AUDIT --> PG
    WEBHOOK --> HOOK["Outbound HTTPS endpoints"]
    NOTIFICATION --> PG
    PROMPT --> PG
    EVALUATION --> PG
    EVALUATION --> MQ
    DOCS --> PG

    MQ --> CTRL["Controller<br/>K8s resource reconciliation"]
    MQ --> ACCELERATOR
    MQ --> CLUSTER
    CTRL --> K8S["Kubernetes API<br/>inference workloads · image pre-pull · resource samples"]
    CTRL --> RESOURCEMETRICS
    RT -.-> JFS
    classDef edge fill:#007F86,stroke:#00565C,stroke-width:2px,color:#FFFFFF
    classDef svc fill:#CFE9EA,stroke:#4E9CA0,stroke-width:1.5px,color:#0B383D
    classDef core fill:#0B4F55,stroke:#083B40,stroke-width:2px,color:#FFFFFF
    classDef store fill:#E7ECEE,stroke:#9AACB1,stroke-width:1.5px,color:#23343A
    classDef ext fill:#FBE7CF,stroke:#D08A3C,stroke-width:2px,color:#6B3B08
    classDef gpu fill:#FADDD1,stroke:#D2704E,stroke-width:2px,color:#71260E
    class CGW,DGW edge
    class MQ,CTRL core
    class AUTH,TENANCY,ACCOUNT,MODEL,IMAGE,INFER,RUNTIME,ROUTING,PROVIDER,ACCELERATOR,CLUSTER,BATCH,METERING,BILLING,OPTIMIZATION,OBSERVABILITY,TRACING,RESOURCEMETRICS,AUDIT,WEBHOOK,NOTIFICATION,PROMPT,EVALUATION,DOCS svc
    class PG,RD,JFS,REG,FSTORE store
    class IDP,EXT,HOOK ext
    class K8S,RT gpu
    style edge fill:#EFF7F7,stroke:#9CC7C9,stroke-width:1.5px,color:#2A6A6F
    style mods fill:#F5F9F9,stroke:#B4C7CA,stroke-width:1.5px,color:#2F5B60
    style m_acc fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
    style m_mid fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
    style m_bill fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
    style m_obs fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
    style m_prompt fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
```

### Microservice inventory

The single Deployment hosts 24 microservice modules. Every module is listed
below with its responsibility boundary and the feature points it implements
(backlog numbers):

| # | Module | Core responsibility | Feature points |
| --- | --- | --- | --- |
| 1 | `auth` | Local and SSO-federated authentication (LDAP / OIDC / SAML / WeChat), account binding and JIT provisioning, sessions and tokens, API Key lifecycle and verification, per-key rate limits, per-key model scoping, role-based console routing | #1, #7, #11, #22, #46 |
| 2 | `tenancy` | Organization / project isolation, members, roles and invitations (RBAC), console-surface separation, per-organization service quotas | #6, #10, #17, #51 |
| 3 | `account` | Account profile and GDPR-style account-data export bundles | #41 |
| 4 | `model` | Model catalog, versioning and rollback, weight paths and model-hub download, per-tenant model authorization | #2, #13, #32 |
| 5 | `image` | Inference engine image registry, versioning and import, node-level pre-pull tasks, model × engine × card-type compatibility matrix | #3, #19 |
| 6 | `infer` | Inference service lifecycle (deploy, scale, upgrade, rollback), autoscaling, routing policies, playground (incl. side-by-side comparison), SDK quickstart, service logs, load testing | #2, #12, #16, #20, #21, #33, #35, #45 |
| 7 | `runtime` | Platform serving scheme (plain Deployment vs AIBrix), runtime component health and registered-model projection | #47 |
| 8 | `routing` | Routing groups (virtual models) over real catalog candidates: weights, cost/latency strategies, intent rules, decision trail | #49 |
| 9 | `provider` | External provider registry, encrypted write-only keys, upstream-model curation and publishing into the catalog, connectivity probes | #48 |
| 10 | `metering` | Token metering and tamper-proof vouchers, usage records, request logs, usage dashboards, per-key usage analytics, error analysis | #4, #9, #12, #28, #31 |
| 11 | `billing` | Model × card-type price matrix, balance/quota accounts, charging and settlement, payments / invoices / auto-recharge, org spend limits, cost analytics, forecasting, billing reports | #5, #8, #11, #14, #25, #29, #36 |
| 12 | `batch` | Batch inference jobs: JSONL submission and validation, asynchronous worker, progress tracking, result / error download | #42 |
| 13 | `audit` | Audit logging of control-plane and auth events with activity export, deployment history and audit | #15, #34 |
| 14 | `webhook` | Outbound webhook endpoints, event subscriptions, HMAC signing, retry policy and delivery log | #23 |
| 15 | `notification` | In-console notification center, per-user preferences, threshold alerts and read/unread state | #26 |
| 16 | `observability` | Model performance aggregation (latency / throughput / error rate / token throughput) and platform component health & status page | #24, #30 |
| 17 | `tracing` | Per-request traces with phase-level latency breakdown (TTFT / generation / total) and request-id lookup | #27 |
| 18 | `resourcemetrics` | Per-service CPU / memory / GPU utilization series sampled by the Controller | #37 |
| 19 | `accelerator` | Accelerator inventory and health: per-node vendor, card model, driver and device-plugin status, allocated/free counts, readiness | #18 |
| 20 | `cluster` | Multi-cluster registry, cluster health and workload placement, cluster-targeted deployments | #40 |
| 21 | `optimization` | Cost optimization recommendations: rule-based, quantified, dismissible savings advice (advice-only, never acts) | #50 |
| 22 | `prompt` | Prompt library: create, version, organize, search, per-prompt usage counters, playground hand-off | #43 |
| 23 | `evaluation` | Prompt evaluation suites: variable-based cases, scored runs, run comparison | #44 |
| 24 | `docs` | Curated user-realm API catalog served to the in-console API documentation explorer | #38 |


### 2.1 `auth` — Authentication, SSO Federation, and Multi-Tenancy

**Core responsibilities:**

- **Local account authentication**: user registration, login, and password policies; credentials are stored only as salted hashes;
- **Third-party SSO federated authentication**: a built-in **pluggable identity provider (IdP) framework** connects directly to existing enterprise account systems, without re-implementing the login chain for every protocol:
  - **OIDC / OAuth 2.0**: GitHub, Google, GitLab, and self-hosted enterprise IdPs;
  - **SAML 2.0**: enterprise-grade single sign-on;
  - **LDAP / LDAPS**: connects to enterprise directory services (Active Directory, OpenLDAP, etc.), supporting user-DN bind and group-based filtering;
  - **WeChat**: QR-code login via WeChat Open Platform and WeCom (Enterprise WeChat);
- **Multiple coexisting IdPs with pluggable configuration**: administrators can enable multiple IdPs at the same time, configuring protocol parameters, visibility scope, default organization, and whether auto-registration is allowed for each one; new providers plug in as independent extensions without touching the core login logic;
- **Account binding and JIT provisioning**: stable identifiers such as `issuer + subject` (OIDC / SAML) or `DN` (LDAP) serve as the primary key of the external identity and are bound to platform accounts; the first login can auto-create an account (Just-In-Time Provisioning), or auto-creation can be disabled so that administrators pre-assign accounts;
- **Attribute and role mapping**: maps the username, email, group, and role claims returned by the IdP to platform organizations, projects, and roles, delivering permissions based on the enterprise organizational structure;
- **Sessions and tokens**: after a successful SSO login, the platform issues a session and an access token, supporting token rotation and Single Logout; local password login can be toggled independently, coexisting with SSO or disabled entirely;
- Management and isolation of the two-level (organization / project) multi-tenancy model;
- Full API Key lifecycle management (creation, verification, revocation, expiry control); the plaintext is shown only once at creation, and the system stores only the salted hash digest;
- **API Key verification for the Control Gateway and the data plane Inference Gateway**: exposes an authentication gRPC interface for the Inference Gateway's Wasm plugin to call on every request, backed by a Redis positive cache for high-concurrency, low-latency verification;

**External dependencies:** PostgreSQL (accounts, tenants, and identity bindings), Redis (auth cache and sessions), external identity providers (LDAP / OIDC / SAML / WeChat)

### 2.2 `model` — Model Asset Management

**Core responsibilities:**

- Metadata registration and version management for open-source LLMs (Qwen, DeepSeek, LLaMA, etc.);
- Maintenance of model weight paths in object storage, so inference services can mount them directly through the shared file system;
- Tenant-level model authorization, deciding the set of models each tenant may consume.

**External dependencies:** PostgreSQL (model metadata), MinIO and JuiceFS (weight storage)

### 2.3 `image` — Inference Image Management

**Core responsibilities:**

- Unified management of inference engine container images: registration, versioning, and adaptation relationships for the supported engine families (vLLM / SGLang / TensorRT-LLM) across multiple heterogeneous accelerator cards;
- Maintains the adaptation matrix between images and card types, engines, and model formats, providing the selectable image list for inference service orchestration;
- Drives image pre-pulling and caching: publishes pre-pull tasks to the message queue, which the Controller executes to pull images onto compute nodes ahead of time, shortening inference replica cold-start time;
- Verifies image provenance and integrity to keep untrusted images out of the inference chain.

**External dependencies:** image registry / MinIO (image and metadata storage), message queue (pre-pull task delivery)

### 2.4 `infer` — Inference Service Management

**Core responsibilities:**

- Heterogeneous compute management: unified management of **multiple heterogeneous accelerator cards** through a vendor-neutral node label and extended-resource model, so new card types are onboarded by labeling nodes rather than by changing the control plane;
- Lifecycle management of inference services: desired-state maintenance and change delivery for deployment, scaling, upgrade, and decommissioning;
- Inference engine adaptation: presents a unified OpenAI-compatible surface that hides engine differences;
- Health status and available endpoint registration of inference instances, used by the data plane gateway for routing;
- Quota-aware admission: inference services are created with the administrator-specified replica count and card types, bounded by the organization's service quota (feature #51) and the cluster's available resources.

**External dependencies:** message queue (change delivery), MinIO and JuiceFS (shared mounting of weights and images)

### 2.5 `metering` — Token Metering

**Core responsibilities:**

- Consumes metering events published by the data plane gateway's Wasm plugin, extracting token usage (Prompt / Completion / Cached / Reasoning);
- Generates tamper-proof metering vouchers;
- Delivers them asynchronously to the billing chain through the message queue, decoupling the inference chain from the accounting chain.

**External dependencies:** Redis (metering buffer), message queue (Kafka / NATS)

### 2.6 `billing` — Billing and Accounting

**Core responsibilities:**

- Maintains the "model × card type" price matrix, supporting tiered pricing and packages;
- Fund reservation at request admission and actual settlement after the request completes;
- Charge management for both account modes: balance (prepaid) and quota (postpaid);
- Bill generation, usage statistics, and reconciliation.

**External dependencies:** PostgreSQL (accounting data), message queue (metering event consumption)

### 2.7 Controller — Resource Reconciliation

**Core responsibilities:**

- Consumes change messages from the message queue and turns them into actual Kubernetes actions: creating/updating/deleting inference workloads, Services, and Endpoints;
- Executes image pre-pulling: lands the pre-pull tasks published by the `image` module onto specific nodes;
- Continuous reconciliation: observes gaps between actual and desired state and converges them, keeping inference services available;
- Decoupled from the gRPC servers through the message queue, so it can scale, restart, and upgrade independently.

**External dependencies:** message queue (message consumption), Kubernetes API, MinIO and JuiceFS (weight and image mounting)

---

## 3. Gateway Design

### 3.1 Control Gateway

- **Positioning**: the entry point for platform management APIs, serving only administrators and the console;
- **Implementation**: `grpc-gateway` auto-generates the HTTP/JSON proxy and OpenAPI documents from the Protobuf contract; all business logic lives in the unified gRPC Server;
- **Capabilities**: SSO federated login entry (authorization initiation and callback), model and image management, inference service orchestration, API Key and organization management, pricing and bill queries, and more;
- **Carries no inference traffic**: fully separated from the inference data plane for independent releases and fault isolation;
- **Admin/user surface separation**: the end-user console at `/` (API `/api/v1/*`) and the admin console at `/admin` (API `/api/v1/admin/*`) are separate surfaces with independent sessions and login flows (features #17 and #22); a request that carries a session from the wrong surface is rejected before any handler runs. User-facing account APIs (login, signup) stay under `/api/v1/auth/*`, while the admin web console is served under the `/admin` path prefix (`/admin/models`, `/admin/inference-services`, `/admin/api-keys`, …).

### 3.2 Data Plane (Inference) Gateway: Envoy + Wasm

The Inference Gateway is built on **Envoy** and implements the following capabilities at the gateway through **Wasm plugins**, so that inference traffic never passes through business processes:

| Capability | Description |
| --- | --- |
| **Authentication** | Parses the API Key from the `Authorization` header and **verifies it by calling the `auth` module's verification interface over gRPC**; invalid or revoked keys are rejected at the gateway and never reach inference instances |
| **Metering** | Parses token usage from streaming (SSE) and non-streaming responses, and emits metering events to the message queue for `metering` and `billing` to consume |
| **Routing** | Selects healthy inference instances per model, load balances, and retries on failure, shielding callers from backend instance changes |
| **Streaming pass-through** | Keeps SSE pass-through with per-event flush, avoiding buffering-induced latency |
| **Observability** | Uniformly produces access logs and metrics for capacity observation and troubleshooting |

> Note: because authentication and metering are pushed down to gateway Wasm plugins, the platform needs no standalone quota/rate-limiting service. Admission control is applied where the traffic is: per-API-key request-rate limits and per-organization service quotas (max concurrent requests, max inference services, max batch jobs per day, max output tokens per request) are enforced at the inference gateway and on the control-plane create paths, with organization spend limits enforced by the billing chain; the check order is rate → quota → funds.

### 3.3 The Inference Gateway's API Key Authentication Chain

Every request on the inference data plane must have its API Key verified, and the authoritative decision belongs to the `auth` module. The Wasm plugin therefore makes no business decisions of its own — it **calls the `auth` module over gRPC** to complete verification:

```mermaid
sequenceDiagram
    autonumber
    participant Agent as Agents / SDK
    participant W as Wasm plugin (Inference Gateway)
    participant Cache as Local cache (Wasm memory)
    participant Auth as auth module (gRPC)
    participant RD as Redis cache

    Agent->>W: POST /v1/chat/completions (Bearer sk-xxx)
    W->>Cache: Look up local cache by key digest
    alt Cache hit
        Cache-->>W: Return org / KeyID / role
    else Cache miss
        W->>Auth: gRPC VerifyAPIKey (key digest, model, source IP)
        Auth->>RD: Query auth positive cache
        RD-->>Auth: Return immediately on hit
        Auth-->>W: Return org / KeyID / role (or reject)
        W->>Cache: Write local cache entry with TTL
    end
    alt Verification passed
        W-->>Agent: Allow, forward to inference instance
    else Verification failed
        W-->>Agent: 401 / 403, never reaches inference instances
    end
```

- **Caller**: Envoy's Wasm plugin, initiating authentication at the request-header phase; implemented preferably with Envoy's `ext_authz` gRPC filter, or with a direct gRPC call from the Wasm plugin;
- **Callee**: the authentication gRPC interface exposed by the `auth` module, taking the API Key digest and request context (model, source IP) as input and returning the organization, Key ID, role, and the context needed for metering; the Wasm plugin **never connects to the database directly**;
- **Layered caching**: the Wasm plugin keeps a TTL-based local cache (30s recommended) keyed by the key digest; on a miss it falls back to `auth`, which is fronted by a Redis positive cache — the two cache layers keep per-request authentication overhead at the sub-millisecond level;
- **Timeout and failure policy**: authentication calls have millisecond-level timeouts and retries; on timeout or `auth` unavailability, requests are rejected under a **fail-closed** policy so that unauthorized traffic never reaches inference instances;
- **Revocation propagation**: the local cache TTL bounds the worst-case delay before a revocation takes effect; the control plane can broadcast cache invalidation to satisfy stricter immediate-revocation requirements;
- **Responsibility boundary**: the Wasm plugin only handles "calling, caching, and deciding"; authoritative judgments — API Key hash comparison, organization ownership, revocation, and expiry — always stay in the `auth` module.

---

## 4. Module Collaboration Flows

### 4.1 SSO Federated Login and Account Mapping

```mermaid
sequenceDiagram
    autonumber
    actor User as User
    participant Console as Console / CLI
    participant CGW as Control Gateway
    participant Auth as auth module
    participant IdP as External identity provider<br/>LDAP · OIDC · SAML · WeChat

    User->>Console: Choose "Sign in with xx"
    Console->>CGW: GET /api/v1/auth/sso/{provider}/authorize
    CGW->>Auth: Initiate SSO authorization
    Auth->>IdP: OIDC / SAML redirect, or LDAP directory bind query
    IdP-->>Auth: Return authorization code, assertion, or directory entry
    Auth->>IdP: Validate and exchange for user identity and attributes
    IdP-->>Auth: Return subject and attribute claims
    Auth->>Auth: Match existing binding, or JIT-provision account per policy
    Auth->>Auth: Map organization and roles, issue session and access tokens
    Auth-->>CGW: Login successful
    CGW-->>Console: Deliver session token
    Console-->>User: Enter the console
```

- **Pluggable**: adding an SSO provider only requires implementing the unified IdP plugin interface (authorization, callback, identity and attribute extraction), without touching the `auth` core login logic;
- **Security**: callbacks strictly validate `state` / `nonce` / signatures to prevent request forgery; external identities serve only as binding identifiers, and the platform always authorizes by its internal user ID.

### 4.2 One-Click Model Deployment Flow

```mermaid
sequenceDiagram
    autonumber
    actor Admin as Platform administrator
    participant CGW as Control Gateway
    participant GRPC as gRPC Server (infer / image)
    participant MQ as Message Queue
    participant CTRL as Controller
    participant K8s as Kubernetes

    Admin->>CGW: POST /api/v1/models/{id}/deploy (card type, image, and replica count)
    CGW->>GRPC: Validate parameters, write desired state, register deployment task
    GRPC->>MQ: Publish "deploy inference service" message
    MQ->>CTRL: Consume deployment message
    CTRL->>K8s: Create inference Deployment and Service (mount shared weight volume)
    K8s-->>CTRL: Inference Pods ready
    CTRL->>K8s: Register service endpoints, mark inference service available
    CTRL-->>GRPC: Report deployment result
    GRPC-->>CGW: Return deployment success
    CGW-->>Admin: 200 OK
```

### 4.3 Inference Request and Billing Loop

```mermaid
sequenceDiagram
    autonumber
    actor Agent as Agents / SDK
    participant DGW as Inference Gateway (Envoy + Wasm)
    participant Auth as auth module
    participant RT as Inference service
    participant MQ as Message Queue
    participant Metering as metering module
    participant Billing as billing module

    Agent->>DGW: POST /v1/chat/completions (Bearer sk-xxx, stream=true)
    DGW->>Auth: gRPC VerifyAPIKey (Wasm plugin call, skipped on local cache hit)
    Auth-->>DGW: Return org / KeyID / role, authentication passed
    DGW->>RT: Route by model and forward the inference request
    loop SSE event stream pass-through in real time
        RT-->>DGW: data: {"choices":[{"delta":{"content":"..."}}]}
        DGW-->>Agent: Flush and forward in real time
    end
    RT-->>DGW: Final data chunk (with usage stats) and [DONE]
    DGW-->>Agent: Forward [DONE] and close gracefully
    DGW->>MQ: Wasm plugin emits token usage metering event
    MQ->>Metering: Consume metering event, generate metering voucher
    Metering->>MQ: Publish settlement event
    MQ->>Billing: Complete actual charging and record the bill
```

---

## 5. Storage and Network

### 5.1 Storage Tiers

| Storage | Serves | Responsibility |
| --- | --- | --- |
| **Longhorn** | The Kubernetes cluster itself | Provides persistent volumes (PVCs) for the control plane and in-cluster stateful components; block storage and replica redundancy via CSI |
| **MinIO** | Inference services | S3-compatible object storage holding inference engine images and model weight originals, and serving as the backend of JuiceFS |
| **JuiceFS** | Inference services | Mounts object storage as a POSIX file system; multiple replicas share the same weights and images, achieving second-level mounting and node-level caching |

- **Separation of duties**: Longhorn serves only in-cluster components (PostgreSQL, Redis, etc.) and takes no part in the inference data path; inference weights and images uniformly go through MinIO + JuiceFS;
- **Shared acceleration**: inference replicas share one JuiceFS volume, and with node-local caching, no replica re-pulls tens of GB of weights.

### 5.2 Network Design Inside the Kubernetes Cluster

The cluster runs its own network plane, shown as the **Cluster Network** zone in the
topology diagram. It has four concerns:

| Plane | Purpose | Design |
| --- | --- | --- |
| **Pod network (east-west)** | Pod-to-pod and pod-to-service traffic | A standard CNI plugin provides the flat pod network and IPAM; every microservice, the Controller, the gateways, and the inference instances live on it |
| **Service discovery** | Stable addressing for the 24 microservice modules and the Controller | ClusterIP Services per exposure unit (gRPC server, gateways, message queue, Redis, PostgreSQL) resolved through CoreDNS; modules call each other in-process, so service discovery is only needed between workloads |
| **North-south entry** | Console and API access from outside the cluster | An Envoy Gateway terminates TLS and routes `/` (end-user console), `/admin` (admin console), `/v1/*` (inference API), and `/api/v1/*` to the Control Gateway or the Inference Gateway |
| **Isolation & policy** | Blast-radius containment between the control plane, the data plane, and tenants | Namespace separation plus NetworkPolicies that allow only the flows in the topology diagram (consoles → gateways, gateways → gRPC server/data plane, gRPC server ↔ message queue, Controller → Kubernetes API, inference → weight storage); egress from the `provider` module to external LLM endpoints and from `webhook` to customer endpoints is explicitly allow-listed |
| **Transport tiers** | Separate high-bandwidth inference traffic from management traffic | The control plane uses the standard Ethernet network; the inference data path (requests/responses, KV Cache transfer, multi-card communication) can be placed on an optional RDMA fabric (RoCE / InfiniBand) |

- **Physical/logical separation**: inference's high-volume, low-latency traffic goes over RDMA while the control plane uses the standard network, avoiding mutual interference;
- **On-demand enablement**: RDMA is an enhancement of the inference network; where RDMA is not deployed, inference traffic can fall back to the standard network;
- **Failure containment**: because the Control Gateway and the Inference Gateway are separate Envoy Gateway-backed, with independent Services and Deployments, a network or upgrade fault on the inference path never blocks platform management traffic and vice versa;
- **External egress**: only two modules need outbound internet access — `provider` (external LLM providers, connect-only probes with a five-state result) and `webhook` (customer HTTPS endpoints) — plus `model` for model-hub downloads; everything else stays inside the cluster network.

---

## 6. External System Dependency Boundaries

| External system | Used by | Responsibility |
| --- | --- | --- |
| PostgreSQL | auth · tenancy · account · model · image · infer · routing · provider · metering · billing · batch · audit · webhook · notification · observability · tracing · resourcemetrics · cluster · optimization · prompt · evaluation · docs | Business data persistence and the transactional ledger |
| Redis | auth · metering | Auth cache (positive API-Key cache, sessions) and metering buffer |
| Longhorn | Control plane stateful components | Persistent storage (PVCs) for the cluster itself |
| MinIO | model · image | Object storage: model weights and inference engine images |
| JuiceFS | model · infer · Controller | Shared multi-replica mounting of weights and images |
| Kubernetes | Controller | Creation and reconciliation of inference workloads, image pre-pulling, accelerator snapshots and resource-metric samples |
| Kafka / NATS | Microservice gRPC servers · Controller | Message passing between the gRPC servers and the Controller (deployments, image pre-pulls, runtime scheme, accelerator/cluster snapshots, metering events) |
| Envoy | Inference Gateway (data plane) | Authentication, metering, and proxying of inference requests |
| Envoy Gateway / CNI / CoreDNS | All in-cluster workloads | North-south entry with TLS, pod networking and service discovery (see §5.2) |
| Image registry (Harbor) | image | Source and destination of imported inference engine images |
| External LLM providers (DeepSeek / OpenAI-compatible) | provider · infer | Upstream inference endpoints for published external models; connectivity is probed with a five-state result |
| External identity providers (LDAP / OIDC / SAML / WeChat) | auth | Third-party SSO federated login and the source of user attributes |
| Customer webhook endpoints (HTTPS) | webhook | Outbound event delivery with HMAC signing and retry policy |
| Model hubs (ModelScope / HuggingFace) | model | Model weight download into the shared JuiceFS volume |

> Authentication chain: before allowing a request through, the Inference Gateway's Wasm plugin verifies the API Key by **calling the `auth` module over gRPC**; on a Wasm local-cache miss it falls back to `auth`, which is fronted by a Redis positive cache — the two cache layers keep latency low under high concurrency, and requests are rejected fail-closed on timeout or unavailability.
