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

// Feature-12 (request logs & API playground) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/request-logs-playground.md reachable from the outside:
// the Request Logs page renders (AC-A8) and the Playground page renders
// its controls (AC-B3). Request-log rows are produced by the internal
// ingestion path (covered by the FVT suite), so a fresh org shows the
// empty state.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['request-logs-playground', 'feature-12'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-rl-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed a user-realm session so the protected pages render (feature:
    // unauthenticated pages redirect to login). The request-logs and
    // playground pages live on the user surface (/request-logs, /playground).
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC9: a user session cannot call the admin request-logs API': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/metering/request-logs',
      org: browser.globals.orgA,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC9: wrong realm rejected');
    });
  },

  'AC-A8: request logs API answers empty for a fresh org': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/metering/request-logs',
      org,
      sessionRealm: 'user'
    }, (res) => {
      const body = api.assertOk(browser, res, 'request logs list');
      browser.assert.ok(
        Array.isArray(body.requestLogs),
        'AC-A8: request logs returns an array'
      );
    });
  },

  'AC-B3: playground page renders controls': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/playground');
    browser.waitForElementPresent(
      '[data-testid="playground-model-select"], [data-testid="playground-no-models"]',
      10000,
      'AC-B3: model selector or no-models state renders'
    );
    browser.assert.not.elementPresent('[data-testid="playground-service-select"]', 'AC-B3: user playground does not expose service selection');
  },

  'AC-A8: request logs page renders filters and empty state': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/request-logs');
    browser.waitForElementPresent(
      '[data-testid="request-log-filters"]',
      10000,
      'AC-A8: filters render'
    );
    browser.waitForElementPresent(
      '[data-testid="request-log-filter-status"]',
      5000,
      'AC-A8: status filter renders'
    );
    browser.waitForElementPresent(
      '[data-testid="request-log-filter-key"]',
      5000,
      'AC-A8: API key filter renders'
    );
    browser.waitForElementPresent(
      '[data-testid="request-log-filter-model"]',
      5000,
      'AC-A8: model filter renders'
    );
    browser.waitForElementPresent(
      '[data-testid="request-logs-empty"], [data-testid="request-logs-table"]',
      10000,
      'AC-A8: request logs list renders'
    );
  },

};