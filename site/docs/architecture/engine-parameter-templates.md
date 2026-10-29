# Engine Parameter Templates — Architecture and Detailed Design

| Attribute | Content |
| --- | --- |
| Feature point | An operator can save a set of serving-engine parameters as a named template together with the deployment context it applies to, and the deploy form loads the best-matching template instead of making the operator retype flags. Templates support create/read/update/delete plus bulk export and import |
| Document scope | The template model and its context semantics, the seven RPCs, matching and ordering, validation rules, the export document and import strategies, the console surface, error codes and acceptance criteria |
| Owning modules | `services/infer` (`template_model.go`, `template_repository.go`, `template_service.go`) · `proto/taas/infer/v1` · `web` (`EngineTemplatesPage`, `DeployForm`, `shells/nav`) |
| Related documents | [StormService Serving Topology](./stormservice-serving-topology.md) (the topology and per-role engine arguments a template stores) · [Accelerator Inventory](./accelerator-inventory.md) (where the accelerator context values come from) |
| Status | Implemented |

---

## 1. Why a template

Deploying a model means writing engine flags: `max-model-len`, `gpu-memory-utilization`, `enable-chunked-prefill`, `tensor-parallel-size`, and for a multi-machine or PD layout a second and third set of them. The same model on the same card type always wants the same values, and those values are the part an operator gets wrong. A template captures them once, tagged with the context in which they are correct.

## 2. The template

`engine_param_templates`

| Column | Type | Meaning |
| --- | --- | --- |
| `template_id` | uuid | primary key |
| `name` | varchar(128), unique | how an operator refers to the template |
| `description` | varchar(512) | free text |
| `engine` | varchar | serving engine the flags belong to (`vllm`, `sglang`) |
| `model_id` | varchar(64) | catalog model the template is tuned for; empty = any |
| `topology` | varchar | `single` / `multinode` / `pd`; empty = any |
| `accelerator` | varchar | accelerator vendor; empty = any |
| `accelerator_type` | varchar | card model; empty = any |
| `engine_args` | json | shared flags, keyed by flag name **without** leading dashes |
| `extra_args` | json | free-form argument list, appended after the shared flags |
| `role_engine_args` | json | per-role overrides (`worker` / `prefill` / `decode`) |
| `created_at` / `updated_at` / `updated_by` | | audit columns |

`model_id` is deliberately text, not `uuid`: an empty string means "any model", and Postgres rejects `''` for a `uuid` column (see AD1).

**AD1 — Context fields are nullable by value, not by NULL.** Every context field is a non-null string whose empty value means "matches everything". One sentinel carries both the "any" meaning and the query semantics, so a filter is always `value = ? OR value = ''` and no `IS NULL` branch is needed anywhere — including in the export/import round trip. The cost is that an FK-shaped column must be `varchar`; an earlier revision used `uuid` for `model_id` and the server crash-looped on AutoMigrate because Postgres refused `''`.

**AD2 — Engine is strict, the rest is lenient.** A vLLM flag is meaningless to SGLang, so the engine context is matched exactly: a template whose engine differs from the query's engine is never returned. The other context fields are preferences, not requirements, hence `= ? OR = ''`.

## 3. RPCs

| RPC | HTTP | Purpose |
| --- | --- | --- |
| `CreateEngineParamTemplate` | `POST /api/v1/admin/engine-param-templates` | create |
| `ListEngineParamTemplates` | `GET /api/v1/admin/engine-param-templates` | list, optionally narrowed by a match context, best match first |
| `GetEngineParamTemplate` | `GET /api/v1/admin/engine-param-templates/{template_id}` | read one |
| `UpdateEngineParamTemplate` | `PATCH /api/v1/admin/engine-param-templates/{template_id}` | replace content |
| `DeleteEngineParamTemplate` | `DELETE /api/v1/admin/engine-param-templates/{template_id}` | delete |
| `ExportEngineParamTemplates` | `GET /api/v1/admin/engine-param-templates:export` | every template as one JSON document |
| `ImportEngineParamTemplates` | `POST /api/v1/admin/engine-param-templates:import` | load such a document |

All seven are **admin-surface** (`/api/v1/admin/...`), because a template describes platform hardware and engine behaviour rather than one tenant's work, and because the deploy form that consumes it lives in the platform console. The console `pathBelongsToRealm` guard therefore rejects an admin call made against `/api/v1/...`.

`ListEngineParamTemplates` carries the match context as query fields (`engine`, `model_id`, `topology`, `accelerator`, `accelerator_type`) and returns each template with a read-only `match_score`; a request without a context returns everything with `match_score = 0`.

## 4. Matching

The repository builds one predicate:

```sql
engine = :engine
AND (model_id = :model_id OR model_id = '')
AND (topology = :topology OR topology = '')
AND (accelerator = :accelerator OR accelerator = '')
AND (accelerator_type = :accelerator_type OR accelerator_type = '')
```

and the service scores the survivors, most specific first:

| Context field | Score |
| --- | --- |
| `model_id` | 8 |
| `topology` | 4 |
| `accelerator` / `accelerator_type` | 2 each |

