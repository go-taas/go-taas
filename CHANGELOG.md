# [1.87.0](https://github.com/go-taas/go-taas/compare/v1.86.0...v1.87.0) (2026-10-25)


### Features

* **gateway:** give the data plane its own gateway ([c2b5dcf](https://github.com/go-taas/go-taas/commit/c2b5dcfdf22a2c09763dd49300a2d37ecfb8b465))

# [1.86.0](https://github.com/go-taas/go-taas/compare/v1.85.0...v1.86.0) (2026-10-25)


### Bug Fixes

* **model:** accept the served model name in model authorization ([0cd132f](https://github.com/go-taas/go-taas/commit/0cd132f1befb75bdd3aa83c26eb0388642db94ce))
* **mq:** give each consumer its own queue so no event is split away ([60fa135](https://github.com/go-taas/go-taas/commit/60fa1354e4913933cfe7bfe0cb7e7f2c2009bc89))


### Features

* **gateway:** ask the engine for usage on a streamed response ([39a8ab3](https://github.com/go-taas/go-taas/commit/39a8ab3f15df25b541d0fadc9722f4a7ef5dab54))

# [1.85.0](https://github.com/go-taas/go-taas/compare/v1.84.1...v1.85.0) (2026-10-25)


### Features

* **gateway:** add the internal API the data-plane plugin calls ([fdbad7b](https://github.com/go-taas/go-taas/commit/fdbad7b888cc97f474bc85c260eea0b093c6a144))
* **gateway:** add the Wasm plugin that authenticates and meters inference ([4d037fc](https://github.com/go-taas/go-taas/commit/4d037fc2a72841401e9cf11c3a387a895b6d8b5e))

## [1.84.1](https://github.com/go-taas/go-taas/compare/v1.84.0...v1.84.1) (2026-10-25)


### Bug Fixes

* **console:** tighten the chart's axis steps so memory is not half empty ([3df3779](https://github.com/go-taas/go-taas/commit/3df37797d39f99b42a5ed1b45b0f91b2826e7f55))
* **metrics:** derive card capacity from used + free, and deflake a MQ test ([a4131c2](https://github.com/go-taas/go-taas/commit/a4131c23d08c4f5c21a9f601d9a2ad8e8d000250))
* **metrics:** reject non-finite prometheus samples so NaN never reaches the API ([7a25bd5](https://github.com/go-taas/go-taas/commit/7a25bd513ea6ae707ff9f56b8de4e6d4f0979d92))

# [1.84.0](https://github.com/go-taas/go-taas/compare/v1.83.0...v1.84.0) (2026-10-25)


### Bug Fixes

* **console:** scale the metrics axis to the data and fix the replica GPU cell ([b494243](https://github.com/go-taas/go-taas/commit/b4942430e062617b6f898cc6cadab037218f2e20))
* **helm:** default the inference PodMonitor values when they are absent ([cd94b93](https://github.com/go-taas/go-taas/commit/cd94b93158c2f554245eb0c9b1bf132ab369002e))


### Features

* **metrics:** add GPU memory and the engine's own latency telemetry ([962c4bb](https://github.com/go-taas/go-taas/commit/962c4bb76b48372f2732d10ebe565e2aa19a4409))

# [1.83.0](https://github.com/go-taas/go-taas/compare/v1.82.0...v1.83.0) (2026-10-25)


### Bug Fixes

* **metrics:** scope the PromQL queries to the real inference namespace ([f4df7d0](https://github.com/go-taas/go-taas/commit/f4df7d0b879e5c87da93b9683151862e5c186584))
* **metrics:** send the rate window in a form PromQL accepts ([2532ade](https://github.com/go-taas/go-taas/commit/2532ade5ffbfcef1da3092cd901f958129838aed))


### Features

* **metrics:** read service metrics from Prometheus and fix GPU availability ([76628dc](https://github.com/go-taas/go-taas/commit/76628dc579ea1917e0166ba0fb5a3fc8e126b170))

# [1.82.0](https://github.com/go-taas/go-taas/compare/v1.81.3...v1.82.0) (2026-10-25)


### Bug Fixes

* **controller:** retry the database at startup so metrics survive a rollout ([4d6736f](https://github.com/go-taas/go-taas/commit/4d6736f18265a001608a43dae670c4ecdf655cb6))


### Features

* **infer:** enforce the service access policy where the platform is the caller ([f903638](https://github.com/go-taas/go-taas/commit/f9036380f261a19b0d3628e7e8681e30f5b08ab7))
* **infer:** make inference services platform assets with an access policy ([5addf8b](https://github.com/go-taas/go-taas/commit/5addf8bfb642d0a56a130f475c92bcee1887736e))

## [1.81.3](https://github.com/go-taas/go-taas/compare/v1.81.2...v1.81.3) (2026-10-25)


### Bug Fixes

* **console:** put the avatar on the left of the account block ([7b5dccc](https://github.com/go-taas/go-taas/commit/7b5dccc9cb15e4c78dc90c5be4e262bbd2714503))

## [1.81.2](https://github.com/go-taas/go-taas/compare/v1.81.1...v1.81.2) (2026-10-25)


### Bug Fixes

* **console:** pin the account block to the sidebar bottom and enrich it ([5880b83](https://github.com/go-taas/go-taas/commit/5880b830a650ffc44773670dd657513d710ea100))
* **console:** stop the settings summary from clipping its URLs ([92eeb16](https://github.com/go-taas/go-taas/commit/92eeb160076953840aaca4345e485159d4d70bcb))

## [1.81.1](https://github.com/go-taas/go-taas/compare/v1.81.0...v1.81.1) (2026-10-25)


### Bug Fixes

* **runtime:** make the saved inference hostname actually publish the gateway ([64e9ffd](https://github.com/go-taas/go-taas/commit/64e9ffdcb8176f7485d853fedc3474e1c7245349))

# [1.81.0](https://github.com/go-taas/go-taas/compare/v1.80.0...v1.81.0) (2026-10-25)


### Bug Fixes

* **console:** treat a wrong-console session as "sign in as admin" ([0506321](https://github.com/go-taas/go-taas/commit/0506321e56a3b454f0f61546fa8a90774c30608a))
* **runtime:** show the actor's display name on the platform settings page ([2f21bf6](https://github.com/go-taas/go-taas/commit/2f21bf6248af5b5e23dd01cc202e342bbc24d23d))


### Features

* separate the control and data planes, with a configurable inference hostname ([b9f5eb6](https://github.com/go-taas/go-taas/commit/b9f5eb6587f2ed3084c6e5101f7507abc8b73427))

# [1.80.0](https://github.com/go-taas/go-taas/compare/v1.79.2...v1.80.0) (2026-10-25)


### Features

* **infer:** make the playground test request actually call the model ([69a890f](https://github.com/go-taas/go-taas/commit/69a890f3b87f5fabeba372d4da220b8d4158dc6b))

## [1.79.2](https://github.com/go-taas/go-taas/compare/v1.79.1...v1.79.2) (2026-10-25)


### Bug Fixes

* **controller:** make deployed models routable through the serving gateway ([353fe78](https://github.com/go-taas/go-taas/commit/353fe787216d5d08b75ffab7e6ab719c90694d81))

## [1.79.1](https://github.com/go-taas/go-taas/compare/v1.79.0...v1.79.1) (2026-10-25)


### Bug Fixes

* **console:** use the served model name in the quickstart snippet ([bdfcfd9](https://github.com/go-taas/go-taas/commit/bdfcfd9207f5842ee292acf400050611061e891d))
* **infer:** derive the quickstart endpoint instead of failing with 500 ([3424d0a](https://github.com/go-taas/go-taas/commit/3424d0a2d82593c5f61f8b74656e81efd6dedf36))
* **k8s:** omit the default :80 from the discovered gateway URL ([31b9730](https://github.com/go-taas/go-taas/commit/31b9730a3f279ecb4e8e109126b0949c07f36762))

# [1.79.0](https://github.com/go-taas/go-taas/compare/v1.78.2...v1.79.0) (2026-10-25)


### Bug Fixes

* **console:** drop organization wording from the deploy form ([ef68496](https://github.com/go-taas/go-taas/commit/ef684963d0650b89d1aac8e0367bdd0f98edd480))
* **console:** show a loading state instead of an empty audit log ([de316ab](https://github.com/go-taas/go-taas/commit/de316ab5b9767c0ce066358602b6ec2e19be3694))


### Features

* **console:** platform-wide operator surface, no organization switcher ([7bcf4b5](https://github.com/go-taas/go-taas/commit/7bcf4b5a7e2ab312bdde56c189f47180aabedb98))
* **console:** resolve proto enum names and add audit organization filter ([2bfe17d](https://github.com/go-taas/go-taas/commit/2bfe17d90a5a14441f3d436a0bcf7469f2245fdb))

## [1.78.2](https://github.com/go-taas/go-taas/compare/v1.78.1...v1.78.2) (2026-10-25)


### Bug Fixes

* **console:** only suggest in-fleet card types in the deploy form ([7999dc6](https://github.com/go-taas/go-taas/commit/7999dc60004c803ba3023ecb69ca29706a333072))
* **inventory:** report the card model as the card type, not "gpu" ([2d2fbc2](https://github.com/go-taas/go-taas/commit/2d2fbc2d2aad1bff1a405be92b8173594358a790))

## [1.78.1](https://github.com/go-taas/go-taas/compare/v1.78.0...v1.78.1) (2026-10-25)


### Bug Fixes

* **console:** normalize the autoscaling state enum and translate On/Off ([6afdffe](https://github.com/go-taas/go-taas/commit/6afdffe9612f223b5174be61295f07c3340c6ca5))
* **console:** translate the deploy dialog's remaining English strings ([5c4dda3](https://github.com/go-taas/go-taas/commit/5c4dda3683035397c9292070ca798e1508ee9d97))
* **controller:** bound the deploy readiness wait and stop queue stalls ([bfcc223](https://github.com/go-taas/go-taas/commit/bfcc223ccc86087ff351ece9338b077d6707e418))
* **controller:** pin the accelerator label the nodes actually carry ([42bb087](https://github.com/go-taas/go-taas/commit/42bb087a5f2468a6a69831567c3a32cafbe8ffa0))
* **infer:** don't reject a deploy whose optional card type is empty ([d98c642](https://github.com/go-taas/go-taas/commit/d98c642689ac916017ca2ead932181d4438e8492))
* **mq:** bound the consumer's unacked window ([b671f8b](https://github.com/go-taas/go-taas/commit/b671f8b1ec18168ab04fb47f892fd7eb79f65a47))

# [1.78.0](https://github.com/go-taas/go-taas/compare/v1.77.0...v1.78.0) (2026-10-25)


### Bug Fixes

* **cluster:** store only selector-worthy accelerator labels ([1e6b3ef](https://github.com/go-taas/go-taas/commit/1e6b3efb612b0092a937ad9a51f1ba00d84e72db))
* **console:** localize the cluster filters and accelerator vendor banner ([2edf4b5](https://github.com/go-taas/go-taas/commit/2edf4b5fa6c60228da133e4420d521764a196549))
* **console:** localize the remaining enum filter options ([ff1cb1f](https://github.com/go-taas/go-taas/commit/ff1cb1fb6be963309a2e1c6e75d47fc04a4cfe74))
* **console:** localize the users state filter options ([1385ad9](https://github.com/go-taas/go-taas/commit/1385ad918d072111541246c1f24026f100ca0720))
* **console:** say 词元 in the usage subtitle ([c480a6b](https://github.com/go-taas/go-taas/commit/c480a6be0fcbc6ff8f9236d394c4944ca15a7b16))
* **console:** translate the usage card's in/out token sub-label ([eab9414](https://github.com/go-taas/go-taas/commit/eab9414a0abfe861d0b519254382e6fb44004553))
* **controller:** bound the image-warmup wait and report the pull reason ([2b38cd1](https://github.com/go-taas/go-taas/commit/2b38cd158865d9ed68e6bd80fb16b2496cafbdaf))
* **controller:** stop redelivering a warmup that already failed ([88a9cdd](https://github.com/go-taas/go-taas/commit/88a9cdd3c71e874eae567443bfec84e9843a5778))
* **helm:** default the server RBAC toggle when old values lack it ([964a075](https://github.com/go-taas/go-taas/commit/964a075fe14f3d6c1accaf3a490a953eef1ec430))
* **image:** fail warmup tasks a restart left active ([4257cb0](https://github.com/go-taas/go-taas/commit/4257cb0fa358287d8c6a5930d5ad9a83d8be8e81))


### Features

* **model:** show the grantor's display name in the grant list ([a00408e](https://github.com/go-taas/go-taas/commit/a00408e037ff3d1e0fd4cd477b2caea8b107728f))
* people-not-UUID rosters, local cluster discovery, honest i18n ([65f6ae5](https://github.com/go-taas/go-taas/commit/65f6ae5258707af4b5f8079c376a05202851a9b4))

# [1.77.0](https://github.com/go-taas/go-taas/compare/v1.76.0...v1.77.0) (2026-10-25)


### Bug Fixes

* **console:** add the missing model downloads nav label and 90-day range ([2a468b1](https://github.com/go-taas/go-taas/commit/2a468b17e5e9dc95ab0fbb3a1ffad35d2d5ee35d))
* **console:** cap hub id and model name columns in the download table ([b464da7](https://github.com/go-taas/go-taas/commit/b464da7097c6c67253927adee64a46943901809a))
* **console:** keep download state badges on one line ([49f7a44](https://github.com/go-taas/go-taas/commit/49f7a44eefc988dd5828a7b6ff05639c6306304f))
* **console:** scroll the download table inside its panel ([dafae52](https://github.com/go-taas/go-taas/commit/dafae52babf516bca138848a209b4a9cf49ca91b))
* **console:** wrap long download failure messages inside the table cell ([ba6aecf](https://github.com/go-taas/go-taas/commit/ba6aecfbc395a57baffb9dbd1a157c5beb9e09d9))


### Features

* **auth:** ship without Keycloak, seed the local admin by default ([84f1ab1](https://github.com/go-taas/go-taas/commit/84f1ab18af16b5183225a28995f09ea3a1d2539b))
* **console:** model downloads page, hub model id, full i18n pass ([6fc2790](https://github.com/go-taas/go-taas/commit/6fc2790178a766dd93af929855c5a85aaaadb93d))
* **model:** globally unique hub model id and background hub downloads ([0c5b44d](https://github.com/go-taas/go-taas/commit/0c5b44d538d3fa6b40db8be7da59bbbf8c5f0bcb))

# [1.76.0](https://github.com/go-taas/go-taas/compare/v1.75.0...v1.76.0) (2026-10-24)


### Bug Fixes

* **console:** darken inactive topbar tabs for reliable contrast ([82cb14b](https://github.com/go-taas/go-taas/commit/82cb14beb38990a21db8f6fecbe4190bb5ed0f7b))
* **console:** raise contrast for language switch, active nav, disabled buttons ([b5df22b](https://github.com/go-taas/go-taas/commit/b5df22be281c73bd7de4e7d35b3e8176a52b9617))


### Features

* **console:** professional tech palette, contrast fixes, vendor inference ([0c421f0](https://github.com/go-taas/go-taas/commit/0c421f0b7e63eb33b5a8c32d23f50966e5325513))

# [1.75.0](https://github.com/go-taas/go-taas/compare/v1.74.0...v1.75.0) (2026-10-24)


### Bug Fixes

* **auth:** return the session realm from local login ([015b0fc](https://github.com/go-taas/go-taas/commit/015b0fcee8e9e314310a5965b8ed4acbd67db1b4))
* **console:** correct provider status i18n keys and translate pagination ([cadc0f4](https://github.com/go-taas/go-taas/commit/cadc0f44d3a775ba6e1f91f43e3d908c95b59a16))


### Features

* **console:** unify login page, exempt admin from email code, translate UI ([44c9219](https://github.com/go-taas/go-taas/commit/44c9219a5cbb73858414e5397cfd83a0dfac67d8))

# [1.74.0](https://github.com/go-taas/go-taas/compare/v1.73.0...v1.74.0) (2026-10-24)


### Features

* **helm:** expose the seeded admin password as a chart value ([52b5f89](https://github.com/go-taas/go-taas/commit/52b5f8964293a3a787b18c304815e7eef07b70dc))

# [1.73.0](https://github.com/go-taas/go-taas/compare/v1.72.0...v1.73.0) (2026-10-24)


### Bug Fixes

* **auth:** exempt platform admins from login email verification ([0e6c1d2](https://github.com/go-taas/go-taas/commit/0e6c1d2145a90bccaed81b27c3548f9de6965518))


### Features

* **auth:** seed a local admin account and enforce a password policy ([2a8e107](https://github.com/go-taas/go-taas/commit/2a8e1078ac550cfde6b14171165d75f1508b35b1))

# [1.72.0](https://github.com/go-taas/go-taas/compare/v1.71.0...v1.72.0) (2026-10-24)


### Bug Fixes

* **console:** load the avatar on the admin surface and add admin profile ([59a6685](https://github.com/go-taas/go-taas/commit/59a66851d9f1e5f6c41f4bc65a33c78f3138ab9e))


### Features

* **console:** redesign profile page and show real avatar everywhere ([17a2993](https://github.com/go-taas/go-taas/commit/17a29933e38b86be73279098376c81e004960f9b))

# [1.71.0](https://github.com/go-taas/go-taas/compare/v1.70.0...v1.71.0) (2026-10-24)


### Features

* **tenancy:** add join requests, project quotas and scoped admin roles ([ba928b7](https://github.com/go-taas/go-taas/commit/ba928b7e78972e9ca6ec33205f77b8c6679d1a8a))

# [1.70.0](https://github.com/go-taas/go-taas/compare/v1.69.0...v1.70.0) (2026-10-24)


### Features

* **models,auth:** add model hub download UI and user deletion ([695b8ae](https://github.com/go-taas/go-taas/commit/695b8ae63c5d520f10c02c41abd1cc3832658a57))

# [1.69.0](https://github.com/go-taas/go-taas/compare/v1.68.1...v1.69.0) (2026-10-24)


### Features

* **auth:** add user profile and harden local account flows ([eb01bb6](https://github.com/go-taas/go-taas/commit/eb01bb651a0d1c9e24a7407ad324b08461b69b69))

## [1.68.1](https://github.com/go-taas/go-taas/compare/v1.68.0...v1.68.1) (2026-10-24)


### Bug Fixes

* **auth:** support implicit TLS SMTP on port 465 ([0f90593](https://github.com/go-taas/go-taas/commit/0f90593f2b57c698f411057079b2a830cbf7c49c))

# [1.68.0](https://github.com/go-taas/go-taas/compare/v1.67.0...v1.68.0) (2026-10-24)


### Features

* **auth:** add email verification codes for local accounts ([b6874ba](https://github.com/go-taas/go-taas/commit/b6874ba48a799a29985f5a8fdf83fc97c3f1bd6e))

# [1.67.0](https://github.com/go-taas/go-taas/compare/v1.66.2...v1.67.0) (2026-10-24)


### Features

* **auth:** add local account registration and login ([2ad316a](https://github.com/go-taas/go-taas/commit/2ad316add9237dfc18c7ad3f3a25badc42508673))

## [1.66.2](https://github.com/go-taas/go-taas/compare/v1.66.1...v1.66.2) (2026-10-24)


### Bug Fixes

* **auth:** route password-less SSO providers to redirect flow ([64b37d5](https://github.com/go-taas/go-taas/commit/64b37d5cdd8a4c6fc867e471fbe88890e95281b9))

## [1.66.1](https://github.com/go-taas/go-taas/compare/v1.66.0...v1.66.1) (2026-10-24)


### Bug Fixes

* **tenancy:** resolve username when adding a member ([2e54049](https://github.com/go-taas/go-taas/commit/2e540491da2d9de9f9805ae190758968ef16e688))

# [1.66.0](https://github.com/go-taas/go-taas/compare/v1.65.2...v1.66.0) (2026-10-24)


### Features

* **auth:** add user directory with lifecycle control ([58e930e](https://github.com/go-taas/go-taas/commit/58e930ec8b25727e227a56e3c4dce735331ecfec))
* **tenancy:** add project member management ([27aeb1a](https://github.com/go-taas/go-taas/commit/27aeb1a8f02685e985dc59a21a8a93bc3de77fb7))
* **tenancy:** add user-surface my organizations and projects ([78a1099](https://github.com/go-taas/go-taas/commit/78a10991f8b387a7d9a97399bf6ba55ebc91e072))

## [1.65.2](https://github.com/go-taas/go-taas/compare/v1.65.1...v1.65.2) (2026-10-24)


### Bug Fixes

* **console:** stop the blank page when the console redirects to login ([d1e441d](https://github.com/go-taas/go-taas/commit/d1e441da5769efb7f89eb5522b53bfbd9cd60e82))

## [1.65.1](https://github.com/go-taas/go-taas/compare/v1.65.0...v1.65.1) (2026-10-23)


### Bug Fixes

* **console:** polish account card and unify form controls ([9058cc4](https://github.com/go-taas/go-taas/commit/9058cc429c3fbaa22f4fbe88e1a300b0c2b6b68b))

# [1.65.0](https://github.com/go-taas/go-taas/compare/v1.64.0...v1.65.0) (2026-10-23)


### Features

* **console:** Aurora redesign with category navigation ([20b7139](https://github.com/go-taas/go-taas/commit/20b7139187134a53c5fa25b5292e943c74fe43de))

# [1.64.0](https://github.com/go-taas/go-taas/compare/v1.63.0...v1.64.0) (2026-10-23)


### Features

* **helm:** bundle Keycloak as a default dependency ([091c8a1](https://github.com/go-taas/go-taas/commit/091c8a1d336f9eeb0c54661a4815a420675de954))

# [1.63.0](https://github.com/go-taas/go-taas/compare/v1.62.2...v1.63.0) (2026-10-22)


### Features

* **labels:** move platform label prefixes to go-taas.com ([33bd5db](https://github.com/go-taas/go-taas/commit/33bd5dbad85602f8205ff63cc0d310a7573eeaa1))

## [1.62.2](https://github.com/go-taas/go-taas/compare/v1.62.1...v1.62.2) (2026-10-21)


### Bug Fixes

* **fault:** shorten accelerator device-plugin root cause to fit varchar(32) ([f82f5eb](https://github.com/go-taas/go-taas/commit/f82f5eb2663740a404cfeec98a4ee7549fef0f48))

## [1.62.1](https://github.com/go-taas/go-taas/compare/v1.62.0...v1.62.1) (2026-10-21)


### Bug Fixes

* **e2e:** publish seeds with bare subject routing key ([134e5ed](https://github.com/go-taas/go-taas/commit/134e5edf6ed9fde01221d98469d2030925a0b337))

# [1.62.0](https://github.com/go-taas/go-taas/compare/v1.61.0...v1.62.0) (2026-10-21)


### Features

* **mq:** add RabbitMQ client and replace NATS driver ([ae0d06f](https://github.com/go-taas/go-taas/commit/ae0d06fecc97b0bd067ba81ed4c6c306ed06e724))

# [1.61.0](https://github.com/go-taas/go-taas/compare/v1.60.0...v1.61.0) (2026-10-20)


### Bug Fixes

* **e2e:** satisfy revive deep-exit rule in fault seed helper ([38b46ea](https://github.com/go-taas/go-taas/commit/38b46ea1a5af5bce675604b321f8c0ca4fab2d87))
* **fault:** map malformed incident ids to not-found business code ([798daeb](https://github.com/go-taas/go-taas/commit/798daeb1bbd44a2eec9b1ae9b93f101c6e6950cc))
* **vgpu:** map malformed pool ids to not-found business code ([70d6345](https://github.com/go-taas/go-taas/commit/70d634575e4101fe8da37a5fd1208d2a9e001dbe))
* **web:** call admin-surface endpoints from admin pages ([d1c003e](https://github.com/go-taas/go-taas/commit/d1c003ee58eaae3e7f385893b2c046b7003b3096))
* **web:** read ApiError code for permission-denied states ([7f17f51](https://github.com/go-taas/go-taas/commit/7f17f511d135094971984d0a503cc182915a3c66))


### Features

* **infer:** add admin-surface service status rpc and uuid guard ([e373dff](https://github.com/go-taas/go-taas/commit/e373dff65390514932f94159511eaa2571b1545e)), closes [#53](https://github.com/go-taas/go-taas/issues/53)

# [1.60.0](https://github.com/go-taas/go-taas/compare/v1.59.0...v1.60.0) (2026-10-19)


### Features

* **fault:** fault diagnosis and automatic fault tolerance (row 53) ([bdb45f8](https://github.com/go-taas/go-taas/commit/bdb45f8b50bea8af3692feb57d02f05bcaff4f28))
* **web:** fault diagnosis admin pages and tenant status banner ([4752e8e](https://github.com/go-taas/go-taas/commit/4752e8ebd040515806104d20b63dd9766e46a807))

# [1.59.0](https://github.com/go-taas/go-taas/compare/v1.58.0...v1.59.0) (2026-10-19)


### Features

* **web:** vGPU slicing-pool admin pages and shared-GPU deploy ([76caf4e](https://github.com/go-taas/go-taas/commit/76caf4ee8ed39c6f46de3e05a633db9478ab7e7f))

# [1.58.0](https://github.com/go-taas/go-taas/compare/v1.57.2...v1.58.0) (2026-10-19)


### Features

* **vgpu:** HAMI-based vGPU slicing pool (row 52) ([f174584](https://github.com/go-taas/go-taas/commit/f17458492944c9b7deb64f2fa30e23eef9db181b))

## [1.57.2](https://github.com/go-taas/go-taas/compare/v1.57.1...v1.57.2) (2026-10-19)


### Bug Fixes

* **site:** fit diagrams to the page and let readers zoom them ([384a649](https://github.com/go-taas/go-taas/commit/384a64994d07ac2f15e73ae309d285ce3b4e48c3))

## [1.57.1](https://github.com/go-taas/go-taas/compare/v1.57.0...v1.57.1) (2026-10-16)


### Bug Fixes

* **site:** follow the UI language in the documentation browser ([7de14d4](https://github.com/go-taas/go-taas/commit/7de14d444d470c9691b6e92ec5c53d0e089b0813))

# [1.57.0](https://github.com/go-taas/go-taas/compare/v1.56.0...v1.57.0) (2026-10-16)


### Features

* **site:** add bilingual documentation browser and move docs into site ([4130c3a](https://github.com/go-taas/go-taas/commit/4130c3a7ec7e7c0263e35deadbad304daba10d91))

# [1.56.0](https://github.com/go-taas/go-taas/compare/v1.55.0...v1.56.0) (2026-10-16)


### Features

* **deploy:** add helm chart with values-driven dependency configuration ([9930c17](https://github.com/go-taas/go-taas/commit/9930c175ecbedc9f2804c968ee8176351ee977d9))

# [1.55.0](https://github.com/go-taas/go-taas/compare/v1.54.0...v1.55.0) (2026-10-15)


### Features

* **quota:** add per-organization service quota management ([b78b394](https://github.com/go-taas/go-taas/commit/b78b39493104d4bdf9b1bd5c73f199c83c1fdf50))

# [1.54.0](https://github.com/go-taas/go-taas/compare/v1.53.2...v1.54.0) (2026-10-15)


### Features

* **optimization:** add cost optimization recommendations engine and console pages ([ff21027](https://github.com/go-taas/go-taas/commit/ff21027f8b1ffdd82ad90fa40a8925f01a8ae040))

## [1.53.2](https://github.com/go-taas/go-taas/compare/v1.53.1...v1.53.2) (2026-10-14)


### Bug Fixes

* **controller:** complete pod metricsSource fields and harden AIBrix PodAutoscaler writes ([5cd648a](https://github.com/go-taas/go-taas/commit/5cd648a49cd25a9813b80a823b57ce1c0b97ed8f))

## [1.53.1](https://github.com/go-taas/go-taas/compare/v1.53.0...v1.53.1) (2026-10-14)


### Bug Fixes

* **controller:** set CRD-required metricsSources on AIBrix PodAutoscaler ([53dca36](https://github.com/go-taas/go-taas/commit/53dca369a33bab85a592225f017249a548470630))

# [1.53.0](https://github.com/go-taas/go-taas/compare/v1.52.1...v1.53.0) (2026-10-14)


### Features

* **routing:** intelligent model routing groups and decision log ([197b0b4](https://github.com/go-taas/go-taas/commit/197b0b47bf6da353ae7a586b4ed08bd8ebc3f048))

## [1.52.1](https://github.com/go-taas/go-taas/compare/v1.52.0...v1.52.1) (2026-10-13)


### Bug Fixes

* **controller:** set CRD-required scalingStrategy on AIBrix PodAutoscaler ([9a02e15](https://github.com/go-taas/go-taas/commit/9a02e1524efa6ee7da699cd33dc035cb83c13769)), closes [#47](https://github.com/go-taas/go-taas/issues/47)

# [1.52.0](https://github.com/go-taas/go-taas/compare/v1.51.0...v1.52.0) (2026-10-12)


### Features

* **provider:** add external model provider registry and passthrough ([5081721](https://github.com/go-taas/go-taas/commit/5081721227cf847a4a6d556119ce74bf36fc2724)), closes [#48](https://github.com/go-taas/go-taas/issues/48)

# [1.51.0](https://github.com/go-taas/go-taas/compare/v1.50.2...v1.51.0) (2026-10-12)


### Features

* **runtime:** add the distributed inference runtime (AIBrix integration) ([7092cc1](https://github.com/go-taas/go-taas/commit/7092cc1f86bf5150961b70477a611072a70c2404)), closes [#47](https://github.com/go-taas/go-taas/issues/47)

## [1.50.2](https://github.com/go-taas/go-taas/compare/v1.50.1...v1.50.2) (2026-10-10)


### Bug Fixes

* **infer:** keep recently-updated routing policies on top of the list ([345d715](https://github.com/go-taas/go-taas/commit/345d7153f4dda7ede39f412360c3603b241b954c))

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
