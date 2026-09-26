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

// Feature-19 (model × engine × card-type compatibility matrix) e2e suite,
// run against the compose stack gateway. Covers the acceptance criteria of
// docs/design/compatibility-matrix.md reachable from the outside: the admin
// matrix page (AC8/AC9/AC10/AC13), the end-user model detail page
// (AC12/AC14), and the API contract + surface separation.
//
// The compose stack has no Controller/Kubernetes, so the accelerator
// inventory is empty by default and the first-boot seed creates no rows.
// The suite seeds a full inventory snapshot into the accelerator.inventory
// NATS subject (test/e2e/seed/seed_accelerator.go) and registers a model so
// the matrix has a non-empty cross product to exercise against the real UI.

const { execSync } = require('child_process');
const path = require('path');
const api = require('../page-objects/api.js');

// Seed the accelerator inventory by publishing a full snapshot to the
// accelerator.inventory NATS subject inside the compose network. The seed
// program mirrors the Controller's snapshot wire format. `omitCard` drops
// every node carrying the named card type so the not_in_fleet derivation
// (AC6/AC10) can be exercised.
function seedInventory(browser, omitCard) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const omit = omitCard ? ` -omitCard ${omitCard}` : '';
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    // Persist the Go module + build caches across runs so the seed does
    // not re-download dependencies on every invocation (the compose stack
    // has no controller, so the inventory must be seeded per suite).
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed',
    'golang:1.26-alpine',
    `sh -c "go run seed_accelerator.go${omit}"`
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedInventory failed: ${e.message}`);
  }
}

// Register a model in the catalog and return its modelId via the callback.
function createModel(browser, org, name, done) {
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/admin/models',
    org,
    body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
  }, (res) => {
    const body = api.assertOk(browser, res, `createModel ${name}`);
    done(body.modelId);
  });
}

module.exports = {
  '@tags': ['compatibility-matrix', 'feature-19'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-compat-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation (AC13/AC14) ----

  'AC13: compatibility APIs are admin-only (user prefix 404s)': function (browser) {
    // The admin-prefix endpoints are reachable.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/compatibility?page.limit=5',
      org: browser.globals.orgA
    }, (res) => {
      api.assertOk(browser, res, 'AC13: admin matrix reachable');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/compatibility/dimensions',
      org: browser.globals.orgA
    }, (res) => {
      api.assertOk(browser, res, 'AC13: admin dimensions reachable');
    });
    // The user-prefix equivalents must NOT exist (404).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/compatibility?page.limit=5',
      org: browser.globals.orgA
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC13: user-prefix matrix is 404');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/compatibility/dimensions',
      org: browser.globals.orgA
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC13: user-prefix dimensions is 404');
    });
  },

  'AC14: user compatibility API is user-only (admin prefix 404s)': function (browser) {
    // The user-prefix endpoint is reachable (unknown model -> 10101, not 404).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/models/00000000-0000-0000-0000-000000000000/compatibility',
      org: browser.globals.orgA
    }, (res) => {
      api.assertBusinessError(browser, res, 10101, 'AC14: user compat reachable (10101 for unknown)');
    });
    // The admin-prefix equivalent must NOT exist (404).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/models/nonexistent-model/compatibility',
      org: browser.globals.orgA
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC14: admin-prefix user compat is 404');
    });
  },

  'AC13: admin page is served at /admin/compatibility': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/compatibility');
    browser.waitForElementPresent('[data-testid="compatibility-page"]', 15000, 'AC13: admin page served');
  },

  // ---- API contract (AC2/AC3/AC4/AC5/AC7) ----

  'AC2/AC3: matrix list + dimensions return cells and axes': function (browser) {
    const org = browser.globals.orgA;
    seedInventory(browser);
    createModel(browser, org, `e2e-compat-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      browser.globals.compatModelId = modelId;
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/compatibility?page.limit=100',
        org
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC2: matrix list');
        const cells = body.cells || [];
        browser.assert.ok(cells.length > 0, 'AC2: matrix has cells');
        const cell = cells[0];
        browser.assert.ok(Boolean(cell.modelId), 'AC2: cell.modelId');
        browser.assert.ok(Boolean(cell.modelName), 'AC2: cell.modelName');
        browser.assert.ok(Boolean(cell.engine), 'AC2: cell.engine');
        browser.assert.ok(Boolean(cell.cardType), 'AC2: cell.cardType');
        browser.assert.ok(['supported', 'experimental', 'unsupported'].includes(cell.status), 'AC2: cell.status in closed set');
        browser.assert.ok('notInFleet' in cell, 'AC2: cell.notInFleet');
        browser.assert.ok('updatedAt' in cell, 'AC2: cell.updatedAt');
      });
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/compatibility/dimensions',
        org
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC3: dimensions');
        browser.assert.ok((body.models || []).length > 0, 'AC3: models axis');
        browser.assert.ok((body.engines || []).length > 0, 'AC3: engines axis');
        browser.assert.ok((body.cardTypes || []).length > 0, 'AC3: cardTypes axis');
        const counts = body.statusCounts || {};
        browser.assert.ok('supported' in counts, 'AC3: supported count');
        browser.assert.ok('experimental' in counts, 'AC3: experimental count');
        browser.assert.ok('unsupported' in counts, 'AC3: unsupported count');
      });
    });
  },

  'AC2: matrix list filters by status and search': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-compat-filter-${browser.globals.runId}-${browser.globals.testSeq}`;
    seedInventory(browser);
    createModel(browser, org, name, () => {
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/compatibility?status=experimental&page.limit=100',
        org
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC2: status filter');
        const cells = body.cells || [];
        browser.assert.ok(cells.length > 0, 'AC2: experimental cells exist');
        cells.forEach((c) => browser.assert.equal(c.status, 'experimental', 'AC2: all filtered cells experimental'));
      });
      api.request(browser, {
        method: 'GET',
        path: `/api/v1/admin/compatibility?search=${encodeURIComponent(name)}&page.limit=100`,
        org
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC2: search filter');
        const cells = body.cells || [];
        browser.assert.ok(cells.length > 0, 'AC2: search returns cells');
        cells.forEach((c) => browser.assert.equal(c.modelName, name, 'AC2: all cells match searched model'));
      });
    });
  },

  'AC4: set status + note; invalid status -> 10210; unknown dimension -> 10211': function (browser) {
    const org = browser.globals.orgA;
    seedInventory(browser);
    createModel(browser, org, `e2e-compat-set-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/compatibility/${modelId}/vllm/A800`,
        org,
        body: {status: 'supported', note: 'validated on A800'}
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC4: set status');
        browser.assert.equal(body.cell.status, 'supported', 'AC4: status echoed');
        browser.assert.equal(body.cell.note, 'validated on A800', 'AC4: note echoed');
      });
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/compatibility/${modelId}/vllm/A800`,
        org,
        body: {status: 'bogus'}
      }, (res) => {
        api.assertBusinessError(browser, res, 10210, 'AC4: invalid status -> 10210');
      });
      api.request(browser, {
        method: 'PUT',
        path: '/api/v1/admin/compatibility/nonexistent-model/vllm/A800',
        org,
        body: {status: 'supported'}
      }, (res) => {
        api.assertBusinessError(browser, res, 10211, 'AC4: unknown dimension -> 10211');
      });
    });
  },

  'AC5: bulk set is atomic and returns the count; failure leaves cells unchanged': function (browser) {
    const org = browser.globals.orgA;
    seedInventory(browser);
    createModel(browser, org, `e2e-compat-bulk-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/compatibility:bulk',
        org,
        body: {
          cells: [
            {modelId, engine: 'vllm', cardType: 'A800'},
            {modelId, engine: 'vllm', cardType: 'H800'}
          ],
          status: 'supported', note: 'bulk'
        }
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC5: bulk set');
        browser.assert.equal(body.updated, '2', 'AC5: updated count 2');
      });
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/compatibility:bulk',
        org,
        body: {
          cells: [{modelId: 'nonexistent-model', engine: 'vllm', cardType: 'A800'}],
          status: 'unsupported'
        }
      }, (res) => {
        api.assertBusinessError(browser, res, 10211, 'AC5: bulk with unknown dimension -> 10211');
      });
    });
  },

  'AC7: user compatibility is masked (supported/experimental only); unknown -> 10101': function (browser) {
    const org = browser.globals.orgA;
    seedInventory(browser);
    createModel(browser, org, `e2e-compat-masked-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      // Curate one supported cell so the masked projection has content.
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/compatibility/${modelId}/vllm/A800`,
        org,
        body: {status: 'supported', note: 'validated'}
      }, () => {
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/models/${modelId}/compatibility`,
          org
        }, (res) => {
          const body = api.assertOk(browser, res, 'AC7: user compat');
          const entries = body.entries || [];
          browser.assert.ok(entries.length > 0, 'AC7: masked projection has entries');
          entries.forEach((e) => {
            browser.assert.ok(['supported', 'experimental'].includes(e.status), 'AC7: no unsupported leaked');
          });
          browser.assert.ok('supportedCount' in body, 'AC7: supportedCount');
          browser.assert.ok('experimentalCount' in body, 'AC7: experimentalCount');
        });
      });
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/models/00000000-0000-0000-0000-000000000000/compatibility',
        org
      }, (res) => {
        api.assertBusinessError(browser, res, 10101, 'AC7: unknown model -> 10101');
      });
    });
  },

  // ---- Admin UI (AC8/AC9/AC10/AC13) ----

  'AC8: admin page renders summary strip, filters, and grid with last-updated': function (browser) {
    const org = browser.globals.orgA;
    seedInventory(browser);
    createModel(browser, org, `e2e-compat-ui-${browser.globals.runId}-${browser.globals.testSeq}`, () => {
      browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/admin/compatibility');
      browser.waitForElementPresent('[data-testid="compatibility-page"]', 15000, 'AC8: page renders');
      browser.waitForElementPresent('[data-testid="compatibility-summary-strip"]', 15000, 'AC8: summary strip');
      browser.waitForElementPresent('[data-testid="compatibility-supported-count"]', 10000, 'AC8: supported count');
      browser.waitForElementPresent('[data-testid="compatibility-experimental-count"]', 10000, 'AC8: experimental count');
      browser.waitForElementPresent('[data-testid="compatibility-unsupported-count"]', 10000, 'AC8: unsupported count');
      browser.waitForElementPresent('[data-testid="compatibility-filters"]', 10000, 'AC8: filters bar');
      browser.waitForElementPresent('[data-testid="compatibility-model-filter"]', 10000, 'AC8: model filter');
      browser.waitForElementPresent('[data-testid="compatibility-engine-filter"]', 10000, 'AC8: engine filter');
      browser.waitForElementPresent('[data-testid="compatibility-card-filter"]', 10000, 'AC8: card filter');
      browser.waitForElementPresent('[data-testid="compatibility-status-filter"]', 10000, 'AC8: status filter');
      browser.waitForElementPresent('[data-testid="compatibility-search"]', 10000, 'AC8: search');
      browser.waitForElementPresent('[data-testid="compatibility-grid"]', 15000, 'AC8: grid renders');
      browser.waitForElementPresent('[data-testid="compatibility-last-updated"]', 10000, 'AC8: last-updated');
    });
  },

  'AC9: grid cell click opens edit dialog; saving updates in place': function (browser) {
    const org = browser.globals.orgA;
    seedInventory(browser);
    createModel(browser, org, `e2e-compat-edit-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/admin/compatibility');
      browser.waitForElementPresent('[data-testid="compatibility-page"]', 15000, 'AC9: page renders');
      // The grid defaults to the first model in the dimensions list; select
      // the freshly created model so its cells are shown.
      browser.waitForElementPresent('[data-testid="compatibility-model-filter"]', 10000, 'AC9: model filter');
      browser.click(`[data-testid="compatibility-model-filter"] option[value="${modelId}"]`);
      browser.pause(1500);
      // Click the vllm/A800 cell for the selected model.
      browser.waitForElementPresent(`[data-testid="compatibility-cell-${modelId}-vllm-A800"]`, 15000, 'AC9: cell present');
      browser.click(`[data-testid="compatibility-cell-${modelId}-vllm-A800"]`);
      browser.waitForElementPresent('[data-testid="compatibility-edit-dialog"]', 10000, 'AC9: edit dialog opens');
      browser.waitForElementPresent('[data-testid="compatibility-edit-status"]', 10000, 'AC9: status radios');
      browser.waitForElementPresent('[data-testid="compatibility-edit-note"]', 10000, 'AC9: note textarea');
      // Select "supported" and save.
      browser.click('[data-testid="compatibility-edit-status"] input[value="supported"]');
      browser.setValue('[data-testid="compatibility-edit-note"]', 'e2e validated');
      browser.click('[data-testid="compatibility-edit-save"]');
      browser.waitForElementNotPresent('[data-testid="compatibility-edit-dialog"]', 10000, 'AC9: dialog closes');
      // The cell should now show Supported.
      browser.waitForElementPresent(`[data-testid="compatibility-cell-${modelId}-vllm-A800"]`, 10000, 'AC9: cell still present');
      browser.assert.containsText(
        `[data-testid="compatibility-cell-${modelId}-vllm-A800"]`,
        'Supported',
        'AC9: cell updated to Supported'
      );
    });
  },

  'AC9: table view bulk selection + bulk edit updates N cells': function (browser) {
    const org = browser.globals.orgA;
    seedInventory(browser);
    createModel(browser, org, `e2e-compat-bulkedit-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/admin/compatibility');
      browser.waitForElementPresent('[data-testid="compatibility-page"]', 15000, 'AC9: page renders');
      // Filter to the freshly created model so its rows are on page 1.
      browser.waitForElementPresent('[data-testid="compatibility-model-filter"]', 10000, 'AC9: model filter');
      browser.click(`[data-testid="compatibility-model-filter"] option[value="${modelId}"]`);
      browser.pause(1500);
      browser.click('[data-testid="compatibility-view-table"]');
      browser.waitForElementPresent('[data-testid="compatibility-table"]', 15000, 'AC9: table view');
      // Select two rows.
      browser.waitForElementPresent(`[data-testid="compatibility-row-checkbox-${modelId}-vllm-A800"]`, 15000, 'AC9: row checkbox A800');
      browser.click(`[data-testid="compatibility-row-checkbox-${modelId}-vllm-A800"]`);
      browser.waitForElementPresent(`[data-testid="compatibility-row-checkbox-${modelId}-vllm-H800"]`, 15000, 'AC9: row checkbox H800');
      browser.click(`[data-testid="compatibility-row-checkbox-${modelId}-vllm-H800"]`);
      // Bulk edit button enabled.
      browser.waitForElementPresent('[data-testid="compatibility-bulk-edit"]', 10000, 'AC9: bulk edit button');
      browser.click('[data-testid="compatibility-bulk-edit"]');
      browser.waitForElementPresent('[data-testid="compatibility-bulk-dialog"]', 10000, 'AC9: bulk dialog opens');
      browser.click('[data-testid="compatibility-bulk-status"] input[value="supported"]');
      browser.click('[data-testid="compatibility-bulk-apply"]');
      browser.waitForElementNotPresent('[data-testid="compatibility-bulk-dialog"]', 10000, 'AC9: bulk dialog closes');
      // Both rows should now show Supported.
      browser.waitForElementPresent(`[data-testid="compatibility-row-${modelId}-vllm-A800"]`, 10000, 'AC9: A800 row');
      browser.assert.containsText(
        `[data-testid="compatibility-row-${modelId}-vllm-A800"]`,
        'Supported',
        'AC9: A800 row updated'
      );
      browser.assert.containsText(
        `[data-testid="compatibility-row-${modelId}-vllm-H800"]`,
        'Supported',
        'AC9: H800 row updated'
      );
    });
  },

  'AC10: not-in-fleet tag renders on cells whose card type left the fleet': function (browser) {
    const org = browser.globals.orgA;
    // Seed with A800 present, create a model, and curate an A800 cell so it
    // is stored in the matrix. Then seed with A800 omitted so the stored
    // A800 cell is flagged not_in_fleet (AC6/AC10). The table view renders
    // the stored cell with the "not in fleet" flag.
    seedInventory(browser);
    createModel(browser, org, `e2e-compat-nif-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/compatibility/${modelId}/vllm/A800`,
        org,
        body: {status: 'supported', note: 'validated'}
      }, () => {
        seedInventory(browser, 'A800');
        browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
        browser.url(browser.globals.baseUrl + '/admin/compatibility');
        browser.waitForElementPresent('[data-testid="compatibility-page"]', 15000, 'AC10: page renders');
        // Filter to the freshly created model so its rows are on page 1.
        browser.waitForElementPresent('[data-testid="compatibility-model-filter"]', 10000, 'AC10: model filter');
        browser.click(`[data-testid="compatibility-model-filter"] option[value="${modelId}"]`);
        browser.pause(1500);
        browser.click('[data-testid="compatibility-view-table"]');
        browser.waitForElementPresent('[data-testid="compatibility-table"]', 15000, 'AC10: table view');
        // The stored A800 row carries the "not in fleet" flag.
        browser.waitForElementPresent(
          `[data-testid="compatibility-row-${modelId}-vllm-A800"]`,
          15000,
          'AC10: A800 row present'
        );
        browser.assert.containsText(
          `[data-testid="compatibility-row-${modelId}-vllm-A800"]`,
          'not in fleet',
          'AC10: A800 row flagged not in fleet'
        );
      });
    });
  },

  // ---- End-user UI (AC12/AC14) ----

  'AC12: user model detail shows supported/experimental and hides unsupported': function (browser) {
    const org = browser.globals.orgA;
    seedInventory(browser);
    createModel(browser, org, `e2e-compat-user-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      // Curate one supported cell so the masked projection has content.
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/compatibility/${modelId}/vllm/A800`,
        org,
        body: {status: 'supported', note: 'validated'}
      }, () => {
        browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
        browser.url(browser.globals.baseUrl + `/models/${modelId}`);
        browser.waitForElementPresent('[data-testid="model-detail-page"]', 15000, 'AC12: model detail page');
        browser.waitForElementPresent('[data-testid="model-detail-compatibility"]', 10000, 'AC12: compatibility section');
        browser.waitForElementPresent('[data-testid="model-detail-compatibility-table"]', 10000, 'AC12: compatibility table');
        browser.waitForElementPresent('[data-testid="model-detail-compatibility-summary"]', 10000, 'AC12: summary');
        // The curated supported cell is visible.
        browser.waitForElementPresent('[data-testid="model-detail-compat-row-vllm-A800"]', 10000, 'AC12: vllm/A800 row');
        browser.assert.containsText(
          '[data-testid="model-detail-compat-row-vllm-A800"]',
          'Supported',
          'AC12: vllm/A800 shows Supported'
        );
      });
    });
  }
};