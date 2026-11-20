# Offline and Air-Gapped Deployment

| Attribute | Content |
| --- | --- |
| Feature point | Install, upgrade and operate Go TaaS with no outbound internet access: mirror container images into a private registry, stage Helm charts, and import model weights from local archives or an internal hub mirror |
| Document scope | The offline bundle contents, image build/export/push and pull-secret steps, Helm chart and third-party chart mirroring, the shared weights layout and both model-import paths, disabling public model downloads by policy, the offline values file, installation, verification and troubleshooting |
| Owning modules | `deploy/helm/go-taas` (chart) · `deploy/compose` (development stack) · `build/docker/Dockerfile` (images) · `pkg/modelhub`, `services/model` (weights import/download) · `site/docs` (this guide) |
| Related documents | [Installing Go TaaS on Kubernetes](../getting-started/installation.md) (the connected installation path, third-party components and console exposure) · [Local Model Upload](../architecture/local-model-upload.md) (archive formats and extraction safety) · [Image Management](../architecture/image-management.md) (image catalog, import and warmup) · Chart README (full values reference, in the repository at `deploy/helm/go-taas/README.md`) |
| Status | Implemented — the local model upload, Harbor image import, controller warmup (pre-pull) and the chart values below ship in the repository; this page turns them into an air-gap procedure |

---

## 0. What "offline" means here

The control plane, the console, the controller and every inference pod run without internet access. Nothing in the request path calls a public service. All external material is brought in once, on a connected staging host, and transferred as files.

| Phase | Network | What happens |
| --- | --- | --- |
| Build / mirror | connected staging host | Build or pull the platform and engine images, mirror third-party charts and images, download model weights. |
| Transfer | removable media, a one-way data diode, or a restricted internal network | Move an OCI registry export, a chart directory and model archives into the air-gapped environment. |
| Install | air-gapped cluster only | `helm install` from a local chart against the private registry, the private Harbor project and the shared weights filesystem. |
| Operate | air-gapped cluster only | The console, API, scheduler, metering and inference data plane run locally; model weights arrive by upload or by writing them into the shared filesystem. |

Three import channels cover every external dependency:

1. **Container images** — mirrored into the cluster's own registry, `hub.sudoinfotech.com/taas` in the reference installation.
2. **Helm charts and manifests** — the Go TaaS chart plus the third-party charts (KServe, Envoy Gateway, Volcano, JuiceFS CSI, cert-manager, kube-prometheus-stack) staged as `.tgz` files or OCI artifacts.
3. **Model weights** — imported from local archives through the console/API, or copied from an internal model hub mirror into the shared weights directory and registered with a weight path.

## 1. Build the offline bundle

Prepare one directory on the connected staging host and treat it as the release artifact:

| Bundle item | Where it comes from | Where it lands offline | Check |
| --- | --- | --- | --- |
| Platform images | `taas-server`, `taas-controller`, `taas-console` built from `build/docker/Dockerfile`, or pulled from the published registry | private registry `hub.sudoinfotech.com/taas` | `docker manifest inspect hub.sudoinfotech.com/taas/taas-server:<tag>` |
| Serving-engine images | the engine images registered in the catalog, for example `vllm` / `sglang` | the same private registry or the Harbor project `taas` | image digest recorded in the catalog |
| Go TaaS chart | `deploy/helm/go-taas` | `offline-bundle/charts/go-taas-<version>.tgz` | `helm show chart` |
| Third-party charts | KServe, Envoy Gateway, Volcano, JuiceFS CSI, cert-manager, kube-prometheus-stack | `offline-bundle/charts/` | `helm template` against the offline values |
| Third-party container images | the images those charts reference | private registry, with the chart values pointing at the mirrored references | `kubectl get events` after install |
| Model weights | model hub, internal model store, or a fine-tuned checkpoint | JuiceFS-backed shared weights filesystem (`/data/weights`) | catalog entry resolves and an engine pod can read the path |
| Gateway API CRDs | the Gateway API release manifest | `offline-bundle/manifests/` | `kubectl apply --dry-run=server` |
| Certificates and secrets | the organisation PKI | Kubernetes Secrets in `taas` | console and inference hostnames serve TLS |

> **Reference registry.** The chart defaults to `ghcr.io/go-taas/go-taas`. The reference installation mirrors the same three images into `hub.sudoinfotech.com/taas`, so the rendered image references become `hub.sudoinfotech.com/taas/taas-server`, `.../taas-controller` and `.../taas-console`. Set `image.registry` and `image.repository` accordingly; the chart appends the component name.

## 2. Container images

### 2.1 Build and push the platform images

On the connected staging host, build every target with the private repository as the image repository, then push:

