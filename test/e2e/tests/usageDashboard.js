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

// Feature-09 (usage dashboard & per-request cost attribution) e2e
// suite, run against the compose stack gateway. Covers the acceptance
// criteria of docs/design/usage-dashboard.md reachable from the
// outside: the dashboard API's empty state and range validation (AC7),
// the Usage page rendering the cards, chart, metric toggle, group-by
// and balance widget (AC1/AC3/AC6), and the balance widget's no-account
// vs funded states. Charge records are produced by the internal
// charging pipeline (covered by the FVT suite), so a fresh org shows
// the empty dashboard; the widget is exercised by creating an account.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['usage-dashboard', 'feature-09'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-dash-${browser.globals.runId}-${browser.globals.testSeq}`;
    // The org-scoped APIs validate the org header against the
    // organizations table, so the org id must exist first.
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed an admin-realm session so the protected pages render (feature:
    // unauthenticated pages redirect to login).
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC9 (surface separation): user usage page and API use the user surface': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/metering/usage-dashboard',
      org: browser.globals.orgA,
      sessionRealm: 'user'
    }, (res) => {
      api.assertOk(browser, res, 'user usage dashboard');
    });
    browser.url(browser.globals.baseUrl + '/usage');
    browser.waitForElementPresent('[data-testid="usage-dashboard-cards"]', 10000, 'AC9: user usage page renders');
    browser.waitForElementPresent(
      '[data-testid="usage-empty"], [data-testid="usage-table"]',
      10000,
      'AC9: user usage data state renders'
    );
  },

  'AC9 (surface separation): a user session cannot call admin usage APIs': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/metering/usage-dashboard',
      org: browser.globals.orgA,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC9: wrong realm rejected');
    });
  },

  'AC1: usage page renders dashboard cards, chart and metric toggle': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/usage');

    // The dashboard cards row renders.
    browser.waitForElementPresent(
      '[data-testid="usage-dashboard-cards"]',
      10000,
      'AC1: dashboard cards render'
    );
    // The chart renders (empty state for a fresh org).
    browser.waitForElementPresent(
      '[data-testid="usage-chart"]',
      10000,
      'AC1: chart renders'
    );
    // The metric toggle renders.
    browser.waitForElementPresent(
      '[data-testid="usage-metric-toggle"]',
      5000,
      'AC1: metric toggle renders'
    );
    browser.waitForElementPresent(
      '[data-testid="usage-metric-cost"]',
      5000,
      'AC1: cost metric button renders'
    );
    browser.waitForElementPresent(
      '[data-testid="usage-metric-tokens"]',
      5000,
      'AC1: tokens metric button renders'
    );
    browser.waitForElementPresent(
      '[data-testid="usage-metric-requests"]',
      5000,
      'AC1: requests metric button renders'
    );
    // The group-by select renders.
    browser.waitForElementPresent(
      '[data-testid="usage-groupby-select"]',
      5000,
      'AC1: group-by select renders'
    );
    // The balance widget renders (no-account state for a fresh org).
    browser.waitForElementPresent(
      '[data-testid="usage-balance-widget"]',
      10000,
      'AC1: balance widget renders'
    );
  },

  'AC3: metric toggle switches without refetching': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/usage');
    browser.waitForElementPresent('[data-testid="usage-metric-toggle"]', 10000, 'AC3: toggle renders');

    // Click the tokens metric; the chart stays present (client-side switch).
    browser.click('[data-testid="usage-metric-tokens"]');
    browser.waitForElementPresent('[data-testid="usage-chart"]', 5000, 'AC3: chart persists after toggle');
  },

  'AC6: balance widget shows funded state after account creation': function (browser) {
    const org = browser.globals.orgA;

    // Create a prepaid account for the org.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'prepaid', overdrawPolicy: 'block', initialBalanceCents: 10000}
    }, (res) => {
      api.assertOk(browser, res, 'create account for widget');
    });

    browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/usage');
    browser.waitForElementPresent('[data-testid="usage-balance-widget"]', 10000, 'AC6: widget renders');
    // The widget shows the balance (not the muted no-account state).
    browser.assert.not.containsText(
      '[data-testid="usage-balance-widget"]',
      'No billing account',
      'AC6: widget shows funded balance'
    );
  },

  'AC7: dashboard API validates ranges and answers empty': function (browser) {
    const org = browser.globals.orgA;
    const now = Math.floor(Date.now() / 1000);

    // Default range: empty cards/buckets for a fresh org.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/metering/usage-dashboard',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'default dashboard');
      browser.assert.ok(
        body.cards && body.cards.totalCostCents !== undefined,
        'AC7: dashboard returns cards'
      );
      browser.assert.ok(
        Array.isArray(body.dailyBuckets),
        'AC7: dashboard returns dailyBuckets array'
      );
    });

    // Inverted range -> 10404.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/metering/usage-dashboard?since=${now}&until=${now - 10}`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10404, 'AC7: inverted range');
    });

    // Range over 92 days -> 10404.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/metering/usage-dashboard?since=${now - 93 * 24 * 3600}&until=${now}`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10404, 'AC7: over-long range');
    });
  },

  'AC5: export CSV button renders': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/usage');
    browser.waitForElementPresent(
      '[data-testid="usage-export-csv"]',
      10000,
      'AC5: export CSV button renders'
    );
  }
};