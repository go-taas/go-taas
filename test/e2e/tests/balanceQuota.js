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

// Feature-08 (balance & quota accounts) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/balance-quota.md reachable from the outside: the
// Accounts page structure with create/recharge/quota dialogs (FR7),
// the account APIs' validation matrices (10509/10510), recharge
// idempotency (AC3), the ledger query (AC9), org scoping (AC10) and
// the org header requirement (AC16). Settlement-driven deductions
// happen via the internal MQ surface, covered by the FVT suite.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['balance-quota', 'feature-08'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    // One account per org (AD1): every test gets fresh orgs so the
    // create calls never collide across tests.
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-${browser.globals.runId}-${browser.globals.testSeq}`;
    browser.globals.orgB = `org-e2e-b-${browser.globals.runId}-${browser.globals.testSeq}`;
    // The org-scoped APIs validate the org header against the
    // organizations table, so the org ids must exist first.
    api.ensureOrg(browser, browser.globals.orgA);
    api.ensureOrg(browser, browser.globals.orgB);
  },

  afterEach(browser) {
    browser.end();
  },

  'FR7: accounts page renders empty state and create dialog': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/billing/accounts');
    browser.waitForElementPresent(
      '[data-testid="accounts-empty"]',
      10000,
      'FR7: empty state renders for a fresh org'
    );

    // The create dialog opens with the mode selector.
    browser.click('[data-testid="create-account-button"]');
    browser.waitForElementPresent('[data-testid="create-account-dialog"]', 5000, 'FR7: create dialog opens');
    browser.waitForElementPresent('[data-testid="create-mode-select"]', 5000, 'FR7: mode selector renders');
    browser.waitForElementPresent('[data-testid="create-initial-balance"]', 5000, 'FR7: opening balance field renders');
  },

  'AC1: create account round-trip with opening balance': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'prepaid', overdrawPolicy: 'block', initialBalanceCents: 10000}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create account');
      browser.assert.ok(
        body.account && body.account.accountId,
        'AC1: account_id returned'
      );
      browser.assert.equal(body.account.mode, 'prepaid', 'AC1: mode round-trips');
      browser.assert.equal(
        String(body.account.balanceCents),
        '10000',
        'AC1: opening balance round-trips'
      );
      browser.assert.equal(
        String(body.account.remainingCents),
        '10000',
        'AC1: remaining computed'
      );
    });

    // The accounts page renders the row with the mode badge.
    browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/billing/accounts');
    browser.waitForElementPresent('[data-testid="accounts-table"]', 10000, 'AC1: accounts table renders');
    browser.waitForElementPresent('[data-testid="mode-badge-prepaid"]', 5000, 'AC1: mode badge renders');
  },

  'AC1: one account per org — second create is 10509': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'prepaid', overdrawPolicy: 'block'}
    }, (res) => {
      api.assertOk(browser, res, 'first create');
    });

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'postpaid', overdrawPolicy: 'block'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10509, 'AC1: one account per org');
    });
  },

  'AC2: account API validates the matrix (10509)': function (browser) {
    const org = browser.globals.orgA;

    // A bad mode is rejected.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'hybrid', overdrawPolicy: 'block'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10509, 'AC2: bad mode');
    });

    // A bad overdraw policy is rejected.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'postpaid', overdrawPolicy: 'maybe'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10509, 'AC2: bad policy');
    });

    // A negative quota is rejected.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'postpaid', overdrawPolicy: 'block', monthlyQuotaCents: -1}
    }, (res) => {
      api.assertBusinessError(browser, res, 10509, 'AC2: negative quota');
    });

    // A postpaid opening balance is rejected.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'postpaid', overdrawPolicy: 'block', initialBalanceCents: 100}
    }, (res) => {
      api.assertBusinessError(browser, res, 10509, 'AC2: postpaid opening balance');
    });
  },

  'AC3: recharge is idempotent on the key; conflicts are 10510': function (browser) {
    const org = browser.globals.orgA;
    const key = `e2e-rch-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'prepaid', overdrawPolicy: 'block', initialBalanceCents: 1000}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create account');
      const accountId = body.account.accountId;

      // A recharge credits the balance.
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/billing/accounts/${accountId}/recharge`,
        org,
        body: {amountCents: 2500, idempotencyKey: key, note: 'top up'}
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'recharge');
        browser.assert.equal(
          String(body2.account.balanceCents),
          '3500',
          'AC3: balance credited'
        );
      });

      // A replayed key converges without a second credit.
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/billing/accounts/${accountId}/recharge`,
        org,
        body: {amountCents: 2500, idempotencyKey: key}
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'recharge replay');
        browser.assert.equal(
          String(body2.account.balanceCents),
          '3500',
          'AC3: replay converges'
        );
      });

      // A conflicting amount on the same key is 10510.
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/billing/accounts/${accountId}/recharge`,
        org,
        body: {amountCents: 999, idempotencyKey: key}
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10510, 'AC3: conflicting replay');
      });

      // A missing key is 10510.
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/billing/accounts/${accountId}/recharge`,
        org,
        body: {amountCents: 100, idempotencyKey: ''}
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10510, 'AC3: missing key');
      });
    });
  },

  'D8: refund debits a prepaid balance': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'prepaid', overdrawPolicy: 'block', initialBalanceCents: 5000}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create account');
      const accountId = body.account.accountId;

      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/billing/accounts/${accountId}/refund`,
        org,
        body: {amountCents: 1200, idempotencyKey: `e2e-ref-${browser.globals.runId}`}
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'refund');
        browser.assert.equal(
          String(body2.account.balanceCents),
          '3800',
          'D8: balance debited'
        );
        browser.assert.equal(body2.transaction.type, 'refund', 'D8: ledger type');
      });
    });
  },

  'AC9: the ledger lists transactions with filters': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org,
      body: {mode: 'prepaid', overdrawPolicy: 'block', initialBalanceCents: 1000}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create account');
      const accountId = body.account.accountId;

      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/billing/accounts/${accountId}/recharge`,
        org,
        body: {amountCents: 100, idempotencyKey: `e2e-ledger-${browser.globals.runId}`}
      }, (res2) => {
        api.assertOk(browser, res2, 'recharge');

        // The ledger lists the opening recharge plus the second recharge.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/billing/transactions?account_id=${accountId}`,
          org
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'ledger list');
          browser.assert.equal(
            String(body3.pageMeta.total),
            '2',
            'AC9: ledger lists all rows'
          );
        });

        // The type filter narrows to recharges.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/billing/transactions?account_id=${accountId}&type=recharge`,
          org
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'ledger filter');
          browser.assert.equal(
            String(body3.pageMeta.total),
            '2',
            'AC9: type filter matches recharges'
          );
        });

        // A bad type is 10510.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/billing/transactions?account_id=${accountId}&type=donation`,
          org
        }, (res3) => {
          api.assertBusinessError(browser, res3, 10510, 'AC9: bad type filter');
        });
      });
    });
  },

  'AC10: accounts are scoped to the caller org': function (browser) {
    const orgA = browser.globals.orgA;
    const orgB = browser.globals.orgB;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/accounts',
      org: orgA,
      body: {mode: 'prepaid', overdrawPolicy: 'block'}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create account');
      const accountId = body.account.accountId;

      // orgB cannot read orgA's account.
      api.request(browser, {
        method: 'GET',
        path: `/api/v1/admin/billing/accounts/${accountId}`,
        org: orgB
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10503, 'AC10: cross-org read');
      });

      // orgB cannot recharge it either.
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/billing/accounts/${accountId}/recharge`,
        org: orgB,
        body: {amountCents: 100, idempotencyKey: `e2e-cross-${browser.globals.runId}`}
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10503, 'AC10: cross-org recharge');
      });
    });

    // orgB has no accounts of its own.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/billing/accounts',
      org: orgB
    }, (res) => {
      const body = api.assertOk(browser, res, 'orgB accounts');
      browser.assert.deepEqual(
        body.accounts,
        [],
        'AC10: orgB sees no accounts'
      );
    });
  },

  'AC16: account queries require the organization header': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/billing/accounts'
    }, (res) => {
      api.assertBusinessError(browser, res, 10001, 'AC16: missing org header');
    });
  }
};