```bash
# produces hub.sudoinfotech.com/taas/{taas-server,taas-controller,taas-console}:<tag>
make docker-build docker-push \
  IMAGE_REPO=hub.sudoinfotech.com/taas \
  IMAGE_TAG=v1.120.0

# restricted build network: point the build at internal mirrors
make docker-build \
  IMAGE_REPO=hub.sudoinfotech.com/taas \
  IMAGE_TAG=v1.120.0 \
  GOPROXY=https://goproxy.internal.example.com,direct \
  NPM_REGISTRY=https://npm.internal.example.com \
  APT_MIRROR=http://apt.internal.example.com
```

`IMAGE_TARGETS` already contains `taas-server`, `taas-controller` and `taas-console`; the console is a standalone nginx image, not embedded in the server. If the images are built elsewhere, move them with `docker save` / `docker load` or with `skopeo copy` and re-tag them into the private registry.

The cluster pulls from the private registry with a pull secret:

```bash
kubectl -n taas create secret docker-registry harbor-pull \
  --docker-server=hub.sudoinfotech.com \
  --docker-username='robot$taas+puller' \
  --docker-password='<pull-token>'
```

Reference it from the release (`imagePullSecrets: [{name: harbor-pull}]`) and keep the Harbor password in its own push secret (`taas-harbor-push`) as described in the installation guide.

### 2.2 Serving-engine images and pre-pulling

Register the engine image that inference pods will run. With Harbor configured, the console's **Images → Import image** flow copies an upstream reference into the `taas` project and registers the local reference; without Harbor, register an image reference that is already present in the private registry. The image is `taas`-scoped in the catalog and resolved by the controller when a service is created.

The platform can pre-pull an image on the compute nodes before the first deployment:

- Console: **Images** → the image's **Warm up** action.
- API: `POST /api/v1/admin/images/{imageId}:warmup`, then poll `GET /api/v1/admin/images/{imageId}/warmup-tasks` or `GET /api/v1/admin/warmup-tasks/{taskId}`.

Warmup is an asynchronous controller task with per-node results. Use it for every engine image in the bundle so the first inference service does not wait on a multi-gigabyte pull. Confirm the nodes report the image present before declaring the bundle complete.

### 2.3 Third-party component images

The platform does not vendor KServe, Envoy Gateway, Volcano, JuiceFS CSI, cert-manager or the optional monitoring stack. Mirror the images those charts reference into the private registry and override the charts' image values (repository and tag) to the mirrored references. Record every digest in the bundle manifest: a tag that resolves to a different digest after the air gap is a silent supply-chain change.

## 3. Helm charts and dependency mirrors

### 3.1 Package and publish the Go TaaS chart

The chart has no `dependencies:` block, so a plain `helm package` is the full artifact:

```bash
helm package deploy/helm/go-taas --destination offline-bundle/charts
helm push offline-bundle/charts/go-taas-*.tgz oci://hub.sudoinfotech.com/taas/charts
```

Install from the local file in the air-gapped environment:

```bash
helm install taas ./offline-bundle/charts/go-taas-<version>.tgz \
  --namespace taas --create-namespace \
  -f taas-offline-values.yaml
```

### 3.2 Mirror the third-party charts

On the connected host, resolve and download every chart at the version the installation guide pins, then move the `.tgz` files:

```bash
helm repo add kserve https://kserve.github.io/archive
helm repo add envoy-gateway https://gateway.envoyproxy.io/charts
helm repo add volcano https://volcano-sh.github.io/helm-charts
helm repo add juicefs https://juicedata.github.io/charts/
helm repo add jetstack https://charts.jetstack.io
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

helm pull kserve/kserve --version <version> --destination offline-bundle/charts
helm pull envoy-gateway/gateway-helm --version <version> --destination offline-bundle/charts
helm pull volcano/volcano --version <version> --destination offline-bundle/charts
helm pull juicefs/juicefs-csi-driver --version <version> --destination offline-bundle/charts
helm pull jetstack/cert-manager --version <version> --destination offline-bundle/charts
helm pull prometheus-community/kube-prometheus-stack --version <version> --destination offline-bundle/charts
```

Install each from its local `.tgz` (or from the OCI registry mirror) before installing Go TaaS. The Gateway API CRDs are plain YAML and belong in the same bundle; they are a cluster prerequisite, not a chart dependency.

### 3.3 Build-time language mirrors

The Go module proxy and npm registry are needed only when the images are built; they are not contacted at runtime. `make docker-build` passes `GOPROXY`, `NPM_REGISTRY` and `APT_MIRROR` as build arguments, so a build host behind a restricted network can use the organisation's mirrors or a vendored source tree. Record the mirror URLs and the resolved module checksums in the bundle manifest.

## 4. Model weights

