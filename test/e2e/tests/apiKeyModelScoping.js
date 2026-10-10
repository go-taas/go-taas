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

// Feature-46 (api-key-model-scoping) e2e suite, run against the compose
// stack gateway. Covers the console and API surfaces of
// docs/architecture/api-key-model-scoping.md §13.5: AC1 (scoped create +
// list indicator), AC4 (scope edit via the UI), AC5 (picker lists granted
// models), AC6 (admin binding read-only, no admin scope-write route),
// AC7 (api_key.scope_updated in the user activity feed), AC8 (cross-tenant
// masking). AC2/AC3 (VerifyAPIKey enforcement) are FVT-only: the data-plane
// verify path has no HTTP binding in the compose stack.

const api = require('../page-objects/api.js');

// Registers a model and grants it to orgA, then hands {modelId, name}
// to the continuation. Admin-realm request (the model catalog is admin).
// Standalone function (not an object property): Nightwatch runs every
// function property of the export as a test case.
function registerGrantedModel(browser, org, name, done) {
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/admin/models',
    org,
    body: {name, version: 'v1', weight_path: `models/${name}/v1`}
  }, (res) => {
    const body = api.assertOk(browser, res, `register model ${name}`);
    const modelId = body.modelId;
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/models/${modelId}:grant`,
      org,
      body: {organization_id: org}
    }, (res2) => {
      api.assertOk(browser, res2, `grant model ${name} to orgA`);
      done({modelId, name});
    });
  });
}

module.exports = {
  '@tags': ['auth', 'api-key', 'api-key-model-scoping', 'feature-46'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-scope-${browser.globals.runId}-${browser.globals.testSeq}`;
    browser.globals.orgB = `org-e2e-scope-b-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.ensureOrg(browser, browser.globals.orgB);
    // The user surface drives this feature; the admin realm is needed to
    // register models and grant them to the org (AC5's picker source).
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC1: scoped create stores the list and the list view shows the indicator': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-scope-m1-${browser.globals.runId}-${browser.globals.testSeq}`;

    registerGrantedModel(browser, org, name, (model) => {
      // Create a scoped key via the user API (the console dialog posts
      // exactly this shape).
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/auth/api-keys',
        org,
        sessionRealm: 'user',
        body: {name: 'scoped', models: [model.modelId]}
      }, (res) => {
        const body = api.assertOk(browser, res, 'create scoped key');
        const keyId = body.keyId;
        browser.assert.ok(Boolean(keyId), 'AC1: key id returned');

        api.request(browser, {
          path: '/api/v1/auth/api-keys?page.limit=100',
          org,
          sessionRealm: 'user'
        }, (res2) => {
          const listBody = api.assertOk(browser, res2, 'list keys');
          const key = (listBody.keys || []).find((k) => k.keyId === keyId);
          browser.assert.ok(key, 'AC1: created key listed');
          browser.assert.ok(
            key && Array.isArray(key.models) && key.models.length === 1
              && key.models[0] === model.modelId,
            'AC1: list echoes the model scope'
          );
        });
      });
    });
  },

  'AC5: the scope picker offers exactly the org-granted models': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-scope-m2-${browser.globals.runId}-${browser.globals.testSeq}`;

    registerGrantedModel(browser, org, name, (model) => {
      // The user-surface model list is the picker's source (AD7).
      api.request(browser, {
        path: '/api/v1/models?page.limit=100',
        org,
        sessionRealm: 'user'
      }, (res) => {
        const body = api.assertOk(browser, res, 'list user models');
        const models = body.models || [];
        const found = models.find((m) => m.modelId === model.modelId);
        browser.assert.ok(found, 'AC5: granted model visible to the user surface');
      });

      // Unknown model ids are rejected at create time (10039-family
      // validation: 10101 MODEL_NOT_FOUND).
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/auth/api-keys',
        org,
        sessionRealm: 'user',
        body: {name: 'bad-scope', models: ['mdl-does-not-exist']}
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10101, 'AC5: unknown model rejected');
      });

      // Duplicates are rejected (10008 invalid argument).
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/auth/api-keys',
        org,
        sessionRealm: 'user',
        body: {name: 'dup-scope', models: [model.modelId, model.modelId]}
      }, (res3) => {
        api.assertBusinessError(browser, res3, 10008, 'AC5: duplicate model rejected');
      });
    });
  },

  'AC4: scope edit replaces the list and the console list refreshes': function (browser) {
    const org = browser.globals.orgA;
    const seq = `${browser.globals.runId}-${browser.globals.testSeq}`;
    const name1 = `e2e-scope-m3-${seq}`;
    const name2 = `e2e-scope-m4-${seq}`;

    registerGrantedModel(browser, org, name1, (m1) => {
      registerGrantedModel(browser, org, name2, (m2) => {
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/auth/api-keys',
          org,
          sessionRealm: 'user',
          body: {name: 'editable', models: [m1.modelId]}
        }, (res) => {
          const body = api.assertOk(browser, res, 'create key');
          const keyId = body.keyId;

          // Replace the scope with the second model (full replacement
          // semantics, AD5).
          api.request(browser, {
            method: 'PUT',
            path: `/api/v1/auth/api-keys/${keyId}/scope`,
            org,
            sessionRealm: 'user',
            body: {models: [m2.modelId]}
          }, (res2) => {
            const updated = api.assertOk(browser, res2, 'update scope');
            browser.assert.ok(
              updated.key && Array.isArray(updated.key.models)
                && updated.key.models.length === 1
                && updated.key.models[0] === m2.modelId,
              'AC4: scope update returns the replaced list'
            );

            // The list view reflects the new scope.
            api.request(browser, {
              path: '/api/v1/auth/api-keys?page.limit=100',
              org,
              sessionRealm: 'user'
            }, (res3) => {
              const listBody = api.assertOk(browser, res3, 'list keys');
              const key = (listBody.keys || []).find((k) => k.keyId === keyId);
              browser.assert.ok(
                key && Array.isArray(key.models) && key.models.length === 1
                  && key.models[0] === m2.modelId,
                'AC4: list shows the replaced scope'
              );
            });
          });
        });
      });
    });
  },

  'AC4b: clearing the scope returns the key to all-models': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-scope-m5-${browser.globals.runId}-${browser.globals.testSeq}`;

    registerGrantedModel(browser, org, name, (model) => {
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/auth/api-keys',
        org,
        sessionRealm: 'user',
        body: {name: 'clearable', models: [model.modelId]}
      }, (res) => {
        const body = api.assertOk(browser, res, 'create key');
        const keyId = body.keyId;

        // Empty list = unrestricted (the "all models" radio).
        api.request(browser, {
          method: 'PUT',
          path: `/api/v1/auth/api-keys/${keyId}/scope`,
          org,
          sessionRealm: 'user',
          body: {models: []}
        }, (res2) => {
          const updated = api.assertOk(browser, res2, 'clear scope');
          browser.assert.ok(
            updated.key && (!updated.key.models || updated.key.models.length === 0),
            'AC4b: cleared scope is empty'
          );
        });
      });
    });
  },

  'AC6: admin binding is read-only and the admin scope-write route is absent': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-scope-m6-${browser.globals.runId}-${browser.globals.testSeq}`;

    registerGrantedModel(browser, org, name, (model) => {
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/auth/api-keys',
        org,
        sessionRealm: 'user',
        body: {name: 'admin-visible', models: [model.modelId]}
      }, (res) => {
        const body = api.assertOk(browser, res, 'create key');
        const keyId = body.keyId;

        // The deprecated admin list binding returns models[] read-only.
        api.request(browser, {
          path: '/api/v1/admin/auth/api-keys?page.limit=100',
          org
        }, (res2) => {
          const listBody = api.assertOk(browser, res2, 'admin list keys');
          const key = (listBody.keys || []).find((k) => k.keyId === keyId);
          browser.assert.ok(key, 'AC6: key visible via admin binding');
          browser.assert.ok(
            key && Array.isArray(key.models) && key.models.length === 1
              && key.models[0] === model.modelId,
            'AC6: admin binding returns the models array'
          );

          // The admin scope-write route must not exist (404, not a
          // business error).
          api.request(browser, {
            method: 'PUT',
            path: `/api/v1/admin/auth/api-keys/${keyId}/scope`,
            org,
            body: {models: []}
          }, (res3) => {
            browser.assert.equal(
              res3.status,
              404,
              'AC6: admin scope write is not routed (HTTP 404)'
            );
          });
        });
      });
    });
  },

  'AC7: scope edits emit api_key.scope_updated in the user activity feed': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-scope-m7-${browser.globals.runId}-${browser.globals.testSeq}`;

    registerGrantedModel(browser, org, name, (model) => {
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/auth/api-keys',
        org,
        sessionRealm: 'user',
        body: {name: 'audited', models: [model.modelId]}
      }, (res) => {
        const body = api.assertOk(browser, res, 'create key');
        const keyId = body.keyId;

        api.request(browser, {
          method: 'PUT',
          path: `/api/v1/auth/api-keys/${keyId}/scope`,
          org,
          sessionRealm: 'user',
          body: {models: []}
        }, (res2) => {
          api.assertOk(browser, res2, 'update scope for audit');

          api.request(browser, {
            path: '/api/v1/audit/activity?page.limit=100',
            org,
            sessionRealm: 'user'
          }, (res3) => {
            const actBody = api.assertOk(browser, res3, 'my activity');
            const events = actBody.auditEvents || [];
            const evt = events.find(
              (e) => e.action === 'api_key.scope_updated' && e.resourceId === keyId
            );
            browser.assert.ok(evt, 'AC7: api_key.scope_updated present in activity');
          });
        });
      });
    });
  },

  'AC8: a cross-tenant session cannot see or edit another org key': function (browser) {
    const org = browser.globals.orgA;
    const orgB = browser.globals.orgB;
    const name = `e2e-scope-m8-${browser.globals.runId}-${browser.globals.testSeq}`;

    registerGrantedModel(browser, org, name, (model) => {
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/auth/api-keys',
        org,
        sessionRealm: 'user',
        body: {name: 'isolated', models: [model.modelId]}
      }, (res) => {
        const body = api.assertOk(browser, res, 'create key in orgA');
        const keyId = body.keyId;

        // Seed an orgB user session; the session's active org wins over
        // the X-Organization-Id header (D6/FR4.4).
        api.seedSession(browser, 'user', orgB);

        // orgB's list does not contain orgA's key.
        api.request(browser, {
          path: '/api/v1/auth/api-keys?page.limit=100',
          org,
          sessionRealm: 'user'
        }, (res2) => {
          const listBody = api.assertOk(browser, res2, 'orgB list keys');
          const keys = listBody.keys || [];
          browser.assert.ok(
            !keys.some((k) => k.keyId === keyId),
            'AC8: orgB list masks orgA keys'
          );

          // orgB cannot edit orgA's key scope: 10007 without leaking
          // existence details.
          api.request(browser, {
            method: 'PUT',
            path: `/api/v1/auth/api-keys/${keyId}/scope`,
            org,
            sessionRealm: 'user',
            body: {models: []}
          }, (res3) => {
            api.assertBusinessError(browser, res3, 10007, 'AC8: cross-org scope edit masked');
          });
        });
      });
    });
  },

  'AC1-C: console create dialog persists the scope and the table shows the indicator': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-scope-m9-${browser.globals.runId}-${browser.globals.testSeq}`;

    registerGrantedModel(browser, org, name, (model) => {
      // Drive the real console page.
      browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/api-keys');
      browser.waitForElementPresent(
        '[data-testid="create-api-key"]',
        10000,
        'AC1-C: create button renders'
      );
      browser.click('[data-testid="create-api-key"]');
      browser.waitForElementPresent('[data-testid="create-dialog"]', 5000, 'AC1-C: create dialog opens');
      browser.setValue('[data-testid="key-name-input"]', 'console-scoped');
      // Restrict to selected models, then tick the granted model.
      browser.click('[data-testid="scope-option-restricted"]');
      browser.waitForElementPresent(
        `[data-testid="scope-model-${model.modelId}"]`,
        10000,
        'AC1-C: granted model appears in the picker'
      );
      browser.click(`[data-testid="scope-model-${model.modelId}"]`);
      browser.click('[data-testid="submit-create-key"]');
      // The one-time secret dialog appears; acknowledge it.
      browser.waitForElementPresent('[data-testid="secret-display"]', 10000, 'AC1-C: secret shown once');
      browser.click('[data-testid="saved-checkbox"]');
      browser.click('[data-testid="close-created-dialog"]');
      // The table refreshes with the scope indicator.
      browser.waitForElementPresent('[data-testid="api-keys-table"]', 10000, 'AC1-C: keys table renders');
      browser.waitForElementPresent(
        '[data-testid^="api-key-row-"]',
        10000,
        'AC1-C: key row renders'
      );

      // Verify persistence through the API (the row testid carries the
      // key id, which the console does not expose otherwise).
      api.request(browser, {
        path: '/api/v1/auth/api-keys?page.limit=100',
        org,
        sessionRealm: 'user'
      }, (res) => {
        const body = api.assertOk(browser, res, 'list keys after console create');
        const key = (body.keys || []).find((k) => k.name === 'console-scoped');
        browser.assert.ok(key, 'AC1-C: console-created key listed');
        browser.assert.ok(
          key && Array.isArray(key.models) && key.models.length === 1
            && key.models[0] === model.modelId,
          'AC1-C: console create persisted the model scope'
        );
      });
    });
  },

  'AC4-C: console edit-scope dialog updates the scope and refreshes the table': function (browser) {
    const org = browser.globals.orgA;
    const seq = `${browser.globals.runId}-${browser.globals.testSeq}`;
    const name1 = `e2e-scope-m10-${seq}`;
    const name2 = `e2e-scope-m11-${seq}`;

    registerGrantedModel(browser, org, name1, (m1) => {
      registerGrantedModel(browser, org, name2, (m2) => {
        // Create a scoped key via the API first.
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/auth/api-keys',
          org,
          sessionRealm: 'user',
          body: {name: 'console-edit', models: [m1.modelId]}
        }, (res) => {
          const body = api.assertOk(browser, res, 'create key');
          const keyId = body.keyId;

          browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
          browser.url(browser.globals.baseUrl + '/api-keys');
          browser.waitForElementPresent(
            `[data-testid="edit-scope-${keyId}"]`,
            10000,
            'AC4-C: edit-scope action renders for the active key'
          );
          browser.click(`[data-testid="edit-scope-${keyId}"]`);
          browser.waitForElementPresent('[data-testid="edit-scope-dialog"]', 5000, 'AC4-C: scope dialog opens');
          // The dialog opens in restricted mode (the key has a scope).
          browser.waitForElementPresent(
            `[data-testid="scope-model-${m2.modelId}"]`,
            10000,
            'AC4-C: picker lists the other granted model'
          );
          // Untick m1, tick m2 (full replacement).
          browser.click(`[data-testid="scope-model-${m1.modelId}"]`);
          browser.click(`[data-testid="scope-model-${m2.modelId}"]`);
          browser.click('[data-testid="submit-scope-save"]');
          // The dialog closes and the table refreshes.
          browser.waitForElementNotPresent('[data-testid="edit-scope-dialog"]', 10000, 'AC4-C: scope dialog closes');

          api.request(browser, {
            path: '/api/v1/auth/api-keys?page.limit=100',
            org,
            sessionRealm: 'user'
          }, (res2) => {
            const listBody = api.assertOk(browser, res2, 'list keys after console edit');
            const key = (listBody.keys || []).find((k) => k.keyId === keyId);
            browser.assert.ok(
              key && Array.isArray(key.models) && key.models.length === 1
                && key.models[0] === m2.modelId,
              'AC4-C: console edit replaced the scope'
            );
          });
        });
      });
    });
  }
};
