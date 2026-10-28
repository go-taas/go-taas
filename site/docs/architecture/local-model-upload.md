# Local Model Upload — Architecture and Detailed Design

| Attribute | Content |
| --- | --- |
| Feature point | A platform administrator can fill the model catalog from their own disk: an archive of a model directory is streamed to the shared weights filesystem, extracted, and registered exactly like a hub download |
| Document scope | The streaming upload endpoint, the archive formats and their safety rules, the weights layout, the registration path, the console dialog, error handling and acceptance criteria |
| Owning modules | `pkg/server` (`model_upload.go`) · `services/model` (`upload_service.go`) · `pkg/config` (`model.upload`) · `web` (`ModelsPage`, `UploadModelDialog`) |
| Related documents | [Model Catalog and Deployment](./model-catalog-deployment.md) (the weights layout and the deploy path the upload feeds) · [Platform Display Preferences](./platform-display-preferences.md) (the catalog page the upload button sits on) |
| Status | Implemented |

---

## 1. The gap

The catalog could only be filled from a public hub. `StartModelDownload` fetched a model from ModelScope or HuggingFace, wrote it under the weights mount and registered the version — everything a serving platform needs, as long as the model is on a hub. A fine-tuned checkpoint, a model from an air-gapped installation, or anything a hub does not carry had no way in at all.

The upload closes that gap with the same tail: weights on disk, then a catalog row. A model that arrived by upload is indistinguishable from one that arrived by download — same weight-path shape, same deploy path, same delete-time cleanup.

## 2. Why an HTTP endpoint and not an RPC

The API is gRPC-JSON transcoded, and transcoding cannot carry a binary body. A model archive is tens of gigabytes; putting it in a JSON field means buffering it and base64-ing it, which triples the memory before a byte reaches the disk. The upload is therefore a plain streaming endpoint, exactly like the image upload:

```text
POST /api/v1/admin/models:upload     (multipart/form-data)
  file         the archive (.tar, .tar.gz, .tgz or .zip)
  name         catalog model name (required)
  version      catalog version (optional)
  description  model description (optional)
```

**AD1 — Stream to a staging file, then hand off.** The handler reads the multipart parts one at a time with `r.MultipartReader`, copies `file` to a temp file and keeps only the four form fields in memory. `http.MaxBytesReader` bounds the request before a byte is read, so an oversized upload is refused by the framing rather than by a full disk. The staged file is removed when the call returns, on every path.

**AD2 — The model module owns the extraction.** The handler knows nothing about archives: it stages bytes and calls `ModelUploadFunc`, which `apps/taas-server` wires to `model.Service.UploadModel`. The service has the weights directory, the repository and the audit recorder; the HTTP layer does not.

**AD3 — Synchronous.** The image upload precedent is synchronous, and the browser is what reports progress anyway (the transfer is client-bound). The console shows the upload percentage from `xhr.upload.onprogress` and switches to "extracting and registering" once the body is sent, because on a large model the last phase visibly pauses.

## 3. Where the weights land

`<weightsDir>/<clean name>/<version>/`

