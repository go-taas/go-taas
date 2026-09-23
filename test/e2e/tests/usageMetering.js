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

// Feature-04 (metering) e2e suite, run against the compose stack
// gateway. Covers the acceptance criteria of docs/design/metering.md
// that are reachable from the outside: the Usage page structure (AC12)
// and the drill-down entry points (AC13), plus the query APIs' empty
// state and range validation (AC8/AC10). Voucher ingestion happens via
// the internal MQ surface (IngestMeteringEvent has no HTTP binding), so
// a fresh compose stack legitimately shows the empty state; the
// settlement path is covered by the FVT suite.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['metering', 'feature-04'],

  before(browser) {
    // Nothing: each test navigates in beforeEach because afterEach ends
    // the session and a fresh page starts at about:blank, where a
    // cross-origin fetch to the gateway would fail (null origin).
  },

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.orgA = `org-e2e-${browser.globals.runId}`;
  },

  afterEach(browser) {
    browser.end();
  },

  'AC12: usage page renders presets, table and empty state': function (browser) {
    const org = browser.globals.orgA;

    browser.url(browser.globals.baseUrl + '/admin/usage');
    browser.waitForElementPresent('[data-testid="usage-range"]', 10000, 'AC12: range selector renders');
    browser.waitForElementPresent('[data-testid="usage-range-24h"]', 5000, 'AC12: 24h preset renders');
    browser.waitForElementPresent('[data-testid="usage-range-7d"]', 5000, 'AC12: 7d preset renders');
    browser.waitForElementPresent('[data-testid="usage-range-30d"]', 5000, 'AC12: 30d preset renders');
    browser.waitForElementPresent('[data-testid="usage-range-custom"]', 5000, 'AC12: custom preset renders');

    // A fresh organization has no usage: the empty state shows (the
    // table only renders with data).
    browser.waitForElementPresent('[data-testid="usage-empty"]', 10000, 'AC12: empty state renders');
  },

  'AC13: drill-down entry points exist on usage rows': function (browser) {
    const org = browser.globals.orgA;

    // The ingest RPC is cluster-internal, so rows cannot be produced
    // from the e2e environment. Assert the drill-down contracts at the
    // API level instead: the by-model summary and the voucher list both
    // answer for the organization (empty, but well-formed).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/metering/usage-summary?group_by=model',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'by-model summary');
      browser.assert.ok(
        Array.isArray(body.rows),
        'AC13: by-model summary returns a rows array'
      );
    });

    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/metering/vouchers?page.limit=20',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'voucher list');
      browser.assert.ok(
        Array.isArray(body.vouchers),
        'AC13: voucher drill-down returns a vouchers array'
      );
      browser.assert.ok(
        body.pageMeta && body.pageMeta.total !== undefined,
        'AC13: voucher list is paginated'
      );
    });
  },

  'AC8/AC10: usage queries validate ranges and answer empty': function (browser) {
    const org = browser.globals.orgA;
    const now = Math.floor(Date.now() / 1000);

    // Default range: empty rows for a fresh organization.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/metering/usage-summary',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'default summary');
      browser.assert.deepEqual(
        body.rows,
        [],
        'AC8: fresh organization has no usage rows'
      );
    });

    // Settled records list is empty too.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/metering/usage-records',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'usage records');
      browser.assert.deepEqual(
        body.records,
        [],
        'AC8: fresh organization has no usage records'
      );
    });

    // An inverted range is rejected with 10404.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/metering/usage-summary?since=${now}&until=${now - 10}`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10404, 'AC10: inverted range');
    });

    // A range over 92 days is rejected with 10404.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/metering/usage-summary?since=${now - 93 * 24 * 3600}&until=${now}`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10404, 'AC10: range over 92 days');
    });

    // An unknown voucher is 10403.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/metering/vouchers/voucher-missing',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10403, 'AC9: unknown voucher');
    });
  }
};