Weights are the one artifact that must exist inside the cluster before an inference service can start. They live on a `ReadWriteMany` filesystem mounted by both the server and the inference pods — a JuiceFS-backed StorageClass in the reference deployment. The controller creates the inference-side PVC and the server mounts the same filesystem at `model.weightsDir` (for example `/data/weights`); the two PVCs only share data because the StorageClass points at the same JuiceFS volume.

### 4.1 Layout

```text
<weightsDir>/<clean name>/<version>/
```

The catalog's `weight_path` is that relative directory with a trailing slash. A model imported by upload is indistinguishable from one downloaded from a hub: the same layout, the same deploy path, and the same delete-time cleanup.

### 4.2 Import from a local archive

The supported offline path is the platform's own upload, which needs no internet:

- Console: **Models → Upload from disk**.
- API: `POST /api/v1/admin/models:upload` (`multipart/form-data`) with `file`, `name` and the required `hubModelId` (`<vendor>/<model>`); `version` defaults to `upload-<timestamp>` when omitted, and `description` is optional.

Accepted archives are `.tar`, `.tar.gz`, `.tgz` and `.zip` of a model directory. The extractor rejects path traversal, bounds expansion, refuses an empty archive and preserves world-readable files so engine pods can read them. Uploads are bounded by `model.upload.maxArchiveBytes` (200 GiB by default) and require `model.weightsDir` to be mounted; an installation without the weights mount refuses the upload instead of accepting bytes it cannot store.

For very large models, copying into the shared filesystem from a staging host and registering the model is the same operation with less HTTP overhead:

```bash
# staging host: /srv/models/Qwen2.5-7B-Instruct/{config.json,*.safetensors,...}
rsync -a --info=progress2 /srv/models/Qwen2.5-7B-Instruct/ \
  /mnt/juicefs/taas-weights/Qwen2.5-7B-Instruct/v1/
```

Then register the model with weight path `Qwen2.5-7B-Instruct/v1/` in the console or through the model API; the console does not ask for a version, and the server records an internal one. Set file mode `0644` and directory mode `0750`, matching the uploader.

### 4.3 Import from an internal model hub mirror

An organisation that already runs an internal model hub (a mirrored Hugging Face or ModelScope repository, an internal S3/MinIO bucket, or an artifact service) can keep it as the source of truth and materialise weights into the shared filesystem:

1. Mirror the model on the internal hub with the same repository id and revision the catalog records.
2. Copy the snapshot into the shared weights directory, either from a staging host or with the storage tool's own sync (`juicefs sync`, `mc mirror`, or an object-store batch job).
3. Verify a checksum or the file list against the hub manifest before registering.
4. Register the catalog model with the relative weight path.

The built-in downloader is wired to the **public** hub base URLs (`https://modelscope.cn`, `https://huggingface.co`) and exposes no chart value for a replacement base URL. An internal mirror that speaks the hub API can be placed in front of those hostnames with DNS and an HTTPS reverse proxy, but validate that the mirror serves the same list/download paths before relying on it. For a strict air gap, do not depend on that indirection: the guaranteed path is the local archive upload or the direct copy into the shared weights directory.

### 4.4 Avoid public model downloads

There is no separate "online/offline" switch for the hub downloader. The deployment avoids public downloads by not using the **Model download** action and by enforcing the boundary at the network layer:

- Deny egress from the `taas` and inference namespaces to `huggingface.co`, `modelscope.cn` and their CDN/object-storage hosts; allow only the private registry, the internal model hub, the database/MQ endpoints and the cluster's own services.
- Keep `model.weightsDir` mounted so the local upload and the shared-filesystem import remain available. Setting it empty disables the model feature entirely, including upload.
- Remove external model providers from the catalog, or leave them unconfigured; they are outbound integrations and have no place in an air gap.
- Prefer pinned digests and checksums for engine images and model snapshots; an air gap removes the ability to re-fetch a moving tag or a floating revision.

## 5. The offline values file

A minimal values file that keeps every runtime dependency inside the cluster:

