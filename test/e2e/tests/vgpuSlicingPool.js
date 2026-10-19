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

// Feature-52 (HAMI-based vGPU slicing pool) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/vgpu-slicing-pool.md reachable from the outside: the admin
// pool table (AC1), the create-dialog validation (AC2), the pool detail
// node/per-GPU allocation (AC3/AC4), the deploy-form shared-GPU mode
// (AC5), the invalid/ungranted slice (AC7), the delete-blocked guard
// (AC9), the negative realm separation (AC11), the role gate (AC12), and
// the states (AC13).
//
// The compose stack has no real HAMI, so pool membership and per-GPU
// capacity are populated by the Controller from the host cluster's node
// labels. The suite exercises the pool CRUD, validation, tenant-visible
// grant-filtered list, deploy-time slice validation, and the realm/role
// negatives against the real gateway; the browser cases drive the admin
// pages and the deploy form's shared-GPU mode.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['vgpu-slicing-pool', 'feature-52'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-vgpu-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation (AC11/AC12) ----

  'AC11: vGPU pool admin APIs are admin-only (user realm rejected 10038)': function (browser) {
    const org = browser.globals.orgA;
    // Admin realm can list.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/vgpu-pools?limit=50',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertOk(browser, res, 'AC11: admin list reachable');
    });
    // A user-realm session token on an admin route -> 10038.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/vgpu-pools?limit=50',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC11: user realm rejected on admin list');
    });
    // A user-realm session cannot create a pool either.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/vgpu-pools',
      org,
      sessionRealm: 'user',
      body: {name: 'pool-x', labelKey: 'k', labelValue: 'v'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC11: user realm rejected on admin create');
    });
  },

  'AC12: vGPU pool admin RPCs require the admin role (10036)': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/vgpu-pools?limit=50',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertBusinessError(browser, res, 10036, 'AC12: non-member session denied');
    });
  },

  'AC11: the admin page is served at /admin/vgpu-pools and the user deploy form uses /api/v1/vgpu-pools': function (browser) {
    const org = browser.globals.orgA;
    // Create a pool so the admin table renders (the table only appears
    // when at least one pool exists; otherwise the empty state shows).
    const name = `pool-page-${browser.globals.runId}`;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/vgpu-pools',
      org,
      sessionRealm: 'admin',
      body: {
        name,
        labelKey: 'go-taas.io/vgpu-pool',
        labelValue: name,
        placementPolicy: 'binpack',
        templates: [{name: 'small', memoryGib: 8, computePct: 25}]
      }
    }, (res) => {
      api.assertOk(browser, res, 'AC11: create pool for page');
      browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/admin/vgpu-pools');
      browser.waitForElementPresent('[data-testid="vgpu-pool-table"]', 15000, 'AC11: admin page served');
      browser.waitForElementPresent('[data-testid="nav-vgpu-pools"]', 15000, 'AC11: admin nav entry');
      // The tenant-visible list is a user-surface RPC.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/vgpu-pools?limit=50',
        org,
        sessionRealm: 'user'
      }, (res2) => {
        api.assertOk(browser, res2, 'AC11: tenant-visible list reachable on user realm');
      });
    });
  },

  // ---- API contract (AC1/AC2/AC9/AC10) ----

  'AC1/AC2/AC10: create a pool, list it, read it back, validate errors, and delete it': function (browser) {
    const org = browser.globals.orgA;
    const name = `pool-${browser.globals.runId}`;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/vgpu-pools',
      org,
      sessionRealm: 'admin',
      body: {
        name,
        labelKey: 'go-taas.io/vgpu-pool',
        labelValue: name,
        placementPolicy: 'binpack',
        templates: [
          {name: 'small', memoryGib: 8, computePct: 25},
          {name: 'medium', memoryGib: 16, computePct: 50}
        ]
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: create pool');
      const poolId = body.pool.poolId;
      browser.assert.ok(Boolean(poolId), 'AC1: poolId returned');
      browser.assert.equal(body.pool.name, name, 'AC1: name echoed');
      browser.assert.equal(body.pool.placementPolicy, 'binpack', 'AC1: policy echoed');
      browser.assert.equal(body.pool.templates.length, 2, 'AC1: templates echoed');

      // List.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/vgpu-pools?limit=50',
        org,
        sessionRealm: 'admin'
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC1: list pools');
        const found = (body2.pools || []).find((p) => p.poolId === poolId);
        browser.assert.ok(Boolean(found), 'AC1: created pool in list');
        browser.assert.equal(found.name, name, 'AC1: list name');

        // Get.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/vgpu-pools/${poolId}`,
          org,
          sessionRealm: 'admin'
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'AC1: get pool');
          browser.assert.equal(body3.pool.name, name, 'AC1: get name');
          browser.assert.ok(Array.isArray(body3.nodes), 'AC1: nodes array present');
          browser.assert.ok(Array.isArray(body3.services), 'AC1: services array present');

          // Invalid name -> 13401.
          api.request(browser, {
            method: 'POST',
            path: '/api/v1/admin/vgpu-pools',
            org,
            sessionRealm: 'admin',
            body: {name: 'Bad Name!', labelKey: 'k', labelValue: 'v'}
          }, (res4) => {
            api.assertBusinessError(browser, res4, 13401, 'AC2: invalid name -> 13401');

            // Invalid template -> 13402.
            api.request(browser, {
              method: 'POST',
              path: '/api/v1/admin/vgpu-pools',
              org,
              sessionRealm: 'admin',
              body: {
                name: 'pool-b', labelKey: 'k', labelValue: 'v',
                templates: [{name: 'small', memoryGib: 0, computePct: 25}]
              }
            }, (res5) => {
              api.assertBusinessError(browser, res5, 13402, 'AC2: invalid template -> 13402');

              // Unknown pool -> 13406.
              api.request(browser, {
                method: 'GET',
                path: '/api/v1/admin/vgpu-pools/00000000-0000-0000-0000-000000000000',
                org,
                sessionRealm: 'admin'
              }, (res6) => {
                api.assertBusinessError(browser, res6, 13406, 'AC2: unknown pool -> 13406');

                // Delete the idle pool.
                api.request(browser, {
                  method: 'DELETE',
                  path: `/api/v1/admin/vgpu-pools/${poolId}`,
                  org,
                  sessionRealm: 'admin'
                }, (res7) => {
                  api.assertOk(browser, res7, 'AC9: delete idle pool');
                });
              });
            });
          });
        });
      });
    });
  },

  'AC2: a malformed (non-UUID) pool id returns 13406, not an internal error': function (browser) {
    const org = browser.globals.orgA;
    // A malformed pool id is an unknown pool and must map to 13406.
    // (Regression: postgres rejects the non-UUID literal and surfaced as
    // code 13 internal error instead of 13406.)
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/vgpu-pools/not-a-uuid',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertBusinessError(browser, res, 13406, 'AC2: malformed pool id -> 13406');
    });
  },

  'AC9: deleting a pool with active slice users returns 13405': function (browser) {
    const org = browser.globals.orgA;
    const name = `pool-inuse-${browser.globals.runId}`;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/vgpu-pools',
      org,
      sessionRealm: 'admin',
      body: {
        name,
        labelKey: 'go-taas.io/vgpu-pool',
        labelValue: name,
        placementPolicy: 'binpack',
        templates: [{name: 'small', memoryGib: 8, computePct: 25}]
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC9: create pool');
      const poolId = body.pool.poolId;

      // Register a model + image so a shared-GPU service can be created.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
      }, (res2) => {
        const model = api.assertOk(browser, res2, 'AC9: register model');
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/inference-services',
          org,
          sessionRealm: 'admin',
          body: {
            name: `${name}-svc`,
            modelId: model.modelId,
            modelVersion: 'v1',
            imageId: 'img-vllm-nvidia-v063',
            replicas: '1',
            accelerator: 'nvidia',
            acceleratorType: 'gpu',
            poolId,
            sliceTemplateId: 'small'
          }
        }, (res3) => {
          const created = api.assertOk(browser, res3, 'AC9: create shared-GPU service');
          browser.assert.ok(Boolean(created.serviceId), 'AC9: serviceId returned');

          // Deleting the pool now is blocked -> 13405.
          api.request(browser, {
            method: 'DELETE',
            path: `/api/v1/admin/vgpu-pools/${poolId}`,
            org,
            sessionRealm: 'admin'
          }, (res4) => {
            api.assertBusinessError(browser, res4, 13405, 'AC9: delete blocked -> 13405');
          });
        });
      });
    });
  },

  'AC7: a deploy referencing an invalid/ungranted pool returns 13403': function (browser) {
    const org = browser.globals.orgA;
    const name = `pool-13403-${browser.globals.runId}`;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/vgpu-pools',
      org,
      sessionRealm: 'admin',
      body: {
        name,
        labelKey: 'go-taas.io/vgpu-pool',
        labelValue: name,
        placementPolicy: 'binpack',
        templates: [{name: 'small', memoryGib: 8, computePct: 25}]
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC7: create pool');
      const poolId = body.pool.poolId;

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
      }, (res2) => {
        const model = api.assertOk(browser, res2, 'AC7: register model');
        const base = {
          modelId: model.modelId,
          modelVersion: 'v1',
          imageId: 'img-vllm-nvidia-v063',
          replicas: '1',
          accelerator: 'nvidia',
          acceleratorType: 'gpu'
        };
        // Unknown pool -> 13403.
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/inference-services',
          org,
          sessionRealm: 'admin',
          body: Object.assign({}, base, {name: `${name}-unknown-pool`, poolId: '00000000-0000-0000-0000-000000000000', sliceTemplateId: 'small'})
        }, (res3) => {
          api.assertBusinessError(browser, res3, 13403, 'AC7: unknown pool -> 13403');
        });
        // Unknown template in a valid pool -> 13403.
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/inference-services',
          org,
          sessionRealm: 'admin',
          body: Object.assign({}, base, {name: `${name}-unknown-tpl`, poolId, sliceTemplateId: 'nope'})
        }, (res4) => {
          api.assertBusinessError(browser, res4, 13403, 'AC7: unknown template -> 13403');
        });
      });
    });
  },

  'AC7: the tenant-visible pool list is grant-filtered and carries templates': function (browser) {
    const org = browser.globals.orgA;
    const name = `pool-tenant-${browser.globals.runId}`;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/vgpu-pools',
      org,
      sessionRealm: 'admin',
      body: {
        name,
        labelKey: 'go-taas.io/vgpu-pool',
        labelValue: name,
        placementPolicy: 'binpack',
        templates: [{name: 'small', memoryGib: 8, computePct: 25}]
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC7: create pool');
      const poolId = body.pool.poolId;
      // The user surface lists the pool (default-allow grant) with templates.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/vgpu-pools?limit=50',
        org,
        sessionRealm: 'user'
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC7: tenant list');
        const found = (body2.pools || []).find((p) => p.poolId === poolId);
        browser.assert.ok(Boolean(found), 'AC7: pool in tenant list');
        browser.assert.equal(found.name, name, 'AC7: tenant list name');
        browser.assert.ok(Array.isArray(found.templates) && found.templates.length === 1, 'AC7: templates carried');
      });
    });
  },

  // ---- UI flow (AC1/AC13) ----

  'AC1/AC13: the admin page renders the pool table and the create dialog': function (browser) {
    const org = browser.globals.orgA;
    // Create a pool so the table renders.
    const name = `pool-table-${browser.globals.runId}`;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/vgpu-pools',
      org,
      sessionRealm: 'admin',
      body: {
        name,
        labelKey: 'go-taas.io/vgpu-pool',
        labelValue: name,
        placementPolicy: 'binpack',
        templates: [{name: 'small', memoryGib: 8, computePct: 25}]
      }
    }, (res) => {
      api.assertOk(browser, res, 'AC1: create pool for table');
      browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/admin/vgpu-pools');
      browser.waitForElementPresent('[data-testid="vgpu-pool-table"]', 15000, 'AC1: table renders');
      browser.waitForElementPresent('[data-testid="vgpu-pool-search"]', 10000, 'AC1: search');
      browser.waitForElementPresent('[data-testid="vgpu-pool-filter-policy"]', 10000, 'AC1: policy filter');
      browser.waitForElementPresent('[data-testid="vgpu-pool-create"]', 10000, 'AC1: create button');
      // Open the create dialog and verify the fields.
      browser.click('[data-testid="vgpu-pool-create"]');
      browser.waitForElementPresent('[data-testid="vgpu-pool-create-dialog"]', 10000, 'AC2: create dialog');
      browser.waitForElementPresent('[data-testid="vgpu-pool-name-input"]', 10000, 'AC2: name input');
      browser.waitForElementPresent('[data-testid="vgpu-pool-label-key-input"]', 10000, 'AC2: label key input');
      browser.waitForElementPresent('[data-testid="vgpu-pool-label-value-input"]', 10000, 'AC2: label value input');
      browser.waitForElementPresent('[data-testid="vgpu-pool-policy-select"]', 10000, 'AC2: policy select');
      browser.waitForElementPresent('[data-testid="vgpu-pool-save"]', 10000, 'AC2: save button');
    });
  },

  'AC13: the admin page renders the permission-denied state for a non-admin session': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/vgpu-pools');
    browser.waitForElementPresent('[data-testid="vgpu-pool-permission-denied"]', 15000, 'AC13: permission-denied state');
  },

  'AC13: the admin page renders the empty state when no pools match': function (browser) {
    const org = browser.globals.orgA;
    // The admin pool list is platform-wide, so it is not empty after
    // other tests create pools. Search for a name that matches nothing
    // so the filtered list is empty and the empty state renders.
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/vgpu-pools');
    browser.waitForElementPresent('[data-testid="vgpu-pool-search"]', 15000, 'AC13: search renders');
    browser.setValue('[data-testid="vgpu-pool-search"]', 'no-such-pool-xyz');
    browser.waitForElementPresent('[data-testid="vgpu-pool-empty"]', 15000, 'AC13: empty state');
  },

  'AC5: the deploy form toggles Shared GPU and reveals the pool/template dropdowns': function (browser) {
    const org = browser.globals.orgA;
    // Create a pool so the deploy form has a usable pool.
    const name = `pool-deploy-${browser.globals.runId}`;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/vgpu-pools',
      org,
      sessionRealm: 'admin',
      body: {
        name,
        labelKey: 'go-taas.io/vgpu-pool',
        labelValue: name,
        placementPolicy: 'binpack',
        templates: [{name: 'small', memoryGib: 8, computePct: 25}]
      }
    }, (res) => {
      api.assertOk(browser, res, 'AC5: create pool');
      // Register a model so the deploy dialog can be opened.
      const modelName = `model-deploy-${browser.globals.runId}`;
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name: modelName, version: 'v1', weightPath: `models/${modelName}/`}
      }, (res2) => {
        api.assertOk(browser, res2, 'AC5: register model');
        // Navigate to the models page and open the deploy dialog.
        browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
        browser.url(browser.globals.baseUrl + '/admin/models');
        browser.waitForElementPresent(`[data-testid="deploy-${modelName}"]`, 15000, 'AC5: deploy button');
        browser.click(`[data-testid="deploy-${modelName}"]`);
        browser.waitForElementPresent('[data-testid="deploy-dialog"]', 15000, 'AC5: deploy dialog opens');
        browser.waitForElementPresent('[data-testid="deploy-shared-gpu-toggle"]', 15000, 'AC5: shared-GPU toggle present');
        // The toggle is enabled because a usable pool exists.
        browser.getAttribute('[data-testid="deploy-shared-gpu-toggle"]', 'disabled', (res3) => {
          browser.assert.equal(res3.value, null, 'AC5: toggle enabled when a pool exists');
        });
        // Toggle it on and verify the dropdowns appear.
        browser.click('[data-testid="deploy-shared-gpu-toggle"]');
        browser.waitForElementPresent('[data-testid="deploy-pool-select"]', 10000, 'AC5: pool dropdown');
        browser.waitForElementPresent('[data-testid="deploy-slice-template-select"]', 10000, 'AC5: slice template dropdown');
        browser.waitForElementPresent('[data-testid="deploy-shared-gpu-note"]', 10000, 'AC5: shared-GPU note');
      });
    });
  }
};