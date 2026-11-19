# Acknowledgements and Open-Source Projects

| Attribute | Content |
| --- | --- |
| Feature point | The open-source projects Go TaaS builds on, with the official project page for each and the part of the platform that depends on it |
| Document scope | Platform foundation, serving engines, model and storage layers, state and messaging, Go libraries, the web console, observability, and optional identity; the verification basis for every entry |
| Owning module | `site/docs` (this document) · `go.mod` · `web/package.json` · `deploy/` · the platform architecture documents |
| Related documents | [Architecture Design](../design/architecture.md) (system context and component boundaries) · [Installing Go TaaS on Kubernetes](../getting-started/installation.md) (the third-party components and their installation links) · [Offline and Air-Gapped Deployment](../deployment/offline-air-gap.md) (mirroring these projects' artifacts) |
| Status | Implemented — every project below has a concrete reference in `go.mod`, `web/package.json`, the deployment manifests, or the documentation set |

---

Go TaaS is an integration project: it turns a set of mature open-source components into a
multi-tenant token factory. The platform would not exist without the communities below. This page
is the detailed list; the repository [README](../../../README.md) carries the short version.

## 1. Platform foundation

| Project | Official page | What Go TaaS uses |
| --- | --- | --- |
| **Kubernetes** | <https://kubernetes.io/> | The runtime substrate: Deployments, Services, PVCs, RBAC, CRDs, DRA device classes, and the API the controller reconciles against |
| **KServe** | <https://kserve.github.io/kserve/> | The serving control plane and the `LLMInferenceService` CRD (model, template, replicas, worker, prefill, parallelism, router, scaling); the controller writes KServe resources for distributed and single-node services |
| **Envoy Gateway** | <https://gateway.envoyproxy.io/> | The north-south data plane: the Gateway and HTTPRoute that publish the console and the OpenAI-compatible inference endpoint |
| **Gateway API** | <https://gateway-api.sigs.k8s.io/> | The `Gateway`, `HTTPRoute` and related CRDs that both console and inference exposure are expressed in |
| **Volcano** | <https://volcano.sh/> | Optional gang scheduling and priority preemption for inference replicas; the controller writes the PodGroups and the Pod template's scheduler name |
| **HAMi** | <https://github.com/Project-HAMi/HAMi> | GPU slicing and vGPU partitioning for the accelerator tier; HAMi's heterogeneous device management and scheduler are not used |
| **cert-manager** | <https://cert-manager.io/> | Optional certificate issuance for the console and inference hostnames; the chart does not create the TLS Secrets itself |

## 2. Serving engines

| Project | Official page | What Go TaaS uses |
| --- | --- | --- |
| **vLLM** | <https://github.com/vllm-project/vllm> | The primary OpenAI-compatible inference engine and the engine container the KServe presets target; the platform's image catalog registers vLLM engine images and tunes their startup arguments |
| **SGLang** | <https://github.com/sgl-project/sglang> | An alternative serving engine supported by the engine/parameter model alongside vLLM, including distributed and prefill/decode-disaggregated topologies |

## 3. Models and storage

| Project | Official page | What Go TaaS uses |
| --- | --- | --- |
| **Hugging Face Hub** | <https://huggingface.co/> | The model source for the catalog's hub download flow, together with its model metadata and file layout |
| **ModelScope** | <https://modelscope.cn/> | The second model-hub source; the downloader supports both `huggingface` and `modelscope` as catalog sources |
| **JuiceFS** | <https://juicefs.com/> | The `ReadWriteMany`, JuiceFS-backed StorageClass that shares model weights between the control plane and inference pods; the reference deployment's weights filesystem |
| **MinIO** | <https://min.io/> | The local object store behind the development JuiceFS volume in `deploy/compose`; any S3-compatible store can take its place |
| **Harbor** | <https://goharbor.io/> | The cluster's internal container registry: the image import/upload flow pushes engine images into a Harbor project and registers the local reference |

## 4. State and messaging

| Project | Official page | What Go TaaS uses |
| --- | --- | --- |
| **PostgreSQL** | <https://www.postgresql.org/> | The system of record for tenants, catalog, deployments, metering, billing, audit and the rest of the platform's relational state |
| **Redis** | <https://redis.io/> | Cache and coordination for the paths that need low-latency shared state; the chart can provision it in-cluster or point at an external instance |
| **RabbitMQ** | <https://www.rabbitmq.com/> | The message queue that decouples asynchronous work: desired-state changes, metering ingestion, warmup and download status, and the runners |
| **Keycloak** | <https://www.keycloak.org/> | Optional external or chart-provisioned OIDC identity provider; the default installation uses the platform's own account store and a seeded local administrator |

## 5. Go platform libraries

| Project | Official page | What Go TaaS uses |
| --- | --- | --- |
| **gRPC** | <https://grpc.io/> | The service-to-service contract; every platform service is a gRPC service generated from `proto/taas/**` |
| **grpc-gateway** | <https://github.com/grpc-ecosystem/grpc-gateway> | The HTTP/JSON surface and the OpenAPI documents generated from the same Protobuf definitions |
| **GORM** | <https://gorm.io/> | The ORM and migration layer over PostgreSQL (and SQLite in tests), including the repository pattern used by every service |
| **Prometheus client for Go** | <https://github.com/prometheus/client_golang> | Metrics exposition on the server's monitor port and the gRPC server metrics |
| **Kubernetes client-go** | <https://github.com/kubernetes/client-go> | The controller's typed access to the Kubernetes API and the server's read-only cluster discovery |
| **Zap** | <https://github.com/uber-go/zap> | Structured logging across the server, controller and runners |

The complete dependency set is in [`go.mod`](../../../go.mod); this table lists the projects a
reader is most likely to want to know about, not every transitive module.

## 6. Web console

| Project | Official page | What Go TaaS uses |
| --- | --- | --- |
| **React** | <https://react.dev/> | The single-page console that serves both the end-user and the administrator surfaces |
| **Vite** | <https://vite.dev/> | The console's development server, build pipeline and production bundle |
| **Phosphor Icons** | <https://phosphoricons.com/> | The icon set used throughout the console |
| **TypeScript** | <https://www.typescriptlang.org/> | The console's type system, compiled by `tsc -b` as part of the production build |

The exact versions are pinned in [`web/package.json`](../../../web/package.json).

## 7. Observability

| Project | Official page | What Go TaaS uses |
| --- | --- | --- |
| **Prometheus** | <https://prometheus.io/> | Metrics scraping (the chart's PodMonitor and the server's monitor port) and the data source behind the resource-metrics and observability pages when `monitoring.prometheusURL` is set |
| **OpenTelemetry** | <https://opentelemetry.io/> | The observability stack named by the architecture: OTel-compatible traces and metrics alongside Prometheus and log aggregation |
| **Grafana** | <https://grafana.com/> | Optional dashboards when the operator installs `kube-prometheus-stack`; the platform does not embed Grafana, and its own console renders its own panels |

Request traces are stored by the platform itself (`traces` and `trace_spans`, written alongside the
request log). Jaeger is not integrated, and neither is a hosted tracing backend.

## 8. How this list is scoped

A project is listed only when at least one of these is true:

- it appears as a direct or indirect requirement in `go.mod`;
- it appears in `web/package.json`;
- it is referenced by `deploy/helm/go-taas`, `deploy/compose`, or the installation guide;
- it is part of the architecture the design documents describe and the repository depends on.

Projects that appear only as comparison points in a design document (for example managed cloud
services or evaluation tools the platform deliberately does not adopt) are not listed. Knative is
not a dependency of the platform: the distributed-inference design explicitly rejects the
Knative/Istio footprint, and the reference topology uses KServe's `LLMInferenceService` with Envoy
Gateway.

## 9. Contributing back

If you build on Go TaaS, the most useful contribution to these projects is a precise bug report or
a documentation fix upstream, with a reproduction that does not depend on your private deployment.
Within this repository, the [Contributing Guide](../../../CONTRIBUTING.md) and the
[Code of Conduct](../../../CODE_OF_CONDUCT.md) describe how to propose changes to the integrations
listed here.
