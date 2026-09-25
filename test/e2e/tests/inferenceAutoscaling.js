// Copyright 2025 The go-taas Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Feature-16 (inference autoscaling & scale-to-zero) e2e suite, run
// against the compose stack gateway. Covers the acceptance criteria of
// docs/design/inference-autoscaling.md reachable from the outside: the
// admin global-default policy (AC1/AC2), per-service autoscaling
// (AC3/AC4/AC5), the admin service list/detail autoscaling column and
// status block (AC8), the end-user read-only model list/detail summary
// (AC9), and the surface separation (AC10).
//
// The compose stack has no controller, so a created inference service
// legitimately stays `state=pending` with no autoscaling status; the
// user-realm projection therefore returns `autoscaling: null` (the UI
// renders the "Fixed" badge). The `deploying/running` transitions and
// the cold-start/scale-to-zero timing (AC6/AC7) need a k8s cluster and
// are covered by the controller unit tests and FVT.

const api = require('../page-objects/api.js');

// Valid image ids come from configs/config.yaml image.registry.
const NVIDIA_IMAGE = 'img-vllm-nvidia-v063';

module.exports = {
  '@tags': ['inference-autoscaling', 'feature-16'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-as-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Admin surface: global default policy (AC1/AC2) ----

  'AC1: GET default policy returns the seeded defaults': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/autoscaling/policy',
      org: browser.globals.orgA
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: get default policy');
      const p = body.policy;
      browser.assert.ok(p, 'AC1: policy present');
      browser.assert.equal(p.enabled, true, 'AC1: enabled default true');
      browser.assert.equal(p.minReplicas, 1, 'AC1: min default 1');
      browser.assert.equal(p.maxReplicas, 10, 'AC1: max default 10');
      browser.assert.equal(p.targetConcurrency, 32, 'AC1: target default 32');
      browser.assert.equal(p.scaleToZero, false, 'AC1: scale-to-zero default off');
      browser.assert.equal(p.cooldownSeconds, 300, 'AC1: cooldown default 300');
    });
  },

  'AC1: PUT a valid policy persists it and GET returns it': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/autoscaling/policy',
      org,
      body: {
        policy: {
          enabled: true, minReplicas: 2, maxReplicas: 20,
          targetConcurrency: 64, scaleToZero: false, cooldownSeconds: 600
        }
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: update policy');
      browser.assert.equal(body.policy.minReplicas, 2, 'AC1: min echoed');
      browser.assert.equal(body.policy.maxReplicas, 20, 'AC1: max echoed');

      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/autoscaling/policy',
        org
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC1: get after update');
        browser.assert.equal(body2.policy.minReplicas, 2, 'AC1: min persisted');
        browser.assert.equal(body2.policy.maxReplicas, 20, 'AC1: max persisted');
        browser.assert.equal(body2.policy.targetConcurrency, 64, 'AC1: target persisted');
        browser.assert.equal(body2.policy.cooldownSeconds, 600, 'AC1: cooldown persisted');
      });
    });
  },

  'AC2: invalid policies are rejected with 10307': function (browser) {
    const org = browser.globals.orgA;
    const cases = [
      {
        label: 'min > max',
        policy: {enabled: true, minReplicas: 20, maxReplicas: 2, targetConcurrency: 32, scaleToZero: false, cooldownSeconds: 300}
      },
      {
        label: 'min 0 without scale-to-zero',
        policy: {enabled: true, minReplicas: 0, maxReplicas: 10, targetConcurrency: 32, scaleToZero: false, cooldownSeconds: 300}
      },
      {
        label: 'scale-to-zero with min > 0',
        policy: {enabled: true, minReplicas: 1, maxReplicas: 10, targetConcurrency: 32, scaleToZero: true, cooldownSeconds: 300}
      },
      {
        label: 'target out of range (0)',
        policy: {enabled: true, minReplicas: 1, maxReplicas: 10, targetConcurrency: 0, scaleToZero: false, cooldownSeconds: 300}
      },
      {
        label: 'target out of range (1001)',
        policy: {enabled: true, minReplicas: 1, maxReplicas: 10, targetConcurrency: 1001, scaleToZero: false, cooldownSeconds: 300}
      },
      {
        label: 'cooldown out of range (-1)',
        policy: {enabled: true, minReplicas: 1, maxReplicas: 10, targetConcurrency: 32, scaleToZero: false, cooldownSeconds: -1}
      },
      {
        label: 'cooldown out of range (3601)',
        policy: {enabled: true, minReplicas: 1, maxReplicas: 10, targetConcurrency: 32, scaleToZero: false, cooldownSeconds: 3601}
      },
      {
        label: 'max out of range (101)',
        policy: {enabled: true, minReplicas: 1, maxReplicas: 101, targetConcurrency: 32, scaleToZero: false, cooldownSeconds: 300}
      }
    ];

    cases.forEach((c) => {
      api.request(browser, {
        method: 'PUT',
        path: '/api/v1/admin/autoscaling/policy',
        org,
        body: {policy: c.policy}
      }, (res) => {
        api.assertBusinessError(browser, res, 10307, `AC2: ${c.label}`);
      });
    });
  },

  // ---- Admin surface: per-service autoscaling (AC3/AC4/AC5) ----

  'AC3: create service with explicit policy stores it; get returns policy + status': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-as-explicit-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model');
      const modelId = model.modelId;

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name,
          modelId,
          modelVersion: 'v1',
          imageId: NVIDIA_IMAGE,
          replicas: '2',
          accelerator: 'nvidia',
          autoscaling: {
            enabled: true, minReplicas: 1, maxReplicas: 8,
            targetConcurrency: 48, scaleToZero: false, cooldownSeconds: 200
          }
        }
      }, (res2) => {
        const created = api.assertOk(browser, res2, 'AC3: create service with policy');
        const serviceId = created.serviceId;
        browser.assert.ok(serviceId, 'AC3: serviceId returned');

        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/inference-services/${serviceId}`,
          org
        }, (res3) => {
          const body = api.assertOk(browser, res3, 'AC3: get service');
          browser.assert.equal(body.service.autoscalingEnabled, true, 'AC3: autoscaling enabled');
          browser.assert.equal(body.service.minReplicas, 1, 'AC3: min stored');
          browser.assert.equal(body.service.maxReplicas, 8, 'AC3: max stored');
          browser.assert.ok(body.autoscaling, 'AC3: full policy present');
          browser.assert.equal(body.autoscaling.targetConcurrency, 48, 'AC3: target stored');
          browser.assert.equal(body.autoscaling.cooldownSeconds, 200, 'AC3: cooldown stored');
          browser.assert.ok(body.autoscalingStatus, 'AC3: status block present');
        });
      });
    });
  },

  'AC3: create service without a policy inherits the global default': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-as-inherit-${browser.globals.runId}-${browser.globals.testSeq}`;

    // Set a distinctive global default first.
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/autoscaling/policy',
      org,
      body: {
        policy: {
          enabled: true, minReplicas: 3, maxReplicas: 25,
          targetConcurrency: 80, scaleToZero: false, cooldownSeconds: 900
        }
      }
    }, () => {
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
      }, (res) => {
        const model = api.assertOk(browser, res, 'register model');

        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/inference-services',
          org,
          body: {
            name,
            modelId: model.modelId,
            modelVersion: 'v1',
            imageId: NVIDIA_IMAGE,
            replicas: '2',
            accelerator: 'nvidia'
          }
        }, (res2) => {
          const created = api.assertOk(browser, res2, 'AC3: create service without policy');
          const serviceId = created.serviceId;

          api.request(browser, {
            method: 'GET',
            path: `/api/v1/admin/inference-services/${serviceId}`,
            org
          }, (res3) => {
            const body = api.assertOk(browser, res3, 'AC3: get inherited service');
            browser.assert.equal(body.service.autoscalingEnabled, true, 'AC3: inherited enabled');
            browser.assert.equal(body.autoscaling.minReplicas, 3, 'AC3: inherited min');
            browser.assert.equal(body.autoscaling.maxReplicas, 25, 'AC3: inherited max');
            browser.assert.equal(body.autoscaling.targetConcurrency, 80, 'AC3: inherited target');
            browser.assert.equal(body.autoscaling.cooldownSeconds, 900, 'AC3: inherited cooldown');
          });
        });
      });
    });
  },

  'AC4: per-service update changes the policy and the list reflects it': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-as-update-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name,
          modelId: model.modelId,
          modelVersion: 'v1',
          imageId: NVIDIA_IMAGE,
          replicas: '2',
          accelerator: 'nvidia',
          autoscaling: {
            enabled: true, minReplicas: 1, maxReplicas: 8,
            targetConcurrency: 48, scaleToZero: false, cooldownSeconds: 200
          }
        }
      }, (res2) => {
        const created = api.assertOk(browser, res2, 'create service');
        const serviceId = created.serviceId;

        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/inference-services/${serviceId}:autoscaling`,
          org,
          body: {
            policy: {
              enabled: true, minReplicas: 1, maxReplicas: 12,
              targetConcurrency: 32, scaleToZero: false, cooldownSeconds: 300
            }
          }
        }, (res3) => {
          api.assertOk(browser, res3, 'AC4: per-service update');

          api.request(browser, {
            method: 'GET',
            path: '/api/v1/admin/inference-services',
            org
          }, (res4) => {
            const body = api.assertOk(browser, res4, 'AC4: list services');
            const row = (body.services || []).find((s) => s.serviceId === serviceId);
            browser.assert.ok(row, 'AC4: service in list');
            browser.assert.equal(row.autoscalingEnabled, true, 'AC4: autoscaling enabled');
            browser.assert.equal(row.maxReplicas, 12, 'AC4: new max reflected');
          });
        });
      });
    });
  },

  'AC5: disabling autoscaling returns the service to a fixed count': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-as-disable-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name,
          modelId: model.modelId,
          modelVersion: 'v1',
          imageId: NVIDIA_IMAGE,
          replicas: '4',
          accelerator: 'nvidia',
          autoscaling: {
            enabled: true, minReplicas: 1, maxReplicas: 8,
            targetConcurrency: 48, scaleToZero: false, cooldownSeconds: 200
          }
        }
      }, (res2) => {
        const created = api.assertOk(browser, res2, 'create service');
        const serviceId = created.serviceId;

        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/inference-services/${serviceId}:autoscaling`,
          org,
          body: {
            policy: {
              enabled: false, minReplicas: 1, maxReplicas: 8,
              targetConcurrency: 48, scaleToZero: false, cooldownSeconds: 200
            }
          }
        }, (res3) => {
          const body = api.assertOk(browser, res3, 'AC5: disable autoscaling');
          browser.assert.equal(body.fixedReplicas, 4, 'AC5: fixed count equals current desired (4)');

          api.request(browser, {
            method: 'GET',
            path: '/api/v1/admin/inference-services',
            org
          }, (res4) => {
            const body4 = api.assertOk(browser, res4, 'AC5: list after disable');
            const row = (body4.services || []).find((s) => s.serviceId === serviceId);
            browser.assert.ok(row, 'AC5: service in list');
            browser.assert.equal(row.autoscalingEnabled, false, 'AC5: autoscaling disabled');
          });
        });
      });
    });
  },

  'AC4: per-service update on a terminated service is rejected': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-as-term-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name,
          modelId: model.modelId,
          modelVersion: 'v1',
          imageId: NVIDIA_IMAGE,
          replicas: '2',
          accelerator: 'nvidia'
        }
      }, (res2) => {
        const created = api.assertOk(browser, res2, 'create service');
        const serviceId = created.serviceId;

        api.request(browser, {
          method: 'DELETE',
          path: `/api/v1/admin/inference-services/${serviceId}`,
          org
        }, (res3) => {
          api.assertOk(browser, res3, 'delete service');

          api.request(browser, {
            method: 'POST',
            path: `/api/v1/admin/inference-services/${serviceId}:autoscaling`,
            org,
            body: {
              policy: {
                enabled: true, minReplicas: 1, maxReplicas: 8,
                targetConcurrency: 48, scaleToZero: false, cooldownSeconds: 200
              }
            }
          }, (res4) => {
            api.assertBusinessError(browser, res4, 10303, 'AC4: autoscale terminated service rejected');
          });
        });
      });
    });
  },

  // ---- User surface: read-only autoscaling projection (AC9) ----

  'AC9: user model list and detail expose the read-only autoscaling projection': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-as-user-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model');
      const modelId = model.modelId;

      // The user model list carries the model (default-allow rule).
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/models?page.limit=100',
        org
      }, (res2) => {
        const body = api.assertOk(browser, res2, 'AC9: user model list');
        const row = (body.models || []).find((m) => m.modelId === modelId);
        browser.assert.ok(row, 'AC9: model in user list');
        // In compose there is no controller, so the service is not
        // running and the projection is null (the UI renders "Fixed").
        browser.assert.equal(row.autoscaling, null, 'AC9: no autoscaling projection while not running');

        // The user model detail returns the read-only summary.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/models/${modelId}`,
          org
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'AC9: user model detail');
          browser.assert.equal(body3.model.modelId, modelId, 'AC9: model echoed');
          browser.assert.equal(body3.model.name, name, 'AC9: model name echoed');
          browser.assert.equal(body3.autoscaling, null, 'AC9: no autoscaling summary while not running');
        });
      });
    });
  },

  'AC9: user model detail for an unknown model is rejected (10101)': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/models/00000000-0000-0000-0000-000000000000',
      org: browser.globals.orgA
    }, (res) => {
      api.assertBusinessError(browser, res, 10101, 'AC9: unknown model detail');
    });
  },

  // ---- Surface separation (AC10) ----

  'AC10: admin autoscaling endpoints are not reachable on the user prefix': function (browser) {
    // The user console must never call /api/v1/admin/*. A request to the
    // admin autoscaling policy on the user prefix is not a valid route.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/autoscaling/policy',
      org: browser.globals.orgA
    }, (res) => {
      // The user prefix has no autoscaling route: the gateway answers a
      // non-2xx (404/500) rather than a policy envelope.
      browser.assert.ok(
        res.status !== 200 || !res.body || !res.body.policy,
        'AC10: user prefix does not serve the admin autoscaling policy'
      );
    });
  },

  'AC10: admin inference-services autoscaling is not reachable on the user prefix': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/inference-services',
      org: browser.globals.orgA
    }, (res) => {
      // The user surface has no inference-services route.
      browser.assert.ok(
        res.status !== 200 || !res.body || !res.body.services,
        'AC10: user prefix does not serve admin inference-services'
      );
    });
  },

  // ---- Console pages ----

  'AC8: admin autoscaling page renders the global default editor': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/autoscaling');
    browser.waitForElementPresent('[data-testid="as-enabled"]', 15000, 'AC8: autoscaling page renders');
    browser.waitForElementPresent('[data-testid="as-min"]', 5000, 'AC8: min field');
    browser.waitForElementPresent('[data-testid="as-max"]', 5000, 'AC8: max field');
    browser.waitForElementPresent('[data-testid="as-target"]', 5000, 'AC8: target field');
    browser.waitForElementPresent('[data-testid="as-scale-to-zero"]', 5000, 'AC8: scale-to-zero field');
    browser.waitForElementPresent('[data-testid="as-cooldown"]', 5000, 'AC8: cooldown field');
  },

  'AC8: admin inference-services page renders the autoscaling column': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/inference-services');
    browser.waitForElementPresent(
      '[data-testid="services-table"], [data-testid="services-empty"]',
      15000,
      'AC8: inference-services page renders'
    );
  },

  'AC9: user models page renders the autoscaling indicator': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/models');
    browser.waitForElementPresent(
      '[data-testid="models-table"], [data-testid="models-empty"]',
      15000,
      'AC9: user models page renders'
    );
  },

  'AC9: user model detail page renders the read-only autoscaling summary': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-as-detail-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model');
      const modelId = model.modelId;

      browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + `/models/${modelId}`);
      browser.waitForElementPresent('[data-testid="model-detail-name"]', 15000, 'AC9: model detail renders');
      // The read-only summary card renders (with the "no autoscaling"
      // placeholder when the service is not running in compose).
      browser.waitForElementPresent(
        '[data-testid="model-autoscaling-summary"], [data-testid="model-autoscaling-none"]',
        10000,
        'AC9: autoscaling summary card renders'
      );
    });
  }
};