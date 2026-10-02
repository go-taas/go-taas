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
| Compatibility matrix | feature-19 `compatibility-matrix` | `tests/compatibilityMatrix.js` |
| Inference load testing | feature-20 `load-testing` | `tests/loadTesting.js` |
| SDK / Quickstart | feature-21 `sdk-quickstart` | `tests/sdkQuickstart.js` |
| Unified login & role-based routing | feature-22 `unified-login-role-routing` | `tests/unifiedLogin.js` |
| Webhook notifications & event subscriptions | feature-23 `webhook-notifications` | `tests/webhookNotifications.js` |
| Model observability dashboard | feature-24 `model-observability` | `tests/modelObservability.js` |
| Billing reports & CSV export | feature-25 `billing-reports` | `tests/billingReports.js` |
| Notification center & threshold alerts | feature-26 `notification-center` | `tests/notificationCenter.js` |
| Request tracing & latency breakdown | feature-27 `request-tracing` | `tests/requestTracing.js` |
| API key usage analytics | feature-28 `api-key-usage-analytics` | `tests/usageKeys.js` |
| Cost analytics dashboard | feature-29 `cost-analytics-dashboard` | `tests/costAnalytics.js` |
| System health & status | feature-30 `system-health-status` | `tests/systemStatus.js` |
| Error analysis | feature-31 `error-analysis` | `tests/errorAnalysis.js` |
| Model versioning & rollback | feature-32 `model-versioning` | `tests/modelVersioning.js` |
| Inference service logs viewer | feature-33 `service-logs-viewer` | `tests/serviceLogsViewer.js` |
| Deployment history & audit | feature-34 `deployment-history-audit` | `tests/deploymentHistoryAudit.js` |
| Model playground comparison | feature-35 `playground-comparison` | `tests/playgroundComparison.js` |
| Usage & cost forecasting | feature-36 `usage-cost-forecasting` | `tests/usageCostForecasting.js` |
| Inference service resource metrics | feature-37 `service-resource-metrics` | `tests/serviceResourceMetrics.js` |
| API documentation explorer | feature-38 `api-docs-explorer` | `tests/apiDocsExplorer.js` |
| Model fine-tuning management | feature-39 `model-finetuning` | `tests/modelFineTuning.js` |
| Multi-cluster management | feature-40 `multi-cluster-management` | `tests/multiClusterManagement.js` |
| Data export & privacy | feature-41 `data-export-privacy` | `tests/dataExportPrivacy.js` |

Cases are derived from the acceptance criteria in
`docs/design/api-key-management.md`,
`docs/design/model-catalog-deployment.md` and
`docs/design/webhook-notifications.md` (test names reference the AC
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
npm run test:webhook-notifications  # feature-23 only
npm run test:notification-center    # feature-26 only
npm run test:request-tracing        # feature-27 only
npm run test:usage-keys             # feature-28 only
npm run test:cost-analytics         # feature-29 only
npm run test:system-status          # feature-30 only
npm run test:error-analysis         # feature-31 only
npm run test:model-versioning       # feature-32 only
npm run test:service-logs-viewer    # feature-33 only
npm run test:deployment-history-audit  # feature-34 only
npm run test:playground-comparison  # feature-35 only
npm run test:usage-cost-forecasting # feature-36 only
npm run test:service-resource-metrics # feature-37 only
npm run test:api-docs-explorer        # feature-38 only
npm run test:model-finetuning         # feature-39 only
npm run test:multi-cluster-management # feature-40 only
npm run test:data-export-privacy      # feature-41 only
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
