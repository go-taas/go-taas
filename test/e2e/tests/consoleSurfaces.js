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

// Feature-17 (console surface separation) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/console-surface-separation.md reachable from the outside:
// the two route trees (AC1-AC3), the moved-URL redirects (AC16), the
// user console pages (AC17), and the surface separation at the API level
// (AC5/AC6).

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['console-surfaces', 'feature-17'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/usage');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-cs-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed a user-realm session so the protected pages render (feature:
    // unauthenticated pages redirect to login).
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC1: / redirects to the user home (/usage)': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/');
    browser.pause(2000);
    browser.assert.urlContains('/usage', 'AC1: / redirects to /usage');
  },

  'AC2/AC3: user console has the tenant nav categories, no admin links': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/usage');
    browser.waitForElementPresent('[data-testid="user-shell"]', 15000, 'user shell');
    // Navigation is grouped into categories (Aurora redesign): the top bar
    // carries the categories, the left sidebar carries the active
    // category's items. On /usage the Usage & Cost category is active.
    browser.waitForElementPresent('[data-testid="user-nav-cat-start"]', 10000, 'AC2: start category');
    browser.waitForElementPresent('[data-testid="user-nav-cat-usage"]', 10000, 'AC2: usage category');
    browser.waitForElementPresent('[data-testid="user-nav-cat-api"]', 10000, 'AC2: api category');
    browser.waitForElementPresent('[data-testid="user-nav-cat-workloads"]', 10000, 'AC2: workloads category');
    browser.waitForElementPresent('[data-testid="user-nav-cat-billing"]', 10000, 'AC2: billing category');
    browser.waitForElementPresent('[data-testid="user-nav-cat-account"]', 10000, 'AC2: account category');
    // The active category's items are in the sidebar.
    browser.waitForElementPresent('[data-testid="user-nav-usage"]', 10000, 'AC2: user-nav-usage');
    browser.waitForElementPresent('[data-testid="user-nav-usage-keys"]', 10000, 'AC2: user-nav-usage-keys');
    browser.waitForElementPresent('[data-testid="user-nav-cost"]', 10000, 'AC2: user-nav-cost');
    browser.assert.not.elementPresent('[data-testid="nav-models"]', 'AC4: no admin nav in user console');
    browser.assert.not.elementPresent('[data-testid="nav-cat-access"]', 'AC4: no admin categories in user console');
  },

  'AC16: moved admin URLs redirect to the user console': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/api-keys');
    browser.pause(2000);
    browser.assert.urlContains('/api-keys', 'AC16: /admin/api-keys → /api-keys');
  },

  'AC17: user usage page renders': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/usage');
    browser.waitForElementPresent('[data-testid="usage-dashboard-cards"]', 15000, 'AC17: usage page renders');
  },

  'AC5/AC6: user-prefix API is reachable, admin-prefix is separate': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/auth/api-keys?page.limit=5',
      org: browser.globals.orgA
    }, (res) => {
      api.assertOk(browser, res, 'AC5: user-prefix api-keys reachable');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/models?page.limit=5',
      org: browser.globals.orgA
    }, (res) => {
      api.assertOk(browser, res, 'AC5: user-prefix models reachable');
    });
  }
};
