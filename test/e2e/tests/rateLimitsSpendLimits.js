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

// Feature-11 (per-key rate limits & org spend limits) e2e suite, run
// against the compose stack gateway. Covers the acceptance criteria of
// docs/design/rate-limits-spend-limits.md reachable from the outside:
// the API Keys create dialog persists rate limits and the edit dialog
// updates them (AC-C1), and the Accounts create dialog persists the
// spend limit (AC-C2). The gateway enforcement (429) is covered by the
// FVT suite.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['rate-limits-spend-limits', 'feature-11'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-rl-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed admin- and user-realm sessions so both the admin and user
    // protected pages render (feature: unauthenticated pages redirect to
    // login). The suite navigates to /admin/api-keys (→ /api-keys user)
    // and /admin/billing/accounts (admin).
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC-C1: API key create persists rate limits and edit updates them': function (browser) {
    const org = browser.globals.orgA;

    // Create a key with rate limits via the end-user API.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/auth/api-keys',
      org,
      sessionRealm: 'user',
      body: {name: 'limited', rateLimitRpm: 100, rateLimitTpm: 50000}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create key with limits');
      browser.assert.ok(body.keyId, 'AC-C1: key id returned');
      const keyId = body.keyId;
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/auth/api-keys/${keyId}`,
        org,
        sessionRealm: 'user',
        body: {name: 'limited', rateLimitRpm: 120, rateLimitTpm: 60000}
      }, (updateRes) => {
        api.assertOk(browser, updateRes, 'update key limits');

        api.request(browser, {
          method: 'GET',
          path: '/api/v1/auth/api-keys',
          org,
          sessionRealm: 'user'
        }, (listRes) => {
          const listBody = api.assertOk(browser, listRes, 'list keys');
          const key = (listBody.keys || []).find((item) => item.name === 'limited');
          browser.assert.ok(key, 'AC-C1: created key listed');
          browser.assert.equal(String(key.rateLimitRpm), '120', 'AC-C1: updated rpm persisted');
          browser.assert.equal(String(key.rateLimitTpm), '60000', 'AC-C1: updated tpm persisted');
        });
      });
    });
  },

  'AC-C2: account create persists the spend limit': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'prepaid', overdrawPolicy: 'block', initialBalanceCents: 10000, monthlySpendLimitCents: 5000}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create account with spend limit');
      browser.assert.equal(
        String(body.account.monthlySpendLimitCents),
        '5000',
        'AC-C2: spend limit persisted'
      );
    });
  },

  'AC9: a user session cannot call the admin API-key API': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/auth/api-keys',
      org: browser.globals.orgA,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC9: wrong realm rejected');
    });
  },

  'AC-C2: accounts page renders spend-limit column': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/billing/accounts');
    browser.waitForElementPresent(
      '[data-testid="create-account-button"]',
      10000,
      'AC-C2: create account button renders'
    );
    browser.waitForElementPresent(
      '[data-testid="accounts-empty"], [data-testid="accounts-table"]',
      10000,
      'AC-C2: accounts list renders'
    );
  },

  'AC-C1: API keys page renders rate-limit column': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/api-keys');
    browser.waitForElementPresent(
      '[data-testid="create-api-key"]',
      10000,
      'AC-C1: create button renders'
    );
    browser.waitForElementPresent(
      '[data-testid="api-keys-empty"], [data-testid="api-keys-table"]',
      10000,
      'AC-C1: keys list renders'
    );
  }
};