```yaml
# ---- images: the private registry, never ghcr.io ---------------------------
image:
  registry: hub.sudoinfotech.com
  repository: taas
  tag: "v1.120.0"                # pin a version; never rely on :latest
imagePullSecrets:
  - name: harbor-pull

# ---- first sign-in ---------------------------------------------------------
auth:
  seedSso:
    adminLocalPassword: "<YourStr0ng!Pass>"

# ---- internal dependencies (or external endpoints inside the network) ------
dependencies:
  postgresql:
    mode: internal
    internal:
      persistence:
        enabled: true
        size: 20Gi
  redis:
    mode: internal
  rabbitmq:
    mode: internal
  keycloak:
    mode: disabled               # local accounts only; no external IdP needed

# ---- shared model weights --------------------------------------------------
taasServer:
  config:
    model:
      weightsDir: /data/weights
  extraVolumes:
    - name: model-weights
      persistentVolumeClaim:
        claimName: taas-model-weights
  extraVolumeMounts:
    - name: model-weights
      mountPath: /data/weights

# ---- private registry for image import / upload ----------------------------
harbor:
  hostname: hub.sudoinfotech.com
  project: taas
  username: admin
  existingSecret: taas-harbor-push
  existingSecretKey: harbor-password

# ---- controller: same weights filesystem, in the inference namespace -------
controller:
  config:
    controller:
      namespace: taas-infer
      weights:
        pvcName: model-weights
        mountPath: /data/weights
        storageClass: juicefs-platform-shared

# ---- optional observability: leave empty when Prometheus is not installed ---
monitoring:
  prometheusURL: ""
```

Keep the Harbor push password in the `taas-harbor-push` Secret, not in the values file. The in-cluster PostgreSQL, Redis and RabbitMQ are the simple path; if the organisation runs them externally, point `dependencies.*.mode: external` at internal endpoints and keep their credentials in an existing Secret.

## 6. Install and verify

```bash
kubectl create namespace taas
kubectl apply -f offline-bundle/manifests/gateway-api-crds.yaml
# install the third-party charts from offline-bundle/charts first
helm install taas ./offline-bundle/charts/go-taas-<version>.tgz \
  --namespace taas \
  -f taas-offline-values.yaml
```

Verification, in order:

```bash
kubectl -n taas get pods -o wide
kubectl -n taas get events --sort-by=.lastTimestamp
kubectl -n taas get pvc
kubectl -n taas port-forward svc/taas-go-taas-server 9091:9091
```

| Check | How | Expected |
| --- | --- | --- |
| Images resolve only from the private registry | `kubectl -n taas get pods -o jsonpath='{..image}'` | every image starts with `hub.sudoinfotech.com/taas/` |
| No `ImagePullBackOff` | `kubectl -n taas describe pod <pod>` | pull secret accepted, digest matches the bundle |
| Weights are visible to both sides | upload a small model, then deploy it | service reaches `running`; the engine logs show the weight path, not a download |
| The console and API work with egress denied | open the console, create a model, start an inference service | no outbound call is required; the service starts from the shared weights |
| Observability is local | if enabled, query the in-cluster Prometheus | metrics are scraped from the internal endpoints only |
| No public model download | attempt the download action with egress denied | it fails fast or is not offered; the upload path still works |

Run the same checks after every upgrade. An upgrade that introduces a new image, chart or engine default is a new offline bundle, not a values-only change.

## 7. Upgrades and refresh

1. Build a new bundle on the connected host with the new chart version, the new image digests and any new engine images.
2. Push the images and the chart to the private registry.
3. Run `helm upgrade` with the new local chart and the same offline values.
4. Pre-pull the new engine images with the warmup task before rolling the first service.
5. Keep the previous chart and image digests until the new release has served a real inference request; an air-gapped rollback is only possible from the retained bundle.

## 8. Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `ImagePullBackOff` / `failed to resolve reference` | the mirrored tag is missing, or the pull secret is absent or scoped to the wrong registry | verify the tag in Harbor and that `imagePullSecrets` names a Secret in `taas` |
| `helm install` reports a missing dependency | a third-party chart or the Gateway API CRDs were not staged | add the `.tgz` and the CRD manifest to the bundle, install them first |
| Model upload is refused | `model.weightsDir` is not mounted, or the archive exceeds the configured bound | mount the shared PVC at `/data/weights` and check `model.upload.maxArchiveBytes` |
| Inference service stays `Pending` | the accelerator label or device plugin is missing, or the weights PVC is not bound | label the GPU node, confirm `nvidia.com/gpu` is allocatable, inspect the PVC |
| Inference starts but cannot read weights | the server and the inference namespace mount different volumes | both must use the same JuiceFS-backed `ReadWriteMany` StorageClass, not two local volumes |
| A download is attempted and fails | the operator used **Model download** while egress is blocked | use **Upload from disk** or copy the snapshot into the shared weights directory |
| Observability pages are empty | `monitoring.prometheusURL` points at an unreachable public endpoint | point it at the in-cluster Prometheus or leave it empty |

## 9. What stays out of scope

The platform does not provide a generic artifact mirror, a package proxy, or a model-hub replication service. Those are organisation infrastructure: an internal registry (Harbor), an internal chart repository, and an internal model store are the inputs this guide assumes. The platform's responsibility is to consume them without reaching the public internet, which the image registry, chart values, shared weights filesystem and local model upload above provide.