A model+topology+card template scores 16; a bare "vLLM defaults" template scores 0 and still appears, last. The model outweighs the rest because a value tuned for one model is rarely transferable, whereas a topology or card hint usually is.

Ordering is `match_score DESC, name ASC`, so a tie is stable and the list does not reshuffle between requests.

## 5. Validation

`validateTemplate` rejects, with 10322 and a message naming the field:

- an empty or whitespace-only `name`;
- an empty `engine`;
- a `topology` outside `single` / `multinode` / `pd` (legacy `distributed` is normalised to `multinode`, as it is everywhere else);
- an `engine_args` key that already carries leading dashes or a `=` — the key is the flag name alone, and the value is the argument;
- a `role_engine_args` role outside `worker` / `prefill` / `decode`.

Duplicate names map to 10321; a miss on get/update/delete maps to 10320.

| Code | Meaning |
| --- | --- |
| 10320 | template not found |
| 10321 | template name already exists |
| 10322 | invalid template |

## 6. Export and import

`ExportEngineParamTemplates` renders one versioned envelope, so a fleet's tuning can be committed to git, reviewed, and moved between installations:

```json
{
  "version": 1,
  "exportedAt": "2025-02-11T09:12:44Z",
  "templates": [
    {
      "name": "Qwen single 4090",
      "description": "…",
      "engine": "vllm",
      "modelId": "…",
      "topology": "single",
      "accelerator": "NVIDIA",
      "acceleratorType": "NVIDIA-GeForce-RTX-4090",
      "engineArgs": { "max-model-len": "8192", "gpu-memory-utilization": "0.9" },
      "extraArgs": ["--enable-chunked-prefill"],
      "roleEngineArgs": []
    }
  ]
}
```

Import takes the payload and a `strategy`:

| Strategy | Name already exists |
| --- | --- |
| `skip` (default) | left untouched, counted as `skipped` |
| `overwrite` | replaced in place, counted as `overwritten` |

A new name is created in both cases. The response reports `created`, `overwritten`, `skipped`, so an operator can tell a no-op import from a real one. `version` is checked before anything is written: an unknown version fails the whole import rather than importing half a document.

## 7. Console

`/admin/engine-templates` (`EngineTemplatesPage`) lists templates with their context rendered as a readable line — engine, model display name or `任意模型`, topology, card — plus a count of shared flags and whether a per-role override exists. Create/edit happen in a dialog that reuses the deploy form's argument editor, and the page offers export (download the JSON) and import (upload it, choose the strategy).

The deploy form (`DeployForm`) has a **parameter template** picker above the engine arguments. It queries `ListEngineParamTemplates` with the currently selected engine/model/topology/card as context, so the dropdown only offers templates that can apply, best match first. Choosing one fills the shared flags, the extra arguments and each visible role's flags, then shows `已载入模板。下方仍可继续调整，服务会保存自己的副本。` — loading a template is a copy, not a reference: later editing the template never changes a service that was already deployed from it, and the service stores its own arguments like any hand-typed deployment.

**Saving from the deploy form.** Tuning a service and only then realising the values are worth keeping is the normal order of events, so the form also *saves*: `deploy-template-save` opens a dialog that stores the parameters currently entered as a template.

| Decision | Why |
| --- | --- |
| Only filled values are captured | An input left at its default is not a tuned value, and a template full of defaults hides the three flags that matter. Empty shared/extra/role entries are dropped before the POST. |
| The context is captured too | Engine, model, topology and accelerator are what make the template appear again for the same combination and stay out of the way for others. The dialog shows that context (`template-context`) and the counts (`{args} parameters · {extra} extra arguments · {roles} role overrides`) before saving. |
| Model scope is a choice | `template-scope-select` decides whether the template claims the selected model only (the common case) or any model on the engine (`model_id` left empty). Rank still applies: a model-specific template scores above a general one. |
| Nothing to save is refused | A template with no parameters at all is rejected in the dialog instead of on the server, because an empty template is never useful. |
| The new template is selected afterwards | After a save the match query re-runs and the new template is selected, so the answer to "did it work" is the row above, not a page reload. |

## 8. Acceptance criteria

1. Creating a template with a duplicate name returns 10321; with an empty engine or an unknown topology, 10322.
2. A template whose `engine` differs from the query's is never returned; one whose model/topology/card context is empty always is.
3. `match_score` orders a full-context template above a topology-only one above a context-free one.
4. Export produces a document that imports into an empty installation with `created` equal to the exported count.
5. Re-importing the same document with `skip` creates nothing; with `overwrite` it replaces every one and `skipped` is 0.
6. In the deploy form, selecting a template fills the engine-argument inputs and the extra-argument rows.
7. A deployed service keeps its arguments after the source template is edited or deleted.
8. The deploy form saves the currently entered parameters as a template; only filled values are stored, the selected model is included unless "any model" is chosen, and a template with nothing in it is refused.
9. Saving a template does not deploy anything and does not change the service being created.

Verified live on https://taas.sudoinfotech.com: template list/create/match/export over the API, the templates page rendering, and the deploy form filling `max-model-len` = 8192, `gpu-memory-utilization` = 0.9 and an `--enable-chunked-prefill` row from a loaded template.