| Part | Rule |
| --- | --- |
| `clean name` | the catalog name with everything outside `[A-Za-z0-9._-]` replaced by `-` (a `/` becomes `-`, a space becomes `-`), so the path never needs quoting on a command line |
| `version` | the requested version, or `upload-<YYYYMMDD-HHMMSS>`; `/`, `\`, `..` and spaces are replaced |

The row's `weight_path` is that relative directory with a trailing slash, which is exactly what a hub download produces (`Qwen__Qwen2.5-0.5B/`) and what the controller mounts and points the engine at.

**AD4 — One directory per version.** Two versions of one model must not overwrite each other, so the version is part of the path. A re-upload of an existing version is refused **before** anything is extracted (see AD6), so the previously registered weights are never touched.

## 4. Archive handling

| Format | Reader |
| --- | --- |
| `.tar` | `archive/tar` |
| `.tar.gz`, `.tgz` | `compress/gzip` + `archive/tar` |
| `.zip` | `archive/zip` |

Safety rules, each of which fails the whole upload rather than skipping an entry:

- **No escaping paths.** A `..` segment is rejected outright rather than normalised away: relocating an entry silently would extract a file where the operator did not pack it, and a `..` in a model archive is a mistake or an attack, never a layout choice. An absolute path is confined to the destination root by construction.
- **No links, devices or fifos.** Only regular files and directories are extracted — a symlink is the classic escape vector.
- **Bounded expansion.** `maxUploadExtractedBytes` (4 TiB), `maxUploadEntryBytes` (512 GiB) and `maxUploadFiles` (200 000) stop a compression bomb from filling the weights filesystem.
- **Empty archive is an error.** An archive with no files would register a model that cannot serve.
- **Clean up on failure.** A half-extracted directory is worse than none — a later deploy would serve it as if it were a model — so any failure removes the destination directory.

**AD5 — Permissions match the downloader.** Directories are `0750` and files `0644`, which is what a hub download leaves behind. The weights directory is a shared read-only mount for engine pods that may not run as the server's user; making uploaded files `0600` would produce a service that starts and then cannot read its weights.

**AD6 — Refuse an impossible registration before unpacking.** A model of that name bound to a hub id is a different thing entirely (`checkHubBinding`), and a version that already exists would either be overwritten or, with the stale-directory cleanup, deleted. Both checks run first: cheap, and they keep the bytes off the disk when the answer is already no.

## 5. Registration

`RegisterModelOrCreateVersion(name, description, "", version, weightPath)` — the same call the download job uses:

- no model of that name → create the model and its first version;
- model exists, version new → append the version;
- version exists → `10102 MODEL_EXISTS` (already refused earlier, AD6).

The upload then records an audit event (`model.upload`, resource `model`) so the arrival is traceable next to downloads and deletions.

Deleting an uploaded model works unchanged: `DeleteModel` removes the weight directories each version points at, which is why the upload writes nothing outside `<weightsDir>`.

## 6. Errors

All upload failures answer the unified envelope. Business codes keep their meaning; an unexpected failure maps onto `10104 MODEL_PATH_INVALID`.

| Code | Name | When |
| --- | --- | --- |
| 10104 | `MODEL_PATH_INVALID` | missing name, name/version/description out of bounds, unknown archive extension, unreadable archive, escaping entry, expansion over the limit, empty archive, weights directory not configured |
| 10102 | `MODEL_EXISTS` | the model is bound to a hub id, or the version already exists |
| 10101 | `MODEL_NOT_FOUND` | never — a missing model is created |

## 7. Console

`/admin/models` gains **本地上传 / Upload from disk** next to the download panel and the register action. `UploadModelDialog`:

- file picker restricted to the four extensions; picking a file pre-fills the model name from the file name (minus the extension) when the operator has not typed one;
- name (required), version (optional — the server derives a timestamp), description (optional);
- a progress bar fed by `xhr.upload.onprogress`, then the "extracting and registering" state;
- on success the dialog closes and the page reloads, so the new model is immediately deployable.

## 8. Acceptance criteria

1. Uploading a `.tar.gz` of a model directory extracts it under `<weightsDir>/<clean name>/<version>/` and registers the version with that relative weight path.
2. The same works for `.zip`, `.tar` and `.tgz`.
3. An archive containing `../x` is refused and writes nothing outside the weights root.
4. An archive with an unknown extension, or with no files, is refused.
5. A second upload of an existing name+version is refused and leaves the first version's files in place.
6. A failed extraction leaves no partial directory behind.
7. The uploaded files are readable by an engine pod (`0644` files, `0750` directories).
8. Deleting the uploaded model removes its weight directories.
9. An installation without a weights directory refuses the upload with a clear message instead of accepting bytes it cannot store.

Verified live on https://taas.sudoinfotech.com: a `.tar.gz` smoke model uploaded through the console endpoint (`files: 3`, `weightPath: Upload-Smoke-Test/v2/`), the extracted files present under `/data/weights/Upload-Smoke-Test/v2/` with `0644` permissions, the catalog row listed, and the model deleted again with its weights.
