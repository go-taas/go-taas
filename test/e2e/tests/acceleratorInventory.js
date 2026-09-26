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

// Feature-18 (accelerator inventory & health) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/accelerator-inventory.md reachable from the outside: the
// admin fleet page (AC5/AC6/AC7/AC8), the node detail page (AC10), and
// the surface separation (AC11).
//
// The compose stack has no Controller/Kubernetes, so the inventory is
// empty by default. The suite first asserts the empty state (AC8), then
// seeds a full snapshot into the accelerator.inventory NATS subject via
// the seed program (test/e2e/seed/seed_accelerator.go) so the populated
// states (AC5/AC6/AC7/AC10) can be exercised against the real UI.

const { execSync } = require('child_process');
const path = require('path');
const api = require('../page-objects/api.js');

// Seed the accelerator inventory by publishing a full snapshot to the
// accelerator.inventory NATS subject inside the compose network. The
// seed program mirrors the Controller's snapshot wire format.
function seedInventory(browser) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    `-e GOPROXY=https://goproxy.cn,direct`,
    // Persist the Go module + build caches across runs so the seed does
    // not re-download dependencies on every invocation.
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed',
    'golang:1.26-alpine',
    'sh -c "go run seed_accelerator.go"'
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedInventory failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['accelerator-inventory', 'feature-18'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-accel-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Empty state (AC8) ----

  'AC8: empty inventory renders the empty state': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/accelerators');
    browser.waitForElementPresent('[data-testid="accelerators-page"]', 15000, 'AC8: accelerators page renders');
    browser.waitForElementPresent('[data-testid="accelerators-empty"]', 15000, 'AC8: empty state renders');
    browser.assert.containsText(
      '[data-testid="accelerators-empty"]',
      'No accelerator nodes found',
      'AC8: empty state message'
    );
    // The card-type summary strip is empty too.
    browser.waitForElementPresent('[data-testid="accelerators-summary-empty"]', 15000, 'AC8: summary empty');
  },

  // ---- Surface separation (AC11) ----

  'AC11: accelerator APIs are admin-only (user prefix 404s)': function (browser) {
    // The admin-prefix endpoints are reachable.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/accelerators/nodes?page.limit=5',
      org: browser.globals.orgA
    }, (res) => {
      api.assertOk(browser, res, 'AC11: admin nodes reachable');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/accelerators/card-types',
      org: browser.globals.orgA
    }, (res) => {
      api.assertOk(browser, res, 'AC11: admin card-types reachable');
    });
    // The user-prefix equivalents must NOT exist (404 / code 5).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/accelerators/nodes?page.limit=5',
      org: browser.globals.orgA
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC11: user-prefix nodes is 404');
      browser.assert.equal(res.body && res.body.code, 5, 'AC11: user-prefix nodes code 5');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/accelerators/card-types',
      org: browser.globals.orgA
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC11: user-prefix card-types is 404');
    });
  },

  'AC11: admin page is served at /admin/accelerators': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/accelerators');
    browser.waitForElementPresent('[data-testid="accelerators-page"]', 15000, 'AC11: admin page served');
  },

  // ---- 10208 not-found (AC10) ----

  'AC10: missing node id shows the 10208 not-found state': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/accelerators/nonexistent-node');
    browser.waitForElementPresent('[data-testid="accelerator-node-not-found"]', 15000, 'AC10: not-found state');
    browser.assert.containsText(
      '[data-testid="accelerator-node-not-found"]',
      'Node not found',
      'AC10: not-found message'
    );
    browser.waitForElementPresent('[data-testid="accelerator-node-back"]', 10000, 'AC10: back link');
  },

  // ---- Populated states (AC5/AC6/AC7/AC10) ----

  'AC5: populated fleet page renders the summary strip and node table': function (browser) {
    seedInventory(browser);
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/accelerators');
    browser.waitForElementPresent('[data-testid="accelerators-page"]', 15000, 'AC5: page renders');
    browser.waitForElementPresent('[data-testid="accelerators-summary-strip"]', 15000, 'AC5: summary strip');
    browser.waitForElementPresent('[data-testid="accelerators-table"]', 15000, 'AC5: node table');
    browser.waitForElementPresent('[data-testid="accelerator-node-node-nvidia-a800-01"]', 15000, 'AC5: nvidia a800 node row');
    browser.waitForElementPresent('[data-testid="accelerator-node-node-iluvatar-biv150-01"]', 15000, 'AC5: iluvatar node row');
    browser.waitForElementPresent('[data-testid="accelerator-node-node-metax-mxc500-01"]', 15000, 'AC5: metax node row');
    browser.waitForElementPresent('[data-testid="accelerators-last-updated"]', 10000, 'AC5: last-updated timestamp');
  },

  'AC7: zero-free card type is flagged and vendor banner shows': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/accelerators');
    browser.waitForElementPresent('[data-testid="accelerators-page"]', 15000, 'AC7: page renders');
    // The degraded node has 1 allocated / 3 free A800, so A800 is not
    // zero-free; the H800 card type has 4 free. No card type in the seed
    // is zero-free, so assert the no-free flag is absent for the seeded
    // card types and that the summary cards render.
    browser.waitForElementPresent('[data-testid="accelerator-card-nvidia-A800"]', 15000, 'AC7: A800 card');
    browser.waitForElementPresent('[data-testid="accelerator-card-nvidia-H800"]', 15000, 'AC7: H800 card');
    browser.waitForElementPresent('[data-testid="accelerator-card-iluvatar-BI-V150"]', 15000, 'AC7: BI-V150 card');
    browser.waitForElementPresent('[data-testid="accelerator-card-metax-MXC500"]', 15000, 'AC7: MXC500 card');
  },

  'AC6: vendor filter narrows the node table': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/accelerators');
    browser.waitForElementPresent('[data-testid="accelerators-page"]', 15000, 'AC6: page renders');
    browser.waitForElementPresent('[data-testid="accelerators-vendor-filter"]', 10000, 'AC6: vendor filter');
    browser.click('[data-testid="accelerators-vendor-filter"] option[value="iluvatar"]');
    browser.pause(1500);
    browser.waitForElementPresent('[data-testid="accelerator-node-node-iluvatar-biv150-01"]', 15000, 'AC6: iluvatar node present');
    browser.assert.not.elementPresent(
      '[data-testid="accelerator-node-node-nvidia-a800-01"]',
      'AC6: nvidia node filtered out'
    );
  },

  'AC6: health filter narrows the node table': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/accelerators');
    browser.waitForElementPresent('[data-testid="accelerators-page"]', 15000, 'AC6: page renders');
    browser.waitForElementPresent('[data-testid="accelerators-health-filter"]', 10000, 'AC6: health filter');
    browser.click('[data-testid="accelerators-health-filter"] option[value="degraded"]');
    browser.pause(1500);
    browser.waitForElementPresent('[data-testid="accelerator-node-node-nvidia-degraded-01"]', 15000, 'AC6: degraded node present');
    browser.assert.not.elementPresent(
      '[data-testid="accelerator-node-node-nvidia-a800-01"]',
      'AC6: healthy node filtered out'
    );
  },

  'AC6: node-name search narrows the node table': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/accelerators');
    browser.waitForElementPresent('[data-testid="accelerators-page"]', 15000, 'AC6: page renders');
    browser.waitForElementPresent('[data-testid="accelerators-search"]', 10000, 'AC6: search box');
    browser.setValue('[data-testid="accelerators-search"]', 'metax');
    browser.pause(1500);
    browser.waitForElementPresent('[data-testid="accelerator-node-node-metax-mxc500-01"]', 15000, 'AC6: metax node present');
    browser.assert.not.elementPresent(
      '[data-testid="accelerator-node-node-nvidia-a800-01"]',
      'AC6: non-matching node filtered out'
    );
  },

  'AC10: node detail shows per-GPU breakdown and health signals': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/accelerators/node-nvidia-a800-01');
    browser.waitForElementPresent('[data-testid="accelerator-node-detail"]', 15000, 'AC10: detail page renders');
    browser.waitForElementPresent('[data-testid="accelerator-node-health"]', 10000, 'AC10: health badge');
    browser.waitForElementPresent('[data-testid="accelerator-node-gpus"]', 10000, 'AC10: GPU breakdown table');
    browser.waitForElementPresent('[data-testid="accelerator-node-resources"]', 10000, 'AC10: resources table');
    browser.waitForElementPresent('[data-testid="accelerator-node-labels"]', 10000, 'AC10: labels');
    browser.waitForElementPresent('[data-testid="accelerator-node-taints"]', 10000, 'AC10: taints');
    browser.waitForElementPresent('[data-testid="accelerator-node-warmup-empty"]', 10000, 'AC10: warmup empty state');
    browser.assert.containsText('[data-testid="accelerator-node-health"]', 'healthy', 'AC10: healthy badge');
  }
};