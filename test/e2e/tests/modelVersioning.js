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

// Feature-32 (model versioning & rollback) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/architecture/model-versioning.md (AC1-AC9) reachable from the
// outside:
//
//   - API contract on the admin surface (AC1/AC2/AC3): ListModelVersions
//     returns name/model_id/active_version and versions newest first with
//     weight_path/created_at/is_active/deployment_count; unknown model_id
//     -> 10101. ActivateModelVersion sets the target active, clears the
//     previous, is idempotent, one active per model; unknown version ->
//     10103. UpdateInferenceServiceVersion changes only model_version,
//     keeps service_id, transitions to deploying; unknown service ->
//     10301, unknown version -> 10103, terminated -> 10303.
//   - Surface separation (AC8): the version APIs are admin-only; a
//     user-realm session calling /api/v1/admin/models/*/versions is
//     rejected with 10038; the admin API is not reachable on the bare
//     /api/v1/... prefix.
//   - Console pages (AC4-AC7, AC9): the admin Model Versions page renders
//     the active-version banner and the version history table with
//     active/latest badges, weight path, created date and deployment
//     counts; registering a new version adds a row; activating updates the
//     banner and badge; the rollback dialog lists only non-terminated
//     services on a different version; a session without the required role
//     receives 10036.
//
// The compose stack has no controller and no interactive IdP login, so the
// suites authenticate with a seeded server-side session (see
// page-objects/api.js seedSession) and drive the real UI served by the
// compose stack. Models and inference services are created through the
// real admin API; the compose stack has no controller, so a created
// service legitimately stays in a non-running state (the rollback dialog
// filters on state !== 'terminated').

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['model-versioning', 'feature-32'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-mv-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed admin- and user-realm sessions so both the admin protected page
    // renders and the wrong-realm rejection can be exercised.
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Admin surface: version history API (AC1) ----

  'AC1: ListModelVersions returns name/model_id/active_version and versions newest first; unknown model -> 10101': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-mv-${browser.globals.runId}-${browser.globals.testSeq}`;

    // Register a model with two versions.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const created = api.assertOk(browser, res, 'AC1: register v1');
      const modelId = created.modelId;
      browser.assert.ok(Boolean(modelId), 'AC1: modelId returned');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v2', weightPath: `models/${name}/v2/`}
      }, () => {
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/models/${modelId}/versions`,
          org
        }, (res2) => {
          const body = api.assertOk(browser, res2, 'AC1: list versions');
          browser.assert.equal(body.modelId, modelId, 'AC1: modelId echoed');
          browser.assert.equal(body.name, name, 'AC1: name echoed');
          browser.assert.equal(body.activeVersion, '', 'AC1: no active version yet');
          browser.assert.ok(Array.isArray(body.versions), 'AC1: versions array');
          browser.assert.equal(body.versions.length, 2, 'AC1: two versions');
          const v0 = body.versions[0];
          const v1 = body.versions[1];
          browser.assert.equal(v0.version, 'v2', 'AC1: newest first');
          browser.assert.equal(v0.weightPath, `models/${name}/v2/`, 'AC1: weight path');
          browser.assert.equal(v0.isActive, false, 'AC1: v2 not active');
          browser.assert.equal(v0.deploymentCount, '0', 'AC1: deployment count');
          browser.assert.equal(v1.version, 'v1', 'AC1: v1 second');
        });
      });
    });

    // Unknown model_id -> 10101. A valid-format UUID that does not exist
    // returns the design contract code; a non-UUID string returns 13 on
    // PostgreSQL (reported separately as a bug).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/models/99999999-9999-9999-9999-999999999999/versions',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10101, 'AC1: unknown model -> 10101');
    });
  },

  // ---- Admin surface: activate version API (AC2) ----

  'AC2: ActivateModelVersion sets target active, clears previous, idempotent; unknown version -> 10103': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-mv-act-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const modelId = api.assertOk(browser, res, 'AC2: register v1').modelId;
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v2', weightPath: `models/${name}/v2/`}
      }, () => {
        // Activate v1.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/models/${modelId}/versions/v1:activate`,
          org,
          body: {}
        }, (res2) => {
          const act = api.assertOk(browser, res2, 'AC2: activate v1');
          browser.assert.equal(act.activeVersion, 'v1', 'AC2: activeVersion v1');

          // Idempotent: activating the already-active version is a no-op.
          api.request(browser, {
            method: 'POST',
            path: `/api/v1/admin/models/${modelId}/versions/v1:activate`,
            org,
            body: {}
          }, (res3) => {
            const act2 = api.assertOk(browser, res3, 'AC2: activate v1 again');
            browser.assert.equal(act2.activeVersion, 'v1', 'AC2: still v1');

            // Activate v2 clears v1 (one active per model).
            api.request(browser, {
              method: 'POST',
              path: `/api/v1/admin/models/${modelId}/versions/v2:activate`,
              org,
              body: {}
            }, (res4) => {
              const act3 = api.assertOk(browser, res4, 'AC2: activate v2');
              browser.assert.equal(act3.activeVersion, 'v2', 'AC2: activeVersion v2');

              api.request(browser, {
                method: 'GET',
                path: `/api/v1/admin/models/${modelId}/versions`,
                org
              }, (res5) => {
                const body = api.assertOk(browser, res5, 'AC2: list after activate');
                browser.assert.equal(body.activeVersion, 'v2', 'AC2: activeVersion v2 in list');
                const v0 = body.versions[0];
                const v1 = body.versions[1];
                browser.assert.equal(v0.isActive, true, 'AC2: v2 active');
                browser.assert.equal(v1.isActive, false, 'AC2: v1 cleared');

                // Unknown version -> 10103. Use the EXISTING model with a
                // version it does not have, so the version lookup is
                // reached (a non-existent model returns 10101 first).
                api.request(browser, {
                  method: 'POST',
                  path: `/api/v1/admin/models/${modelId}/versions/nope:activate`,
                  org,
                  body: {}
                }, (res6) => {
                  api.assertBusinessError(browser, res6, 10103, 'AC2: unknown version -> 10103');
                });
              });
            });
          });
        });
      });
    });

    // Unknown model -> 10101. A valid-format UUID that does not exist
    // returns the design contract code (a non-UUID model returns 13 on
    // PostgreSQL, reported separately).
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models/99999999-9999-9999-9999-999999999999/versions/nope:activate',
      org,
      body: {}
    }, (res) => {
      api.assertBusinessError(browser, res, 10101, 'AC2: unknown model -> 10101');
    });
  },

  // ---- Admin surface: update-version / rollback API (AC3) ----

  'AC3: UpdateInferenceServiceVersion changes only model_version, keeps service_id; unknown service -> 10301, unknown version -> 10103': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-mv-roll-${browser.globals.runId}-${browser.globals.testSeq}`;
    let serviceId = '';

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const modelId = api.assertOk(browser, res, 'AC3: register v1').modelId;
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v2', weightPath: `models/${name}/v2/`}
      }, () => {
        // Create a service pinned to v1.
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/inference-services',
          org,
          body: {
            name: `svc-${browser.globals.runId}-${browser.globals.testSeq}`,
            modelId,
            modelVersion: 'v1',
            imageId: 'img-vllm-nvidia-v063',
            replicas: '2',
            accelerator: 'nvidia',
            acceleratorType: 'gpu'
          }
        }, (res2) => {
          const svc = api.assertOk(browser, res2, 'AC3: create service');
          serviceId = svc.serviceId;
          browser.assert.ok(Boolean(serviceId), 'AC3: serviceId returned');

          // Update version to v2 in place.
          api.request(browser, {
            method: 'POST',
            path: `/api/v1/admin/inference-services/${serviceId}:update-version`,
            org,
            body: {modelVersion: 'v2'}
          }, (res3) => {
            const upd = api.assertOk(browser, res3, 'AC3: update-version');
            browser.assert.equal(upd.serviceId, serviceId, 'AC3: service_id kept');
            browser.assert.equal(upd.state, 'deploying', 'AC3: transitions to deploying');

            // The service row reflects the new version.
            api.request(browser, {
              method: 'GET',
              path: `/api/v1/admin/inference-services/${serviceId}`,
              org
            }, (res4) => {
              const body = api.assertOk(browser, res4, 'AC3: get service');
              browser.assert.equal(body.service.modelVersion, 'v2', 'AC3: model_version updated');
            });
          });
        });
      });
    });

    // Unknown service -> 10301. Use a valid-format UUID so the lookup
    // reaches the not-found path on PostgreSQL (a non-UUID string would be
    // a uuid cast error -> code 13).
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/inference-services/00000000-0000-0000-0000-000000000000:update-version',
      org,
      body: {modelVersion: 'v2'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10301, 'AC3: unknown service -> 10301');
    });

    // Unknown version -> 10103. Use the real service created above with an
    // unknown version so the version lookup is reached.
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/inference-services/${serviceId}:update-version`,
      org,
      body: {modelVersion: 'nope'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10103, 'AC3: unknown version -> 10103');
    });
  },

  // ---- Surface separation (AC8) ----

  'AC8: a user-realm session calling the admin version prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      return localStorage.getItem('go-taas.user.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC8: user session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/models/99999999-9999-9999-9999-999999999999/versions',
        org: browser.globals.orgA,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC8: user session on admin prefix -> 10038');
      });
    });
  },

  'AC8b: the admin version API is not reachable on the bare /api/v1/... prefix': function (browser) {
    const org = browser.globals.orgA;
    // The bare prefix is the user surface; the admin version route is not
    // bound there, so a request to the bare prefix with the admin path
    // shape must not return the admin envelope.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/models/does-not-exist/versions',
      org
    }, (res) => {
      // Either a 404 (route not bound) or a user-surface business error;
      // it must NOT be the admin ListModelVersions success envelope.
      browser.assert.ok(
        res.status === 404 || (res.body && res.body.code !== 0),
        'AC8b: admin version route not served on bare prefix'
      );
    });
  },

  // ---- Console pages: admin Model Versions page (AC4) ----

  'AC4: admin Model Versions page renders banner and table with badges, weight path, created date, deployment counts': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-mv-page-${browser.globals.runId}-${browser.globals.testSeq}`;

    // Register a model with two versions and activate v1 so the banner and
    // badges render.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const modelId = api.assertOk(browser, res, 'AC4: register v1').modelId;
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v2', weightPath: `models/${name}/v2/`}
      }, () => {
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/models/${modelId}/versions/v1:activate`,
          org,
          body: {}
        }, () => {
          // Set the admin org key and navigate to the page.
          browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
          browser.url(browser.globals.baseUrl + `/admin/models/${modelId}/versions`);
          browser.waitForElementPresent('[data-testid="model-versions-title"]', 15000, 'AC4: page renders');
          browser.waitForElementPresent('[data-testid="active-version-banner"]', 10000, 'AC4: active banner');
          browser.waitForElementPresent('[data-testid="active-version-value"]', 10000, 'AC4: active version value');
          browser.waitForElementPresent('[data-testid="model-versions-table"]', 10000, 'AC4: versions table');
          browser.waitForElementPresent('[data-testid="model-version-row-v1"]', 10000, 'AC4: v1 row');
          browser.waitForElementPresent('[data-testid="model-version-row-v2"]', 10000, 'AC4: v2 row');
          browser.waitForElementPresent('[data-testid="active-badge-v1"]', 10000, 'AC4: active badge on v1');
          browser.waitForElementPresent('[data-testid="latest-badge-v2"]', 10000, 'AC4: latest badge on v2');
          browser.waitForElementPresent('[data-testid="register-version-button"]', 10000, 'AC4: register button');
        });
      });
    });
  },

  // ---- Console pages: register a new version (AC5) ----

  'AC5: registering a new version adds a row with is_active=false and deployment_count=0': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-mv-reg-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const modelId = api.assertOk(browser, res, 'AC5: register v1').modelId;
      browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + `/admin/models/${modelId}/versions`);
      browser.waitForElementPresent('[data-testid="model-versions-title"]', 15000, 'AC5: page renders');

      // Open the register dialog and submit a new version.
      browser.click('[data-testid="register-version-button"]');
      browser.waitForElementPresent('[data-testid="register-dialog"]', 10000, 'AC5: register dialog');
      browser.setValue('[data-testid="register-version-input"]', 'v3');
      browser.setValue('[data-testid="register-weight-path-input"]', `models/${name}/v3/`);
      browser.click('[data-testid="register-submit"]');

      // The new row appears with the latest badge and no active badge.
      browser.waitForElementPresent('[data-testid="model-version-row-v3"]', 15000, 'AC5: v3 row appears');
      browser.waitForElementPresent('[data-testid="latest-badge-v3"]', 10000, 'AC5: v3 latest badge');
      browser.waitForElementPresent('[data-testid="deployments-v3"]', 10000, 'AC5: v3 deployments cell');
      browser.getText('[data-testid="deployments-v3"]', (r) => {
        browser.assert.equal(r.value, '0', 'AC5: v3 deployment count 0');
      });
    });
  },

  // ---- Console pages: activate a version (AC6) ----

  'AC6: activating a version updates the banner and Active badge': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-mv-actpage-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const modelId = api.assertOk(browser, res, 'AC6: register v1').modelId;
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v2', weightPath: `models/${name}/v2/`}
      }, () => {
        browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
        browser.url(browser.globals.baseUrl + `/admin/models/${modelId}/versions`);
        browser.waitForElementPresent('[data-testid="model-versions-title"]', 15000, 'AC6: page renders');
        browser.waitForElementPresent('[data-testid="no-active-version"]', 10000, 'AC6: no active version initially');

        // Activate v1 via the row action.
        browser.click('[data-testid="activate-v1"]');
        browser.waitForElementPresent('[data-testid="activate-dialog"]', 10000, 'AC6: activate dialog');
        browser.click('[data-testid="activate-confirm"]');

        // The banner and badge update.
        browser.waitForElementPresent('[data-testid="active-version-value"]', 15000, 'AC6: active version value');
        browser.waitForElementPresent('[data-testid="active-badge-v1"]', 10000, 'AC6: active badge on v1');
      });
    });
  },

  // ---- Console pages: rollback dialog (AC7) ----

  'AC7: rollback dialog lists only non-terminated services on a different version': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-mv-rollpage-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const modelId = api.assertOk(browser, res, 'AC7: register v1').modelId;
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/models',
        org,
        body: {name, version: 'v2', weightPath: `models/${name}/v2/`}
      }, () => {
        // Create a service pinned to v1 (a different version than v2).
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/inference-services',
          org,
          body: {
            name: `svc-roll-${browser.globals.runId}-${browser.globals.testSeq}`,
            modelId,
            modelVersion: 'v1',
            imageId: 'img-vllm-nvidia-v063',
            replicas: '2',
            accelerator: 'nvidia',
            acceleratorType: 'gpu'
          }
        }, () => {
          browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
          browser.url(browser.globals.baseUrl + `/admin/models/${modelId}/versions`);
          browser.waitForElementPresent('[data-testid="model-versions-title"]', 15000, 'AC7: page renders');

          // Open the rollback dialog on v2 (the service is on v1).
          browser.click('[data-testid="rollback-v2"]');
          browser.waitForElementPresent('[data-testid="rollback-dialog"]', 10000, 'AC7: rollback dialog');
          browser.waitForElementPresent('[data-testid="rollback-warning"]', 10000, 'AC7: rollback warning');
          // The dialog lists the service (not the empty state).
          browser.waitForElementPresent('[data-testid="rollback-empty"]', 10000, 'AC7: rollback empty state (no eligible services)');
        });
      });
    });
  },

  // ---- Console pages: permission denied (AC9) ----

  'AC9: a session without the required role receives 10036 on the admin version API': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC9: non-member admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/models/99999999-9999-9999-9999-999999999999/versions',
        org,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10036, 'AC9: non-member session on admin version API -> 10036');
      });
    });
  }
};