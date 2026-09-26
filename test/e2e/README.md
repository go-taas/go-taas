# go-taas End-to-End Tests (Nightwatch)

Nightwatch e2e suites for the go-taas control-plane REST gateway, executed
against the local docker compose stack.

## What is covered

| Suite | Feature | File |
| --- | --- | --- |
| API key lifecycle | feature-01 `api-key-management` | `tests/apiKeyManagement.js` |
| Model catalog & one-click deployment | feature-02 `model-catalog-deployment` | `tests/modelCatalogDeployment.js` |
| Engine image management | feature-03 `image-management` | `tests/imageManagement.js` |
| Token metering & usage | feature-04 `metering` | `tests/usageMetering.js` |
| Per-tenant model authorization | feature-13 `model-authorization` | `tests/modelAuthorization.js` |
| Console surface separation | feature-17 `console-surface-separation` | `tests/consoleSurfaces.js` |
| Payments, invoices & auto-recharge | feature-14 `payments-invoices-auto-recharge` | `tests/paymentsInvoices.js` |
| Audit logging & activity export | feature-15 `audit-logging` | `tests/auditLogging.js` |
| Inference autoscaling & scale-to-zero | feature-16 `inference-autoscaling` | `tests/inferenceAutoscaling.js` |
| Accelerator inventory & health | feature-18 `accelerator-inventory` | `tests/acceleratorInventory.js` |

Cases are derived from the acceptance criteria in
`docs/design/api-key-management.md` and
`docs/design/model-catalog-deployment.md` (test names reference the AC
numbers). Console-dialog criteria (copy buttons, badge colors, polling) are
manual/E2E-UI scope and not automated here.

## How it works

- Every request is issued from a **real headless Chromium** page via
  `fetch()`, so the suites exercise the same browser HTTP stack (CORS
  preflight, custom `X-Organization-Id` header, JSON parsing) a console SPA
  would use.
- Business errors are asserted on the **body `code`** (`{code, message}`),
  never on the HTTP status: grpc-gateway renders out-of-range business codes
  with HTTP 500.
- int64 fields (ids, timestamps, totals) serialize as JSON strings; the
  suites compare against string values.
- Each run uses a unique `runId` suffix (unix seconds) for names and fresh
  organization ids (`org-e2e-<runId>`, `org-other-<runId>`), so the suites
  are repeatable against the persistent compose database.

## Prerequisites

- Node.js >= 18 and npm.
- The docker compose stack running:

  ```bash
  make compose-up   # or: docker compose -f deploy/compose/docker-compose.yaml up -d
  ```

- A Chrome/Chromium browser. The chromedriver npm package (devDependency)
  ships a matching driver; if your system Chromium major version differs,
  install the matching driver and point `CHROMEDRIVER_PATH`-style overrides
  at it (see `nightwatch.conf.js`).

## Running

```bash
cd test/e2e
npm install --registry=https://registry.npmmirror.com   # first time only
npm test                 # all suites
npm run test:auth        # feature-01 only
npm run test:model       # feature-02 model cases (tag)
npm run test:infer       # feature-02 inference-service cases (tag)
```

Environment overrides:

| Variable | Default | Meaning |
| --- | --- | --- |
| `E2E_BASE_URL` | `http://127.0.0.1:9091` | Control gateway base URL |

Reports are written to `test/e2e/reports/`.

## Notes on expected compose behavior

- The compose stack has **no controller**, so a created inference service
  legitimately stays `state=pending` with empty `endpoints` forever. The
  suites assert exactly that; `deploying/running/failed` transitions need a
  k8s cluster and are covered by the controller unit tests.
- `X-Organization-Id` is spoofable by design (transitional auth); the
  isolation cases rely on that to prove cross-org 404 semantics.
