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

// Feature-02 (model-catalog-deployment) e2e suite, run against the compose
// stack gateway. Covers the API-level acceptance criteria of
// docs/design/model-catalog-deployment.md (AC1-AC5, AC8, AC9) plus
// cross-org isolation. AC6/AC7 (deploying->running transitions, fault
// injection) need a k8s cluster with the controller and are covered by the
// controller unit tests; in compose a created service legitimately stays
// `pending` with empty endpoints.

const api = require('../page-objects/api.js');

// Valid image ids come from configs/config.yaml image.registry.
const NVIDIA_IMAGE = 'img-vllm-nvidia-v063';
const ILUVATAR_IMAGE = 'img-vllm-iluvatar-v063';

module.exports = {
  '@tags': ['model', 'infer', 'feature-02'],

  before(browser) {
    // Nothing: each test navigates in beforeEach because afterEach ends
    // the session and a fresh page starts at about:blank, where a
    // cross-origin fetch to the gateway would fail (null origin).
  },

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    // Fresh organizations per run: the compose DB persists between runs, so
    // unique org ids keep every case isolated and repeatable.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.orgA = `org-e2e-${browser.globals.runId}`;
    browser.globals.orgB = `org-other-${browser.globals.runId}`;
  },

  afterEach(browser) {
    browser.end();
  },

  'AC1: register model succeeds; duplicate name+version conflicts; new version appends': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-model-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const created = api.assertOk(browser, res, 'register model v1');
      browser.assert.ok(Boolean(created.modelId), 'AC1: modelId returned');

      // Same name+version again -> 10102 MODEL_EXISTS.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10102, 'duplicate name+version conflicts');
      });

      // New version of the same name appends and keeps the model id stable.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v2', weightPath: `models/${name}/v2/`}
      }, (res3) => {
        const v2 = api.assertOk(browser, res3, 'register model v2');
        browser.assert.equal(v2.modelId, created.modelId, 'AC1: modelId stable across versions');
      });
    });
  },

  'AC2: getModel returns metadata with versions ordered latest-first': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-versions-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const created = api.assertOk(browser, res, 'register v1');

      // Ensure a strictly later created_at so the ordering assertion is
      // deterministic even at second granularity.
      browser.pause(1100);

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v2', weightPath: `models/${name}/v2/`}
      }, () => {
        api.request(browser, {
          path: `/api/v1/admin/models/${created.modelId}`,
          org
        }, (res3) => {
          const body = api.assertOk(browser, res3, 'get model');
          const model = body.model || {};
          browser.assert.equal(model.name, name, 'AC2: model name echoed');
          browser.assert.equal(model.latestVersion, 'v2', 'AC2: latestVersion is the newest');
          browser.assert.deepEqual(
            body.versions,
            ['v2', 'v1'],
            'AC2: versions ordered latest-first'
          );
        });
      });
    });
  },

  'AC1: listModels paginates and shows the registered model with its latest version': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-catalog-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const created = api.assertOk(browser, res, 'register for catalog');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v9', weightPath: `models/${name}/v9/`}
      }, () => {
        api.request(browser, {
          path: '/api/v1/admin/models?page.offset=0&page.limit=20',
          org
        }, (res3) => {
          const body = api.assertOk(browser, res3, 'list models');
          const row = (body.models || []).find((m) => m.modelId === created.modelId);
          browser.assert.ok(row, 'catalog contains the registered model');
          browser.assert.equal(row && row.latestVersion, 'v9', 'catalog shows latest version');
          browser.assert.ok(
            body.pageMeta && Number(body.pageMeta.total) >= 1,
            'pageMeta.total populated'
          );
        });
      });
    });
  },

  'FR1.3: invalid weight paths are rejected syntactically': function (browser) {
    const org = browser.globals.orgA;

    const cases = [
      {weightPath: '', label: 'empty path'},
      {weightPath: '/abs/path', label: 'absolute path'},
      {weightPath: 'models/../etc', label: 'dot-dot segment'}
    ];

    cases.forEach((c) => {
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name: `e2e-badpath-${browser.globals.runId}`, version: 'v1', weightPath: c.weightPath}
      }, (res) => {
        // 10104 MODEL_PATH_INVALID.
        api.assertBusinessError(browser, res, 10104, `weight path: ${c.label}`);
      });
    });
  },

  'AC4: create inference service returns immediately with state=pending': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-deploy-${browser.globals.runId}`;
    const started = Date.now();

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model for deploy');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name,
          modelId: model.modelId,
          modelVersion: 'v1',
          imageId: NVIDIA_IMAGE,
          replicas: '1',
          accelerator: 'nvidia'
        }
      }, (res2) => {
        const created = api.assertOk(browser, res2, 'create inference service');
        browser.assert.ok(Boolean(created.serviceId), 'AC4: serviceId returned');
        const elapsed = Date.now() - started;
        browser.assert.ok(
          elapsed < 20000,
          `AC4: create returns without blocking on pods (${elapsed} ms)`
        );

        // In compose there is no controller, so the service stays pending —
        // that is the expected steady state, and endpoints must stay empty.
        api.request(browser, {
          path: `/api/v1/admin/inference-services/${created.serviceId}`,
          org
        }, (res3) => {
          const body = api.assertOk(browser, res3, 'get service after create');
          browser.assert.equal(body.service.state, 'pending', 'state is pending (no controller in compose)');
          browser.assert.deepEqual(body.endpoints, [], 'endpoints empty while not running');
          browser.assert.equal(body.service.modelId, model.modelId, 'spec echoes modelId');
          browser.assert.equal(body.service.imageId, NVIDIA_IMAGE, 'spec echoes imageId');
        });
      });
    });
  },

  'AC5: invalid create requests are rejected with validation codes': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-invalid-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model for invalid creates');

      const base = {
        modelId: model.modelId,
        modelVersion: 'v1',
        imageId: NVIDIA_IMAGE,
        replicas: '1',
        accelerator: 'nvidia'
      };

      // Unknown model id -> 10101 MODEL_NOT_FOUND.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: Object.assign({}, base, {name: `${name}-unknown-model`, modelId: '00000000-0000-0000-0000-000000000000'})
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10101, 'unknown modelId');
      });

      // replicas 0 -> 10305 INFER_REPLICAS_INVALID.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: Object.assign({}, base, {name: `${name}-zero-replicas`, replicas: '0'})
      }, (res3) => {
        api.assertBusinessError(browser, res3, 10305, 'replicas 0');
      });

      // replicas 101 -> 10305 (upper bound).
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: Object.assign({}, base, {name: `${name}-too-many-replicas`, replicas: '101'})
      }, (res4) => {
        api.assertBusinessError(browser, res4, 10305, 'replicas 101');
      });

      // Unsupported accelerator -> 10306 INFER_ENGINE_UNSUPPORTED.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: Object.assign({}, base, {name: `${name}-tpu`, accelerator: 'tpu'})
      }, (res5) => {
        api.assertBusinessError(browser, res5, 10306, 'accelerator tpu');
      });

      // Image accelerator differs from request accelerator -> 10204 IMAGE_INCOMPATIBLE.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: Object.assign({}, base, {name: `${name}-mismatch`, imageId: ILUVATAR_IMAGE})
      }, (res6) => {
        api.assertBusinessError(browser, res6, 10204, 'image/accelerator mismatch');
      });

      // Unknown image id -> 10201 IMAGE_NOT_FOUND.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: Object.assign({}, base, {name: `${name}-unknown-image`, imageId: 'img-does-not-exist'})
      }, (res7) => {
        api.assertBusinessError(browser, res7, 10201, 'unknown imageId');
      });
    });
  },

  'AC8: scale changes desired replicas without touching other spec fields': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-scale-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model for scale');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name,
          modelId: model.modelId,
          modelVersion: 'v1',
          imageId: NVIDIA_IMAGE,
          replicas: '1',
          accelerator: 'nvidia'
        }
      }, (res2) => {
        const created = api.assertOk(browser, res2, 'create service for scale');

        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/inference-services/${created.serviceId}:scale`,
          org,
          body: {replicas: '5'}
        }, (res3) => {
          api.assertOk(browser, res3, 'scale to 5');

          api.request(browser, {
            path: `/api/v1/admin/inference-services/${created.serviceId}`,
            org
          }, (res4) => {
            const body = api.assertOk(browser, res4, 'get after scale');
            browser.assert.equal(body.service.replicas, 5, 'AC8: desired replicas updated to 5');
            browser.assert.equal(body.service.imageId, NVIDIA_IMAGE, 'AC8: image unchanged');
            browser.assert.equal(body.service.modelVersion, 'v1', 'AC8: model version unchanged');
          });
        });

        // Scaling to 0 is rejected with the replicas-invalid code.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/inference-services/${created.serviceId}:scale`,
          org,
          body: {replicas: '0'}
        }, (res5) => {
          api.assertBusinessError(browser, res5, 10305, 'scale to 0 rejected');
        });
      });
    });
  },

  'AC3+AC9: delete model is guarded while referenced, then unblocked after service delete; service delete is idempotent': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-guard-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model for guard');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name,
          modelId: model.modelId,
          modelVersion: 'v1',
          imageId: NVIDIA_IMAGE,
          replicas: '1',
          accelerator: 'nvidia'
        }
      }, (res2) => {
        const created = api.assertOk(browser, res2, 'create referencing service');

        // Delete model while referenced -> 10101 naming the blocking service.
        api.request(browser, {
          method: 'DELETE',
          path: `/api/v1/admin/models/${model.modelId}`,
          org
        }, (res3) => {
          const err = api.assertBusinessError(browser, res3, 10101, 'delete guarded model');
          browser.assert.ok(
            err.message.indexOf(name) !== -1,
            `AC3: error names the blocking service (got "${err.message}")`
          );
        });

        // Delete the service, then the model delete must succeed.
        api.request(browser, {
          method: 'DELETE',
          path: `/api/v1/admin/inference-services/${created.serviceId}`,
          org
        }, (res4) => {
          api.assertOk(browser, res4, 'delete service');

          // Idempotent second delete (AC9).
          api.request(browser, {
            method: 'DELETE',
            path: `/api/v1/admin/inference-services/${created.serviceId}`,
            org
          }, (res5) => {
            api.assertOk(browser, res5, 'second service delete is idempotent');
          });

          // Terminated services disappear from the default list view (AC9).
          api.request(browser, {
            path: '/api/v1/admin/inference-services?page.offset=0&page.limit=20',
            org
          }, (res6) => {
            const body = api.assertOk(browser, res6, 'list after delete');
            const stillListed = (body.services || []).some((s) => s.serviceId === created.serviceId);
            browser.assert.equal(stillListed, false, 'AC9: terminated service hidden from default list');
          });

          // Model delete now unblocked (FR6.3).
          api.request(browser, {
            method: 'DELETE',
            path: `/api/v1/admin/models/${model.modelId}`,
            org
          }, (res7) => {
            api.assertOk(browser, res7, 'model delete after service termination');
          });
        });
      });
    });
  },

  'isolation: inference services of another organization are not visible (10301)': function (browser) {
    const orgA = browser.globals.orgA;
    const orgB = browser.globals.orgB;
    const name = `e2e-isolation-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org: orgA,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model in org A');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org: orgA,
        body: {
          name,
          modelId: model.modelId,
          modelVersion: 'v1',
          imageId: NVIDIA_IMAGE,
          replicas: '1',
          accelerator: 'nvidia'
        }
      }, (res2) => {
        const created = api.assertOk(browser, res2, 'create service in org A');

        // Cross-org get -> 10301 INFER_SERVICE_NOT_FOUND.
        api.request(browser, {
          path: `/api/v1/admin/inference-services/${created.serviceId}`,
          org: orgB
        }, (res3) => {
          api.assertBusinessError(browser, res3, 10301, 'cross-org get');
        });

        // Cross-org list never leaks the service.
        api.request(browser, {
          path: '/api/v1/admin/inference-services?page.offset=0&page.limit=20',
          org: orgB
        }, (res4) => {
          const body = api.assertOk(browser, res4, 'cross-org list');
          const leaked = (body.services || []).some((s) => s.serviceId === created.serviceId);
          browser.assert.equal(leaked, false, 'org B list does not leak org A services');
        });

        // Cross-org scale -> 10301.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/inference-services/${created.serviceId}:scale`,
          org: orgB,
          body: {replicas: '3'}
        }, (res5) => {
          api.assertBusinessError(browser, res5, 10301, 'cross-org scale');
        });

        // Cross-org delete -> 10301.
        api.request(browser, {
          method: 'DELETE',
          path: `/api/v1/admin/inference-services/${created.serviceId}`,
          org: orgB
        }, (res6) => {
          api.assertBusinessError(browser, res6, 10301, 'cross-org delete');
        });

        // The service is untouched in its owning org.
        api.request(browser, {
          path: `/api/v1/admin/inference-services/${created.serviceId}`,
          org: orgA
        }, (res7) => {
          const body = api.assertOk(browser, res7, 'owning-org get after cross-org attempts');
          browser.assert.equal(body.service.replicas, 1, 'replicas untouched by foreign scale attempt');
        });
      });
    });
  },

  'list inference services shows the created service for the owning org': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-listsvc-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'register model for list');

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
        const created = api.assertOk(browser, res2, 'create service for list');

        api.request(browser, {
          path: '/api/v1/admin/inference-services?page.offset=0&page.limit=20',
          org
        }, (res3) => {
          const body = api.assertOk(browser, res3, 'list services');
          const row = (body.services || []).find((s) => s.serviceId === created.serviceId);
          browser.assert.ok(row, 'list contains the created service');
          browser.assert.equal(row && row.name, name, 'list row echoes name');
          browser.assert.equal(row && row.state, 'pending', 'list row state pending in compose');
          browser.assert.ok(
            body.pageMeta && Number(body.pageMeta.total) >= 1,
            'pageMeta.total populated'
          );
        });
      });
    });
  }
};
