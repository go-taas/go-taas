# Platform Display Preferences and Console Polish — Architecture and Detailed Design

| Attribute | Content |
| --- | --- |
| Feature point | One system time zone, default language and currency for the whole installation; the model-download flow lives on the model catalog instead of its own page; external models are not deployable; the multi-machine layout and per-role engine parameters; the console's autoscaling labels |
| Document scope | The `platform_settings` display columns and their projection, the dual-surface read endpoint, how the consoles consume the preferences, and the console changes that ride along |
| Owning modules | `services/runtime` (settings model, validation, projection, RPC) · `services/infer` (external-model guard, per-role engine args) · `internal/controller` (multi-machine roles, stateful storm, rendezvous, per-role args) · `web` (`platform.tsx`, `display.ts`, `GatewaySettingsPage`, `ModelsPage`, `ModelDownloadsPanel`, `DeployForm`, locales) |
| Related documents | [StormService Serving Topology](./stormservice-serving-topology.md) (the layout and per-role tuning) · [Model Catalog & One-Click Deployment](./model-catalog-deployment.md) (the catalog page the download panel joined) · [Console Surface Separation](./console-surface-separation.md) |
| Status | Implemented |

---

## 1. Display preferences

The platform administrator sets three installation-wide values on `/admin/display-settings` (a page of its own, directly below the gateway settings):

| Setting | Values | Effect |
| --- | --- | --- |
| Time zone | an IANA name, or empty | every console renders timestamps in this zone; empty keeps each viewer's own zone |
| Default language | `zh` / `en` | a first-time visitor's language; an explicit choice in the language switcher always wins |
| Currency unit | `CNY` / `USD` / `EUR` / `HKD` / `JPY` | amounts, prices and cost figures are shown in this currency |

**AD1 — Stored with the settings row.** `platform_settings` gains `timezone`, `default_language` and `currency` (AutoMigrate). They are installation properties like the gateway hostname, so they live in the same singleton row.

**AD1b — Saved by their own call.** `UpdatePlatformDisplaySettings` (`PUT /api/v1/admin/platform-display-settings`) writes only the three display fields. Display preferences used to be saved by the gateway page's `UpdatePlatformSettings`, which is a full-row replace: the moment the two concerns moved onto two pages, a display save from a page that never showed the hostname would have blanked it. The repository writes the three columns (and creates the row on a fresh install), leaving the gateway fields alone.

**AD2 — Validated against closed sets.** `normaliseTimezone` requires a name `time.LoadLocation` accepts (empty is valid), `normaliseLanguage` accepts `en`/`zh`, `normaliseCurrency` accepts the five codes. A bad value returns **12905 RUNTIME_SETTING_INVALID** and nothing is saved.

**AD3 — Read on both surfaces.** `GetPlatformDisplaySettings` is bound at `/api/v1/admin/platform-display-settings` and `/api/v1/platform-display-settings`, because an amount rendered in the user console must use the same currency as the admin console. It projects only the three display fields, never the gateway credentials or hostnames.

**AD4 — One place applies it.** `web/src/display.ts` holds the current time zone and currency in a dependency-free module; `formatTime` (api.ts) and `formatMoney`/`formatAmount` read it. `PlatformDisplayProvider` (mounted above both surfaces) fetches the values once per boot and pushes them in, then applies the default language through `setLangIfUnset` (which never overrides a stored choice). The provider issues its own request because it sits above the surface context; the path follows the token's realm, since a session is only valid on its own prefix.

**AD5 — Every amount is printed in the platform's display currency.** The console has one currency; a page that appends the *stored* code (`{amount} {record.currency}`) is what kept saying USD on an installation configured for CNY. Every money renderer therefore goes through `formatMoney` (minor units), `formatMajor` (major units, e.g. a bill total) or `formatAmount` (an explicit precision), and none of them appends a stored code. A price row still shows its own currency in the *pricing* table, because there a currency is part of the data being edited, not a unit of presentation.

