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

// Feature-05 (pricing) e2e suite, run against the compose stack
// gateway. Covers the acceptance criteria of docs/design/pricing.md
// reachable from the outside: the Pricing page structure and set-price
// dialog (AC14), the Bills page structure and drill-down entry points
// (AC15), plus the price APIs' validation (AC2) and the bills/charges
// queries' empty state and range validation (AC12). Charging happens
// via the internal MQ surface, so a fresh compose stack legitimately
// shows empty states; the charging path is covered by the FVT suite.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['pricing', 'feature-05'],

  before(browser) {
    // Nothing: each test navigates in beforeEach because afterEach ends
    // the session and a fresh page starts at about:blank, where a
    // cross-origin fetch to the gateway would fail (null origin).
  },

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.orgA = `org-e2e-${browser.globals.runId}`;
    // Feature #6: the org-scoped APIs validate the org header against the
    // organizations table, so the org id must exist first.
    api.ensureOrg(browser, browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC14: pricing page renders matrix, toggle and set-price dialog': function (browser) {
    browser.url(browser.globals.baseUrl + '/admin/pricing');
    browser.waitForElementPresent('[data-testid="pricing-set-button"]', 10000, 'AC14: set-price button renders');
    browser.waitForElementPresent('[data-testid="pricing-view-toggle"]', 5000, 'AC14: view toggle renders');
    browser.waitForElementPresent('[data-testid="pricing-view-current"]', 5000, 'AC14: current view toggle renders');
    browser.waitForElementPresent('[data-testid="pricing-view-history"]', 5000, 'AC14: history view toggle renders');

    // A fresh stack has no prices: the empty state shows.
    browser.waitForElementPresent('[data-testid="pricing-empty"]', 10000, 'AC14: empty state renders');

    // The set-price dialog opens with the tiers editor.
    browser.click('[data-testid="pricing-set-button"]');
    browser.waitForElementPresent('[data-testid="set-price-dialog"]', 5000, 'AC14: set-price dialog opens');
    browser.waitForElementPresent('[data-testid="set-price-model"]', 5000, 'AC14: model field renders');
    browser.waitForElementPresent('[data-testid="set-price-card"]', 5000, 'AC14: card field renders');
    browser.waitForElementPresent('[data-testid="set-price-input-rate"]', 5000, 'AC14: input rate field renders');
    browser.waitForElementPresent('[data-testid="set-price-output-rate"]', 5000, 'AC14: output rate field renders');
    browser.waitForElementPresent('[data-testid="tier-add"]', 5000, 'AC14: tiers editor renders');
  },

  'AC14: set-price dialog validates input inline (10507)': function (browser) {
    const org = browser.globals.orgA;

    browser.url(browser.globals.baseUrl + '/admin/pricing');
    browser.waitForElementPresent('[data-testid="pricing-set-button"]', 10000);

    // Open the dialog and submit an invalid price (negative rate).
    browser.click('[data-testid="pricing-set-button"]');
    browser.waitForElementPresent('[data-testid="set-price-dialog"]', 5000);
    browser.setValue('[data-testid="set-price-model"]', `model-e2e-${browser.globals.runId}`);
    browser.setValue('[data-testid="set-price-card"]', 'A800');
    browser.setValue('[data-testid="set-price-input-rate"]', '-1');
    browser.setValue('[data-testid="set-price-output-rate"]', '2');
    browser.click('[data-testid="set-price-save"]');

    // AC2: the inline error banner shows the 10507 message.
    browser.waitForElementPresent('[data-testid="error-banner"]', 10000, 'AC14: inline 10507 error renders');
  },

  'AC2: price API validates the matrix': function (browser) {
    const org = browser.globals.orgA;

    // An empty model id is rejected with 10507.
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/billing/prices',
      org,
      body: {
        modelId: '',
        acceleratorType: 'A800',
        inputPricePerMillion: 1,
        outputPricePerMillion: 2
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10507, 'AC2: empty model id');
    });

    // A negative rate is rejected with 10507.
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/billing/prices',
      org,
      body: {
        modelId: `model-e2e-${browser.globals.runId}`,
        acceleratorType: 'A800',
        inputPricePerMillion: -1,
        outputPricePerMillion: 2
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10507, 'AC2: negative rate');
    });

    // A bounded last tier is rejected with 10507.
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/billing/prices',
      org,
      body: {
        modelId: `model-e2e-${browser.globals.runId}`,
        acceleratorType: 'A800',
        inputPricePerMillion: 1,
        outputPricePerMillion: 2,
        tiers: [{upToTokens: 1000000, inputPricePerMillion: 1, outputPricePerMillion: 2}]
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10507, 'AC2: bounded last tier');
    });
  },

  'AC1/FR2: price round-trip through the API': function (browser) {
    const org = browser.globals.orgA;
    const model = `model-e2e-${browser.globals.runId}`;
    const price = {
      modelId: model,
      acceleratorType: 'A800',
      inputPricePerMillion: 4,
      outputPricePerMillion: 8,
      cachedPricePerMillion: 0.5,
      tiers: [
        {upToTokens: 1000000, inputPricePerMillion: 4, outputPricePerMillion: 8},
        {upToTokens: 0, inputPricePerMillion: 2, outputPricePerMillion: 4}
      ]
    };

    // Set the price; a price_id comes back.
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/billing/prices',
      org,
      body: price
    }, (res) => {
      const body = api.assertOk(browser, res, 'set price');
      browser.assert.ok(
        body.priceId && body.priceId.length > 0,
        'AC1: price_id returned'
      );
    });

    // A repeated save keeps the same price_id (idempotent cell version).
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/billing/prices',
      org,
      body: price
    }, (res) => {
      const body = api.assertOk(browser, res, 'repeat save');
      browser.assert.ok(
        body.priceId && body.priceId.length > 0,
        'AC1: repeated save returns the price_id'
      );
    });

    // The matrix lists the cell with its tiers.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/billing/prices?model_id=${encodeURIComponent(model)}`,
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'list prices');
      browser.assert.ok(
        Array.isArray(body.prices) && body.prices.length === 1,
        'FR2: the matrix lists the cell'
      );
      const entry = body.prices[0];
      browser.assert.equal(entry.modelId, model, 'FR2: model round-trips');
      browser.assert.equal(entry.acceleratorType, 'A800', 'FR2: card round-trips');
      browser.assert.equal(entry.inputPricePerMillion, 4, 'FR2: input rate round-trips');
      browser.assert.equal(entry.outputPricePerMillion, 8, 'FR2: output rate round-trips');
      browser.assert.ok(
        Array.isArray(entry.tiers) && entry.tiers.length === 2,
        'FR2: tiers round-trip'
      );
      browser.assert.equal(
        String(entry.tiers[0].upToTokens),
        '1000000',
        'FR2: tier bound round-trips'
      );
    });

    // The pricing page now renders the row.
    browser.url(browser.globals.baseUrl + '/admin/pricing');
    browser.waitForElementPresent(
      `[data-testid="pricing-row-${model}-A800"]`,
      10000,
      'AC14: the saved price renders in the matrix'
    );
  },

  'AC15: bills page renders table and empty state': function (browser) {
    browser.url(browser.globals.baseUrl + '/admin/billing');
    browser.waitForElementPresent('[data-testid="bills-table"], [data-testid="bills-empty"]', 10000, 'AC15: bills page renders');
  },

  'AC12: bills and charges queries validate ranges and answer empty': function (browser) {
    const org = browser.globals.orgA;
    const now = Math.floor(Date.now() / 1000);

    // A fresh organization has no bills.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/billing/bills',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'bills list');
      browser.assert.deepEqual(
        body.bills,
        [],
        'AC12: fresh organization has no bills'
      );
    });

    // The charges list is empty too.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/billing/charges',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'charges list');
      browser.assert.deepEqual(
        body.charges,
        [],
        'AC12: fresh organization has no charges'
      );
    });

    // An inverted range is rejected with 10508.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/billing/charges?since=${now}&until=${now - 10}`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10508, 'AC12: inverted range');
    });

    // A range over 366 days is rejected with 10508.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/billing/bills?since=${now - 367 * 24 * 3600}&until=${now}`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10508, 'AC12: range over 366 days');
    });
  },

  'AC16: billing queries require the organization header': function (browser) {
    // No X-Organization-Id header: unauthorized (10001).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/billing/charges'
    }, (res) => {
      api.assertBusinessError(browser, res, 10001, 'AC16: missing org header');
    });
  }
};
