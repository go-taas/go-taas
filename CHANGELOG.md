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