**AD5b — The cost and usage analytics pages are the same rule.** The sweep is not limited to the billing pages: the cost analytics cards, the cost chart tooltips, the per-key usage cards and rankings, the user-side usage-keys page, the forecasts and the optimization savings all print through the same helpers. They used to hard-code `$` in a local `costDollars` — which is exactly why the 成本 page kept saying dollars after the installation switched to CNY. A chart tooltip is not exempt either: a bare number there reads as a different unit than the axis beside it.

**AD5c — A price is authored in the settlement currency.** The set-price dialog sends no `currency` and offers no picker; the server applies `billing.currency`. Authoring a price in a *display* currency would store an amount in a unit nothing converts, and the console would then print it as if it were the settlement currency.

**AD6 — The settlement currency is shown next to the display currency.** Amounts are stored in `billing.currency` from the server configuration and printed in the display currency; without conversion those two must be the same value. The page therefore shows the configured settlement currency and warns when it differs, and an installation that prices in CNY sets `billing.currency: CNY` (the chart's `configOverrides`). A silent mismatch would put a ¥ sign on a dollar figure, which is worse than the bug it replaced.

## 2. Model downloads on the catalog

The download flow is no longer a navigation entry: `ModelDownloadsPage` became `components/ModelDownloadsPanel`, rendered by the model catalog behind a *Model downloads* toggle, with its import form collapsed until asked for. `/admin/model-downloads` redirects to `/admin/models` (`MOVED_ADMIN_ROUTES`), so a bookmark still lands on the work.

## 3. External models are not deployable

`CreateInferenceService` rejects a model whose row is external with **10106 MODEL_EXTERNAL**: a provider serves it over its own API, so the platform has no weights to place. The deploy form filters external models out of the picker, so the operator never selects one in the first place.

## 4. Multi-machine layout and per-role engine arguments

The layout formerly called `distributed` is now **multi-machine**: one replica spans one pod per worker, each on its own device and machine, with the workers rendezvousing through the headless Service a stateful storm provides (see the topology document, AD1a–AD1c). `distributed` remains accepted on input as the pre-rename name, and the console normalizes either spelling.

`role_engine_args` lets each role be tuned on its own: a role's flag overrides the shared value, the rest is inherited, and its extra arguments are appended. The deploy form renders one parameter section per role (`prefill`/`decode`, or `worker`), and `:update-topology` accepts a new override list while an omitted one keeps the stored values.

## 5. Console labels

The autoscaling wording now reads **自动扩缩容** ("autoscaling") and **扩缩容** ("scale") wherever it is a label or button; the event log keeps 最近扩缩事件 (a scaling event is an event, not the feature).

## 6. Errors

| Code | Name | When |
| --- | --- | --- |
| 12905 | `RUNTIME_SETTING_INVALID` | unknown time zone, language outside en/zh, unsupported currency |
| 10106 | `MODEL_EXTERNAL` | deploying an external (provider-served) model |
| 10318 | `INFER_ENGINE_ARGS_INVALID` | unknown role, duplicate role, invalid flag in a role override |

## 7. Acceptance criteria

- **AC1** `/admin/display-settings` edits the time zone, default language and currency; a bad value is refused with 12905 and nothing changes.
- **AC1b** Saving the display preferences leaves the gateway hostname and in-cluster URL untouched.
- **AC2** Saved values are visible on both consoles through `platform-display-settings`, and the admin settings projection carries them too.
- **AC3** Timestamps render in the configured zone; an empty zone keeps each viewer's own.
- **AC4** Every amount on the console — usage, bills, accounts, invoices, batch costs, forecasts, optimization savings, balances — carries the configured currency symbol and no stored currency code.
- **AC5** A first-time visitor gets the default language; an explicit choice is never overridden.
- **AC6** The model catalog hosts the download panel; the navigation has no download entry; the old URL redirects.
- **AC7** The deploy picker lists no external model, and the API refuses one with 10106.
- **AC8** Multi-machine serving creates one pod per worker in a stateful storm with the rendezvous flags; per-role engine arguments override the shared ones for their role only.
- **AC9** Autoscaling labels read 自动扩缩容 / 扩缩容.
