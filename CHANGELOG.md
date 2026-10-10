## [1.50.1](https://github.com/go-taas/go-taas/compare/v1.50.0...v1.50.1) (2026-10-10)


### Bug Fixes

* **model:** treat non-uuid model ids as a plain catalog miss ([e9d4fbd](https://github.com/go-taas/go-taas/commit/e9d4fbd903357fb8ba0c4ee8dce96d38de07c552))

# [1.50.0](https://github.com/go-taas/go-taas/compare/v1.49.2...v1.50.0) (2026-10-10)


### Features

* **auth:** add api key model scoping proto and error code ([86ecc8c](https://github.com/go-taas/go-taas/commit/86ecc8c991a1fed3010ab81fed74dad8aba0285a))
* **auth:** enforce api key model scope in the data plane ([9b27df5](https://github.com/go-taas/go-taas/commit/9b27df5b72154282fcd0d537e933fca68cf66edd))
* **console:** add model scope ui to the user api keys page ([d8f8947](https://github.com/go-taas/go-taas/commit/d8f8947aab181c36551b5213b1814f734087b499))

## [1.49.2](https://github.com/go-taas/go-taas/compare/v1.49.1...v1.49.2) (2026-10-09)


### Bug Fixes

* **batch:** normalize proto enum status names and poll the detail page in the console ([cadf602](https://github.com/go-taas/go-taas/commit/cadf60245224905374dc1bb8b5b2fa98953b4ade))
* **batch:** promote validating jobs to in_progress in the worker ([8408ad6](https://github.com/go-taas/go-taas/commit/8408ad63b6794175abf1f6544b2ada38c795f8fb))
* **playground:** compare prompt version loosely across string and number ([d058716](https://github.com/go-taas/go-taas/commit/d05871606d37e28047fa1d66164dbc607e8fb64a))
* **prompts:** wrap bare variable names into ${var} in the console dialogs ([0e8777c](https://github.com/go-taas/go-taas/commit/0e8777c8a85adf81d67688a0ad39ab91e67bdf53))

## [1.49.1](https://github.com/go-taas/go-taas/compare/v1.49.0...v1.49.1) (2026-10-09)


### Bug Fixes

* **infer:** require a ready selected target to enable a routing policy ([6ae786d](https://github.com/go-taas/go-taas/commit/6ae786d0d48567ef312b98be5df7acf6f930ead7))

# [1.49.0](https://github.com/go-taas/go-taas/compare/v1.48.0...v1.49.0) (2026-10-08)


### Bug Fixes

* **apikeys:** add create, edit and revoke dialogs with RPM/TPM rate-limit controls ([ad77304](https://github.com/go-taas/go-taas/commit/ad77304a4de814579d29c279d27177dbda257ac3))
* **requestlogs:** add status, API-key, model and time-range filters ([2f83ee8](https://github.com/go-taas/go-taas/commit/2f83ee8ff5c0c8ff6cb2b3bccb97ea61fea7a276))


### Features

* **evaluation:** add prompt evaluation suites with real metered completion runs ([33e4008](https://github.com/go-taas/go-taas/commit/33e40086cabf112e0f68265afaaf78b36f65ac8f)), closes [#44](https://github.com/go-taas/go-taas/issues/44)

# [1.48.0](https://github.com/go-taas/go-taas/compare/v1.47.0...v1.48.0) (2026-10-08)


### Features

* **infer:** add admin inference routing policies with revisioned outbox distribution ([f091a15](https://github.com/go-taas/go-taas/commit/f091a15ae7152e61b809e4b5a56fb6a567f72152)), closes [#45](https://github.com/go-taas/go-taas/issues/45)

# [1.47.0](https://github.com/go-taas/go-taas/compare/v1.46.1...v1.47.0) (2026-10-08)


### Features

* **batch,prompt:** add batch inference and prompt management ([7218ca4](https://github.com/go-taas/go-taas/commit/7218ca4ccf69bdfeeb7e795e1df15d863537b48d))

## [1.46.1](https://github.com/go-taas/go-taas/compare/v1.46.0...v1.46.1) (2026-10-02)


### Bug Fixes

* **cluster:** add cluster_id to inference services (MC-1) ([4127797](https://github.com/go-taas/go-taas/commit/412779717445432c1720419c2451668ac61bd47b)), closes [#40](https://github.com/go-taas/go-taas/issues/40)
* **docs:** gate the docs RPC by the member role (AD-1) ([ac12515](https://github.com/go-taas/go-taas/commit/ac1251589759ab30cd9813c3a6275d3fcf04bd55))
* **export:** use user API client and gate export RPC by member role (DE-1, DE-2) ([cd26df1](https://github.com/go-taas/go-taas/commit/cd26df10ee29356ae8414553f0627c4e8311d86e)), closes [#41](https://github.com/go-taas/go-taas/issues/41)

# [1.46.0](https://github.com/go-taas/go-taas/compare/v1.45.0...v1.46.0) (2026-10-02)


### Features

* **export:** add data export and privacy (feature [#41](https://github.com/go-taas/go-taas/issues/41)) ([5f023d2](https://github.com/go-taas/go-taas/commit/5f023d22625cb7aecae42edc98379d0c62366a00))

# [1.45.0](https://github.com/go-taas/go-taas/compare/v1.44.0...v1.45.0) (2026-10-02)


### Features

* **cluster:** add multi-cluster management (feature [#40](https://github.com/go-taas/go-taas/issues/40)) ([fb4fddc](https://github.com/go-taas/go-taas/commit/fb4fddc424285706505230bb274c2603d96511b6))
* **finetune:** add model fine-tuning management (feature [#39](https://github.com/go-taas/go-taas/issues/39)) ([cd6b938](https://github.com/go-taas/go-taas/commit/cd6b938ed0763b27caa26a20e7d11e06ea533a3b))

# [1.44.0](https://github.com/go-taas/go-taas/compare/v1.43.0...v1.44.0) (2026-10-02)


### Features

* **docs:** add API documentation explorer (feature [#38](https://github.com/go-taas/go-taas/issues/38)) ([5c79741](https://github.com/go-taas/go-taas/commit/5c79741ddd0fad28dadafcdad9c232b58b508a14))

# [1.43.0](https://github.com/go-taas/go-taas/compare/v1.42.0...v1.43.0) (2026-10-02)


### Features

* **metrics:** add per-service resource metrics (feature [#37](https://github.com/go-taas/go-taas/issues/37)) ([828caba](https://github.com/go-taas/go-taas/commit/828caba0616e9778a8b0a3e60128999bd12107cb))

# [1.42.0](https://github.com/go-taas/go-taas/compare/v1.41.2...v1.42.0) (2026-10-02)


### Bug Fixes

* **nano:** address 4 in-scope Copilot findings on PR [#14](https://github.com/go-taas/go-taas/issues/14) ([86fa40b](https://github.com/go-taas/go-taas/commit/86fa40b5817e6ae2b888b16cb7ef7d4fa03c17db))
* **nano:** sum settlement terms exactly and round to whole raw once ([56e8661](https://github.com/go-taas/go-taas/commit/56e86617e883d3a558b48e8c971adb99972a10c7))


### Features

* **nano:** exact per-inference settlement amount, kept at 30-decimal raw ([37da8d9](https://github.com/go-taas/go-taas/commit/37da8d9d138eca435cc7edfdd617d9fc3869304c)), closes [#7](https://github.com/go-taas/go-taas/issues/7)

## [1.41.2](https://github.com/go-taas/go-taas/compare/v1.41.1...v1.41.2) (2026-10-01)


### Bug Fixes

* **infer:** gate ListServiceLogPods by caller role ([a0bbf7e](https://github.com/go-taas/go-taas/commit/a0bbf7e510c168b2ded0c9af8d2240e002d812cb)), closes [#33](https://github.com/go-taas/go-taas/issues/33)
* **model:** migrate stale global active-version index ([d4bd9e4](https://github.com/go-taas/go-taas/commit/d4bd9e4bc32822038636a3153d5e428b07380b62))

## [1.41.1](https://github.com/go-taas/go-taas/compare/v1.41.0...v1.41.1) (2026-10-01)


### Bug Fixes

* **infer:** gate admin log/deployment and compare RPCs by role ([5b771c4](https://github.com/go-taas/go-taas/commit/5b771c4bde705db9c6b23f8dfac42ee71e061c3b)), closes [#33](https://github.com/go-taas/go-taas/issues/33) [#34](https://github.com/go-taas/go-taas/issues/34) [#35](https://github.com/go-taas/go-taas/issues/35)
* **model:** gate admin model-version RPCs by caller role ([9206d7e](https://github.com/go-taas/go-taas/commit/9206d7e64e2ab0e83df9f1d46eaab7b32660fb99)), closes [#32](https://github.com/go-taas/go-taas/issues/32)
* **model:** make active-version unique index per-model ([0d4ea31](https://github.com/go-taas/go-taas/commit/0d4ea31a94fb442a1a378edd45858b326526521a)), closes [#32](https://github.com/go-taas/go-taas/issues/32)
* **service:** surface error state when service logs pod enumeration fails ([3626c72](https://github.com/go-taas/go-taas/commit/3626c72161d54ecc572f90761bba90b9666b4c29)), closes [#33](https://github.com/go-taas/go-taas/issues/33)

# [1.41.0](https://github.com/go-taas/go-taas/compare/v1.40.0...v1.41.0) (2026-10-01)


### Bug Fixes

* **fvt:** remove duplicate package declaration in playground compare test ([0026c9e](https://github.com/go-taas/go-taas/commit/0026c9e014f887334e62b209d2afef2c57eb8cf5))


### Features

* **forecast:** usage & cost forecasting with confidence bands ([22495e2](https://github.com/go-taas/go-taas/commit/22495e277a043ef6e518cc0843456e504cb5c389))

# [1.40.0](https://github.com/go-taas/go-taas/compare/v1.39.0...v1.40.0) (2026-10-01)


### Bug Fixes

* **fvt:** remove duplicate package declarations in fvt tests ([585e325](https://github.com/go-taas/go-taas/commit/585e325bd6eb7f488b9cc5c5af65829378b5812f))


### Features

* **playground:** model playground comparison (feature-35) ([888c7fa](https://github.com/go-taas/go-taas/commit/888c7facc31a198ce9603d324e70d1787c08fda1))

# [1.39.0](https://github.com/go-taas/go-taas/compare/v1.38.0...v1.39.0) (2026-10-01)


### Features

* **deploy:** deployment history & audit with rollback (feature-34) ([303ebd8](https://github.com/go-taas/go-taas/commit/303ebd8c2f361d26530f1ccd76596b5652a07301))

# [1.38.0](https://github.com/go-taas/go-taas/compare/v1.37.1...v1.38.0) (2026-10-01)


### Features

* **model:** model versioning & rollback (feature-32) ([1818c19](https://github.com/go-taas/go-taas/commit/1818c19854b873cde2fa21164fb8b790ca668a7e))
* **service:** inference service logs viewer (feature-33) ([fbdeba5](https://github.com/go-taas/go-taas/commit/fbdeba5dd3078a4eb819bc359cfb80aafa6f9c7a))

## [1.37.1](https://github.com/go-taas/go-taas/compare/v1.37.0...v1.37.1) (2026-10-01)


### Bug Fixes

* **cost:** bind GetCostAnalyticsOverview on the user prefix ([6cea6b3](https://github.com/go-taas/go-taas/commit/6cea6b3d5212286265209735a5baf62627e6ffe3))
* **errors:** bind GetErrorAnalysisOverview on the user prefix ([8cdf8ae](https://github.com/go-taas/go-taas/commit/8cdf8aee5e966cf694cb274bc864284e1a732aa7))
* **usage:** bind GetUsageKeysOverview on the user prefix ([e48e2c3](https://github.com/go-taas/go-taas/commit/e48e2c348d413efac123c5aa7134283e96135cfa))

# [1.37.0](https://github.com/go-taas/go-taas/compare/v1.36.0...v1.37.0) (2026-10-01)


### Features

* **errors:** error analysis (feature-31) ([5fffd81](https://github.com/go-taas/go-taas/commit/5fffd81e24a1145ecfd807ee0a45e364501271f0))

# [1.36.0](https://github.com/go-taas/go-taas/compare/v1.35.0...v1.36.0) (2026-10-01)


### Features

* **cost:** cost analytics dashboard (feature-29) ([cef0698](https://github.com/go-taas/go-taas/commit/cef06982e19fb1c20268ea4d5f52b45cd53ed871))
* **status:** system health and service status (feature-30) ([7bb5357](https://github.com/go-taas/go-taas/commit/7bb53571fbd3dabf2d50212abb18d93da8dd490c))

# [1.35.0](https://github.com/go-taas/go-taas/compare/v1.34.0...v1.35.0) (2026-10-01)


### Features

* **usage:** API key usage analytics (feature-28) ([8dc3273](https://github.com/go-taas/go-taas/commit/8dc32738f515ca6976eaae3602955e2f02f05f57))

# [1.34.0](https://github.com/go-taas/go-taas/compare/v1.33.1...v1.34.0) (2026-10-01)


### Features

* **tracing:** request tracing with latency breakdown ([a5c2909](https://github.com/go-taas/go-taas/commit/a5c290992ff6628a40fbfa2876bb76f51fb2aabe))

## [1.33.1](https://github.com/go-taas/go-taas/compare/v1.33.0...v1.33.1) (2026-10-01)


### Bug Fixes

* **notification:** refresh bell badge immediately after mark-all-read ([9cb896b](https://github.com/go-taas/go-taas/commit/9cb896bb31a01258c109c71f497b32ef74644945))

# [1.33.0](https://github.com/go-taas/go-taas/compare/v1.32.2...v1.33.0) (2026-10-01)


### Features

* **notification:** add notification center & threshold alerts (feature 26) ([255a546](https://github.com/go-taas/go-taas/commit/255a546aa32804b5fcb2c6108c81a978030c9ca7))

## [1.32.2](https://github.com/go-taas/go-taas/compare/v1.32.1...v1.32.2) (2026-10-01)


### Bug Fixes

* **billing:** gate admin billing-reports RPCs by org role ([ad4d26d](https://github.com/go-taas/go-taas/commit/ad4d26de2d8f4ecde327db045fb9ac2a1ab7b5aa))

## [1.32.1](https://github.com/go-taas/go-taas/compare/v1.32.0...v1.32.1) (2026-09-30)


### Bug Fixes

* **billing:** use realm-scoped api client on user billing reports page ([7c66c49](https://github.com/go-taas/go-taas/commit/7c66c493c36c8c8c99d3c8ae6168fbecc49d18ce))

# [1.32.0](https://github.com/go-taas/go-taas/compare/v1.31.1...v1.32.0) (2026-09-30)


### Features

* **billing:** billing reports and CSV export (feature 25) ([c62a916](https://github.com/go-taas/go-taas/commit/c62a916cea73c4be3b25446976133b3ceb42a5fe))

## [1.31.1](https://github.com/go-taas/go-taas/compare/v1.31.0...v1.31.1) (2026-09-30)


### Bug Fixes

* **observability:** extract model id from second-to-last path segment ([8bc05ca](https://github.com/go-taas/go-taas/commit/8bc05cac8a5078fedcf419f31f90effc705016cb))

# [1.31.0](https://github.com/go-taas/go-taas/compare/v1.30.0...v1.31.0) (2026-09-30)


### Features

* **observability:** model observability dashboard (feature 24) ([b32643f](https://github.com/go-taas/go-taas/commit/b32643f6237c5ab24861665c19f68dcb609a5670))

# [1.30.0](https://github.com/go-taas/go-taas/compare/v1.29.0...v1.30.0) (2026-09-29)


### Features

* **webhook:** webhook notifications and event subscriptions (feature 23) ([f612da2](https://github.com/go-taas/go-taas/commit/f612da25f5918c0fb27ce32faacb15bfc8ebd8f5))

# [1.29.0](https://github.com/go-taas/go-taas/compare/v1.28.0...v1.29.0) (2026-09-28)


### Features

* **compose:** create and clean up cluster resources on up/down ([f38b4d1](https://github.com/go-taas/go-taas/commit/f38b4d1a11402be7084984a887a450e1df4c5954))

# [1.28.0](https://github.com/go-taas/go-taas/compare/v1.27.0...v1.28.0) (2026-09-28)


### Bug Fixes

* **compose:** share the JuiceFS FUSE mount with the control plane ([9d131f8](https://github.com/go-taas/go-taas/commit/9d131f81f0bbff990f27ad770e9f99c5c7043a2e))
* **controller:** request accelerator resources and serve model from weights FS ([bea8e71](https://github.com/go-taas/go-taas/commit/bea8e7103edd58b73b5c7a388685695f51634358))
* **image:** apply the vendor-match rule across all engine accelerators ([b1c8aa0](https://github.com/go-taas/go-taas/commit/b1c8aa0a1153b96d0f9ba6826d2641ebd7c995c9))


### Features

* **compose:** mount JuiceFS weights and wire Harbor config from .env ([209de32](https://github.com/go-taas/go-taas/commit/209de32160c1ed61007fe2d13a398b6df062f3bd))
* **config:** add harbor, model weightsDir and controller weights config ([87e9525](https://github.com/go-taas/go-taas/commit/87e9525deee8ae3c4914df11dce4d7b6ab7505f5))
* **console:** redesign UI and add English/Chinese i18n ([3a48d82](https://github.com/go-taas/go-taas/commit/3a48d82d237ca6bce49999b6104fb1c32758fba4))
* **controller:** provision JuiceFS-backed weights PVC for inference pods ([8fad31d](https://github.com/go-taas/go-taas/commit/8fad31d354b33b09e89cfd36741e2b9934b64660))
* **image:** import engine images into internal Harbor project ([e6c752b](https://github.com/go-taas/go-taas/commit/e6c752bad8cceee50b7139003639f93d1bb680e0))
* **model:** download weights from ModelScope and HuggingFace hubs ([3771400](https://github.com/go-taas/go-taas/commit/37714004d676f947465366ce97b9697bed767697))

# [1.27.0](https://github.com/go-taas/go-taas/compare/v1.26.2...v1.27.0) (2026-09-27)


### Features

* **controller:** manage a real cluster via kubeconfig in compose ([283f5b4](https://github.com/go-taas/go-taas/commit/283f5b4ef5589aa9f1c0eb5af80d5606a151df98))

## [1.26.2](https://github.com/go-taas/go-taas/compare/v1.26.1...v1.26.2) (2026-09-27)


### Bug Fixes

* **auth:** omit empty attribute_mapping on provider create ([6c8a3d7](https://github.com/go-taas/go-taas/commit/6c8a3d7a7d7500d310b54fe42dc47e35b711ccf4))

## [1.26.1](https://github.com/go-taas/go-taas/compare/v1.26.0...v1.26.1) (2026-09-27)


### Bug Fixes

* **auth:** seed admin org membership and harden compose login e2e ([f163acd](https://github.com/go-taas/go-taas/commit/f163acd4390ccc14158b6eba11f1f9e4f15e16cb))

# [1.26.0](https://github.com/go-taas/go-taas/compare/v1.25.0...v1.26.0) (2026-09-27)


### Bug Fixes

* **auth:** resolve lint issues and harden compose-seed coverage ([642ddd6](https://github.com/go-taas/go-taas/commit/642ddd689aa58d91125d49fddebd438ecc40f6b7))


### Features

* **auth:** implement SSOPasswordLogin and SwitchSurface ([2d01206](https://github.com/go-taas/go-taas/commit/2d0120604041f68b7c077cf14763a6e57d180344))
* **auth:** seed the compose Keycloak provider and admin user ([1bcd67a](https://github.com/go-taas/go-taas/commit/1bcd67ac4f21d0667174e66472bd32b28e64dd7e))
* **compose:** seed Keycloak admin user and Direct Access Grants ([50c28f4](https://github.com/go-taas/go-taas/commit/50c28f47f59274d555dd81f14a12443edfb13f0d))
* **web:** add custom login pages and surface switch buttons ([a48d800](https://github.com/go-taas/go-taas/commit/a48d8007fe72faa64cc5a422c650d8a4c050d04d))

# [1.25.0](https://github.com/go-taas/go-taas/compare/v1.24.0...v1.25.0) (2026-09-27)


### Features

* **auth:** add PasswordGrant to the IdP plugin framework ([32c0489](https://github.com/go-taas/go-taas/commit/32c04894a5f0e036dfe04a491febbb9731a2453f))
* **auth:** add SSOPasswordLogin and SwitchSurface RPCs ([cc43e8a](https://github.com/go-taas/go-taas/commit/cc43e8a3168bd5b6f61faf4c98f6fa2cd6a9f763))
* **config:** add auth.adminRoles admin-role set ([3500cd5](https://github.com/go-taas/go-taas/commit/3500cd5c0fbc7325875bf092df4ce9e070375158))

# [1.24.0](https://github.com/go-taas/go-taas/compare/v1.23.0...v1.24.0) (2026-09-27)


### Bug Fixes

* **services:** resolve org from session when present ([730b4ed](https://github.com/go-taas/go-taas/commit/730b4ed514e8c328435101df229315c4423c718c)), closes [#7](https://github.com/go-taas/go-taas/issues/7)


### Features

* **console:** redirect unauthenticated pages to login ([eebd05d](https://github.com/go-taas/go-taas/commit/eebd05d801575af4e647d0d797870cc8416ae108))

# [1.23.0](https://github.com/go-taas/go-taas/compare/v1.22.0...v1.23.0) (2026-09-27)


### Features

* **ui:** use logo in the console ([0c419b6](https://github.com/go-taas/go-taas/commit/0c419b6d16bfd0da3c95836ebef6f2ba8b3dc8b3))

# [1.22.0](https://github.com/go-taas/go-taas/compare/v1.21.0...v1.22.0) (2026-09-26)


### Features

* **infer:** add SDK quickstart inference endpoint and page ([a6e4a99](https://github.com/go-taas/go-taas/commit/a6e4a99b3b65eb8f91c15662a6ec291232432d32)), closes [#21](https://github.com/go-taas/go-taas/issues/21)

# [1.21.0](https://github.com/go-taas/go-taas/compare/v1.20.0...v1.21.0) (2026-09-26)


### Features

* **controller:** make reconcile namespace configurable ([53f3fe3](https://github.com/go-taas/go-taas/commit/53f3fe346c7ba36a809c447ca4b033b089ee1696))

# [1.20.0](https://github.com/go-taas/go-taas/compare/v1.19.0...v1.20.0) (2026-09-26)


### Bug Fixes

* **infer:** create load_tests in Migrate and index it by model+state ([f421ae8](https://github.com/go-taas/go-taas/commit/f421ae89383a5729b4588d9e4e67468ac01186ea))
* **infer:** guard malformed identifiers in load-test lookups ([2b6875a](https://github.com/go-taas/go-taas/commit/2b6875a3b52a1f67672653a60b6b89bf2ff7d245))
* **infer:** return 10311 for an unknown load-test target ([94fd8a3](https://github.com/go-taas/go-taas/commit/94fd8a3388cd1394d046793b93d4c2b74f674696))


### Features

* **infer:** add inference load testing with async runner ([82ee1f7](https://github.com/go-taas/go-taas/commit/82ee1f7eb23a2c57ffe7c5dd9d25f73c8b0c75b0))
* **web:** add load testing pages on both console surfaces ([74f4e3d](https://github.com/go-taas/go-taas/commit/74f4e3d8433d2fb7d5a047b8f2005660605cb0a0))

# [1.19.0](https://github.com/go-taas/go-taas/compare/v1.18.1...v1.19.0) (2026-09-26)


### Features

* **web:** redesign console with AI-native SaaS design system ([0c63483](https://github.com/go-taas/go-taas/commit/0c6348371893ce9923b98a67cb7f1e7d1ea2742d)), closes [#7c3](https://github.com/go-taas/go-taas/issues/7c3) [#0891b2](https://github.com/go-taas/go-taas/issues/0891b2)

## [1.18.1](https://github.com/go-taas/go-taas/compare/v1.18.0...v1.18.1) (2026-09-26)


### Bug Fixes

* **image:** render departed card types in compatibility grid ([5353146](https://github.com/go-taas/go-taas/commit/53531466084d1b126d66e2c0afc136b27ff47917))

# [1.18.0](https://github.com/go-taas/go-taas/compare/v1.17.0...v1.18.0) (2026-09-26)


### Features

* **image:** add model x engine x card-type compatibility matrix ([fef65a0](https://github.com/go-taas/go-taas/commit/fef65a0260ee014a8b9c1e469ec613c1f4e8b5a5))

# [1.17.0](https://github.com/go-taas/go-taas/compare/v1.16.1...v1.17.0) (2026-09-26)


### Features

* **auth:** resolve OIDC endpoints via discovery for Keycloak ([4d1ee5d](https://github.com/go-taas/go-taas/commit/4d1ee5d09d95bc800ada166254d233a5fd0365f5))

## [1.16.1](https://github.com/go-taas/go-taas/compare/v1.16.0...v1.16.1) (2026-09-26)


### Bug Fixes

* **accelerator:** share projection cache between consumer and service ([bc1e5df](https://github.com/go-taas/go-taas/commit/bc1e5df2757e0d362bfee9bfe21c15373d67b471))

# [1.16.0](https://github.com/go-taas/go-taas/compare/v1.15.0...v1.16.0) (2026-09-26)


### Features

* **accelerator:** add inventory proto, projection cache and admin RPCs ([3331911](https://github.com/go-taas/go-taas/commit/3331911d39b95eb28a08f7926ab4ff4f1b02cc82)), closes [#18](https://github.com/go-taas/go-taas/issues/18)
* **config:** add accelerator inventory config section ([afba0d0](https://github.com/go-taas/go-taas/commit/afba0d04435aecbfe3f70b6076521715d98b9ea3))
* **controller:** collect and publish accelerator inventory snapshot ([557f161](https://github.com/go-taas/go-taas/commit/557f1617fda0c96a1497fbcd5368ed13721c9913))
* **image:** add warmup-task-for-node narrow read provider ([787adbc](https://github.com/go-taas/go-taas/commit/787adbcfddda6daa582269c1c1ba4627af94263d))
* **web:** add accelerator inventory pages on the admin console ([5be3371](https://github.com/go-taas/go-taas/commit/5be3371a5459ca5a4a753b1e9b11f7089b2d0307))

# [1.15.0](https://github.com/go-taas/go-taas/compare/v1.14.0...v1.15.0) (2026-09-25)


### Features

* **controller:** reconcile HPA lifecycle and scale-to-zero ([3ce9f75](https://github.com/go-taas/go-taas/commit/3ce9f75d1e3ca82bcd7993f94f73406e0f103704)), closes [#16](https://github.com/go-taas/go-taas/issues/16)
* **infer:** add autoscaling policy proto, error code and config ([b3e4cf7](https://github.com/go-taas/go-taas/commit/b3e4cf7c16806fc1baa6c19a90e1c1f79d7a7dfc))
* **infer:** implement autoscaling policy RPCs and concurrency consumer ([449ca5a](https://github.com/go-taas/go-taas/commit/449ca5af0715a4ab53996ab0f7fd01218ecf190e))
* **model:** add user-realm autoscaling projection and GetAvailableModel ([fbd10fe](https://github.com/go-taas/go-taas/commit/fbd10feaa9e9ad9f2e1f5e321722f1421777478a)), closes [#16](https://github.com/go-taas/go-taas/issues/16)
* **web:** add autoscaling pages on both console surfaces ([6a15c4b](https://github.com/go-taas/go-taas/commit/6a15c4bd2a499be1f6c48d9b0c0f290c234aa9f1))

# [1.14.0](https://github.com/go-taas/go-taas/compare/v1.13.0...v1.14.0) (2026-09-25)


### Features

* **audit:** add audit logging and activity export ([cafac59](https://github.com/go-taas/go-taas/commit/cafac59f1e6666ce40ec772fbacb8a50b4795663))

# [1.13.0](https://github.com/go-taas/go-taas/compare/v1.12.1...v1.13.0) (2026-09-25)


### Features

* **billing:** add payments, invoices and auto-recharge ([bbf49a2](https://github.com/go-taas/go-taas/commit/bbf49a2d8fb64c39042139dadd49247a6276e281))

## [1.12.1](https://github.com/go-taas/go-taas/compare/v1.12.0...v1.12.1) (2026-09-25)


### Bug Fixes

* **console:** make the surface router reactive to route changes ([9e4d230](https://github.com/go-taas/go-taas/commit/9e4d230699220f1579fe7d187210c9fbb3ee172d))

# [1.12.0](https://github.com/go-taas/go-taas/compare/v1.11.0...v1.12.0) (2026-09-25)


### Features

* **console:** split the end-user console from the admin console ([4f8118f](https://github.com/go-taas/go-taas/commit/4f8118f3ce2179e6bdb12d212e5a0f261f5b14d5))

# [1.11.0](https://github.com/go-taas/go-taas/compare/v1.10.0...v1.11.0) (2026-09-25)


### Features

* **model:** add per-tenant model authorization ([ff35760](https://github.com/go-taas/go-taas/commit/ff3576022f8049ad0e23cf53677c62c60ab1382e))

# [1.10.0](https://github.com/go-taas/go-taas/compare/v1.9.0...v1.10.0) (2026-09-25)


### Bug Fixes

* **nano:** correct Raw() copy, zero-value safety, parser malformed-input rejection, and doc wording ([e7445bd](https://github.com/go-taas/go-taas/commit/e7445bda4f59d36df3f7ba6ad5f1606d02e0cbd7))
* **nano:** replace if/else chain with tagged switch to satisfy golangci-lint QF1003 ([b7c01a7](https://github.com/go-taas/go-taas/commit/b7c01a79c9159ea557f6333ad5a43f5aeff27bcf))
* **nano:** return Raw() copy, reject malformed inputs like '.' or '+', fix doc phrasing ([be169e9](https://github.com/go-taas/go-taas/commit/be169e9614f25b009d931d7197715858f8aaa87e))
* **nano:** sign-normalize Format for negative amounts so Sub results render as valid decimals ([34a1469](https://github.com/go-taas/go-taas/commit/34a14697098a7c27c78e58b2c66de031fa0bf7c6))


### Features

* **nano:** exact 30-decimal XNO amount primitives for sub-cent settlement ([5cf5f11](https://github.com/go-taas/go-taas/commit/5cf5f119ad89b1c4ef6164431c6af1e9ef8c7991))

# [1.9.0](https://github.com/go-taas/go-taas/compare/v1.8.0...v1.9.0) (2026-09-24)


### Features

* **console:** add request logs and playground pages (feature-12) ([fa73c46](https://github.com/go-taas/go-taas/commit/fa73c4678645c9d8ed7dae89010d62d67bc9764d))
* **metering,infer:** add request logs and playground proxy (feature-12) ([d08b2f8](https://github.com/go-taas/go-taas/commit/d08b2f89b003af877ff393caf450925c8760c632))

# [1.8.0](https://github.com/go-taas/go-taas/compare/v1.7.0...v1.8.0) (2026-09-24)


### Features

* **auth,billing:** add per-key rate limits and org spend limits (feature-11) ([4c83708](https://github.com/go-taas/go-taas/commit/4c83708de4639bd480de779dc79b6e10aa3b8291))
* **console:** add rate limit and spend limit fields (feature-11) ([2846bea](https://github.com/go-taas/go-taas/commit/2846beaac6532e5946c73c31ece018037de1cb46))

# [1.7.0](https://github.com/go-taas/go-taas/compare/v1.6.0...v1.7.0) (2026-09-24)


### Features

* **console:** add members and invitations pages (feature-10) ([94a5265](https://github.com/go-taas/go-taas/commit/94a52653498328e07be8d8c0764cf4571d2ef3d4))
* **tenancy:** add org members roles and invitations RBAC (feature-10) ([3768626](https://github.com/go-taas/go-taas/commit/3768626c5a553e8c937f50243d654c371f78c62e))

# [1.6.0](https://github.com/go-taas/go-taas/compare/v1.5.0...v1.6.0) (2026-09-24)


### Features

* **console:** add usage dashboard with chart, metric toggle, CSV export and balance widget (feature-09) ([4cb4a42](https://github.com/go-taas/go-taas/commit/4cb4a42d0061cfa2d8cbbc8c42b082e29af7ed0b))
* **metering:** add usage dashboard and per-request cost attribution (feature-09) ([9c93881](https://github.com/go-taas/go-taas/commit/9c93881c26bb0b68cb78f4c2e6c1d4814220d301))

# [1.5.0](https://github.com/go-taas/go-taas/compare/v1.4.0...v1.5.0) (2026-09-24)


### Features

* **billing:** implement balance and quota account modes with ledger (feature-08) ([f4e0e95](https://github.com/go-taas/go-taas/commit/f4e0e956c3f8a33e7fd9f26262ecab834d78fa3a))
* **console:** add billing accounts page with recharge and quota dialogs (feature-08) ([e50716f](https://github.com/go-taas/go-taas/commit/e50716f7a206d9d7d082b38f8dd0b3b516ea08d5))

# [1.4.0](https://github.com/go-taas/go-taas/compare/v1.3.0...v1.4.0) (2026-09-24)


### Features

* **auth:** implement SSO federation with OIDC, SAML and LDAP providers (feature-07) ([7222f16](https://github.com/go-taas/go-taas/commit/7222f161a96a23e97cfd3ecf2cdf33579459da1e))
* **console:** add organizations and projects pages with org switcher (feature-06) ([a286f85](https://github.com/go-taas/go-taas/commit/a286f85233ff1bea8b054bbf77675ff16299155c))
* **console:** add SSO login, providers and identity bindings pages (feature-07) ([4a5f0a2](https://github.com/go-taas/go-taas/commit/4a5f0a22fe98df3e5bbd4053d0f772f3f8a78348))
* **tenancy:** implement organization and project multi-tenancy (feature-06) ([0d3df75](https://github.com/go-taas/go-taas/commit/0d3df7591a868dc3feec9fd0f8730ad39597d8a6))

# [1.3.0](https://github.com/go-taas/go-taas/compare/v1.2.0...v1.3.0) (2026-09-24)


### Features

* **site:** add bilingual static intro site for GitHub Pages ([cea7734](https://github.com/go-taas/go-taas/commit/cea773457b603b9123a7fe60fe12e8f1b38c8fb8))

# [1.2.0](https://github.com/go-taas/go-taas/compare/v1.1.0...v1.2.0) (2026-09-24)


### Bug Fixes

* **metering:** map malformed voucher ids to 10403 instead of internal error ([95400dc](https://github.com/go-taas/go-taas/commit/95400dcffe8b6b8b6220be4815d3474bf77d9155))
* **web:** repair console routing and api-key list, polish compose workflow ([e6d803c](https://github.com/go-taas/go-taas/commit/e6d803c2c855707c24ab38f839bfc68d075e4ff4))


### Features

* **api,web:** split admin surface under /api/v1/admin and /admin console prefix ([da83572](https://github.com/go-taas/go-taas/commit/da83572ff244c13a2b547fd12d5e66739af105b8))
* **billing:** implement price matrix, tiered pricing and charging engine (feature-05) ([c17da8a](https://github.com/go-taas/go-taas/commit/c17da8a3a130d5c28ad781506a4a59c946b30960))
* **image:** add db-backed image registry and warmup pre-pull ([d7eaa31](https://github.com/go-taas/go-taas/commit/d7eaa315e2215406682320c81c836cc0dec041c9))
* **metering:** add token vouchers, hourly settlement and usage queries ([ca469a7](https://github.com/go-taas/go-taas/commit/ca469a71555278694ee215a11a37d932f428ebbf))

# [1.1.0](https://github.com/go-taas/go-taas/compare/v1.0.0...v1.1.0) (2026-09-23)


### Features

* **auth:** add API key lifecycle management ([b4f8f9d](https://github.com/go-taas/go-taas/commit/b4f8f9de77c5f1216d92c05887c4e6d903bac11e))
* **model,infer:** add model catalog and one-click deployment ([9da37a0](https://github.com/go-taas/go-taas/commit/9da37a08b57a5cbc1c9d3464c6fc58cb718f3579))
* **web:** add admin console for api keys, model catalog and inference services ([57969d4](https://github.com/go-taas/go-taas/commit/57969d4b33b1791ee7fdc06833b4d0e4c2ea3a95))

# 1.0.0 (2026-09-16)


### Bug Fixes

* address PR review build workflow issues ([802970f](https://github.com/go-taas/go-taas/commit/802970fff9e9efda56ef6d1e41479e43d1ff5292))


### Features

* scaffold Go codebase foundation ([701cab5](https://github.com/go-taas/go-taas/commit/701cab58226f7a7c3f3f95bc932d532bfeecf667))

# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions and entries below this notice are generated automatically by
semantic-release from Conventional Commits; do not edit them by hand.
