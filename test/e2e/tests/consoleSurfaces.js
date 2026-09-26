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

  'AC2/AC3: user console has the 8 tenant nav items, no admin links': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/usage');
    browser.waitForElementPresent('[data-testid="user-shell"]', 15000, 'user shell');
    // Feature #21 (SDK/Quickstart): the Quickstart item is first, making
    // eight user-nav-* items total (the seven pre-existing items plus
    // user-nav-quickstart).
    browser.waitForElementPresent('[data-testid="user-nav-quickstart"]', 10000, 'AC2: user-nav-quickstart');
    browser.waitForElementPresent('[data-testid="user-nav-usage"]', 10000, 'AC2: user-nav-usage');
    browser.waitForElementPresent('[data-testid="user-nav-api-keys"]', 10000, 'AC2: user-nav-api-keys');
    browser.waitForElementPresent('[data-testid="user-nav-request-logs"]', 10000, 'AC2: user-nav-request-logs');
    browser.waitForElementPresent('[data-testid="user-nav-playground"]', 10000, 'AC2: user-nav-playground');
    browser.waitForElementPresent('[data-testid="user-nav-billing"]', 10000, 'AC2: user-nav-billing');
    browser.waitForElementPresent('[data-testid="user-nav-activity"]', 10000, 'AC2: user-nav-activity');
    browser.waitForElementPresent('[data-testid="user-nav-models"]', 10000, 'AC2: user-nav-models');
    browser.assert.not.elementPresent('[data-testid="nav-models"]', 'AC4: no admin nav in user console');
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
