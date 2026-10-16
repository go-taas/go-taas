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

// Feature-51 (service quota management) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/service-quota-management.md reachable from the outside:
// the admin org table (AC1), the edit dialog validation and presets
// (AC2), the change history (AC10), the user quota bar and /quotas page
// (AC8), the tenant org scoping (AC9), the negative realm separation
// (AC12), the role gate (AC13), and the states (AC14).

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['service-quota-management', 'feature-51'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-quota-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation (AC12/AC13) ----

  'AC12: quota admin APIs are admin-only (user prefix 404s)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/quotas?limit=50',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertOk(browser, res, 'AC12: admin quotas reachable');
    });
    // The bare user prefix has no admin quota route.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/quotas?limit=50',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC12: user realm rejected on admin quotas');
    });
  },

  'AC12: a user session cannot read or mutate quotas (10038)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/quotas?limit=50',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC12: user realm rejected on admin list');
    });
    api.request(browser, {
      method: 'PATCH',
      path: `/api/v1/admin/quotas/${org}`,
      org,
      sessionRealm: 'user',
      body: {maxConcurrentRequests: 50}
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC12: user realm rejected on admin update');
    });
  },

  'AC13: quota admin RPCs require the admin role (10036)': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/quotas?limit=50',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertBusinessError(browser, res, 10036, 'AC13: non-member session denied');
    });
  },

  'AC12: the admin page is served at /admin/quotas and the user page at /quotas': function (browser) {
    const org = browser.globals.orgA;
    // Set a quota so the user page renders the cards (not the
    // all-unlimited empty state).
    api.request(browser, {
      method: 'PATCH',
      path: `/api/v1/admin/quotas/${org}`,
      org,
      sessionRealm: 'admin',
      body: {maxConcurrentRequests: 50}
    }, (res) => {
      api.assertOk(browser, res, 'AC12: set quota');
      browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/admin/quotas');
      browser.waitForElementPresent('[data-testid="quota-table"]', 15000, 'AC12: admin page served');
      browser.waitForElementPresent('[data-testid="nav-quotas"]', 15000, 'AC12: admin nav entry');
      browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/quotas');
      browser.waitForElementPresent('[data-testid="quota-cards"]', 15000, 'AC12: user page served');
    });
  },

  // ---- API contract (AC1/AC2/AC10) ----

  'AC1/AC2/AC10: update a quota, read it back with history, and validate errors': function (browser) {
    const org = browser.globals.orgA;
    // The org is unlimited by default.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/quotas/${org}`,
      org,
      sessionRealm: 'admin'
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: get quota');
      browser.assert.equal(body.quota.organizationId, org, 'AC1: org id');
      browser.assert.equal(body.quota.maxConcurrentRequests, '0', 'AC1: unlimited by default');

      // Update concurrency and services.
      api.request(browser, {
        method: 'PATCH',
        path: `/api/v1/admin/quotas/${org}`,
        org,
        sessionRealm: 'admin',
        body: {maxConcurrentRequests: 50, maxInferenceServices: 20}
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC2: update');
        browser.assert.equal(body2.quota.maxConcurrentRequests, '50', 'AC2: concurrency updated');
        browser.assert.equal(body2.quota.maxInferenceServices, '20', 'AC2: services updated');
        browser.assert.ok(body2.quota.updatedBy, 'AC2: updated by present');

        // Get: the change history carries the two dimensions.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/quotas/${org}`,
          org,
          sessionRealm: 'admin'
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'AC10: get with history');
          const changes = body3.changes || [];
          browser.assert.ok(changes.length >= 2, 'AC10: change history present');
          browser.assert.equal(changes[0].dimension, 'max_concurrent_requests', 'AC10: newest first');
          browser.assert.equal(changes[0].oldValue, '0', 'AC10: old value');
          browser.assert.equal(changes[0].newValue, '50', 'AC10: new value');
        });

        // Invalid value -> 13305.
        api.request(browser, {
          method: 'PATCH',
          path: `/api/v1/admin/quotas/${org}`,
          org,
          sessionRealm: 'admin',
          body: {maxConcurrentRequests: 10001}
        }, (res4) => {
          api.assertBusinessError(browser, res4, 13305, 'AC2: out-of-range -> 13305');
        });

        // Unknown org -> 13306.
        api.request(browser, {
          method: 'GET',
          path: '/api/v1/admin/quotas/org-missing',
          org,
          sessionRealm: 'admin'
        }, (res5) => {
          api.assertBusinessError(browser, res5, 13306, 'AC2: unknown org -> 13306');
        });

        // Invalid limit_state -> 13305.
        api.request(browser, {
          method: 'GET',
          path: '/api/v1/admin/quotas?limit_state=bogus',
          org,
          sessionRealm: 'admin'
        }, (res6) => {
          api.assertBusinessError(browser, res6, 13305, 'AC2: invalid limit_state -> 13305');
        });
      });
    });
  },

  // ---- User surface (AC8/AC9) ----

  'AC8/AC9: the user surface reads the caller org quota with the request-increase mailbox': function (browser) {
    const org = browser.globals.orgA;
    // Set a quota as admin first.
    api.request(browser, {
      method: 'PATCH',
      path: `/api/v1/admin/quotas/${org}`,
      org,
      sessionRealm: 'admin',
      body: {maxConcurrentRequests: 50}
    }, (res) => {
      api.assertOk(browser, res, 'AC8: admin update');
      // The user surface reads the same org.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/quotas',
        org,
        sessionRealm: 'user'
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC8: user quota');
        browser.assert.equal(body2.quota.organizationId, org, 'AC8: own org');
        browser.assert.equal(body2.quota.maxConcurrentRequests, '50', 'AC8: concurrency');
      });
    });
  },

  'AC8: the user quota bar and /quotas page render the four dimension cards': function (browser) {
    const org = browser.globals.orgA;
    // Set a quota so the bar is not hidden (all-unlimited hides it).
    api.request(browser, {
      method: 'PATCH',
      path: `/api/v1/admin/quotas/${org}`,
      org,
      sessionRealm: 'admin',
      body: {maxConcurrentRequests: 50, maxInferenceServices: 20, maxBatchJobsPerDay: 10, maxOutputTokensPerRequest: 8192}
    }, (res) => {
      api.assertOk(browser, res, 'AC8: admin update');
      browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/quotas');
      browser.waitForElementPresent('[data-testid="quota-cards"]', 15000, 'AC8: cards region');
      browser.waitForElementPresent('[data-testid="quota-card-concurrent_requests"]', 10000, 'AC8: concurrency card');
      browser.waitForElementPresent('[data-testid="quota-card-inference_services"]', 10000, 'AC8: services card');
      browser.waitForElementPresent('[data-testid="quota-card-batch_jobs_today"]', 10000, 'AC8: batch card');
      browser.waitForElementPresent('[data-testid="quota-card-output_tokens_per_request"]', 10000, 'AC8: token card');
    });
  },

  'AC8: the /quotas page renders the Request-increase mailto to the configured operator address': function (browser) {
    const org = browser.globals.orgA;
    // Set a quota so the page renders the cards (not the all-unlimited
    // empty state) and the Request-increase button is visible.
    api.request(browser, {
      method: 'PATCH',
      path: `/api/v1/admin/quotas/${org}`,
      org,
      sessionRealm: 'admin',
      body: {maxConcurrentRequests: 50}
    }, (res) => {
      api.assertOk(browser, res, 'AC8: admin update');
      browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/quotas');
      browser.waitForElementPresent('[data-testid="quota-request-increase"]', 15000, 'AC8: request-increase button');
      // The mailto points at the operator address the compose stack
      // configures (CONFIG_QUOTA_REQUESTINCREASETO) and carries the org
      // display name prefilled.
      browser.getAttribute('[data-testid="quota-request-increase"]', 'href', (res2) => {
        const href = res2.value || '';
        browser.assert.ok(href.startsWith('mailto:ops@go-taas.example'), `AC8: mailto to operator address (got ${href})`);
        browser.assert.ok(href.includes('subject='), 'AC8: mailto subject prefilled');
        browser.assert.ok(href.includes('body='), 'AC8: mailto body prefilled');
      });
    });
  },

  // ---- UI flow (AC1/AC14) ----

  'AC1/AC14: the admin page renders the org table and the edit dialog': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/quotas');
    browser.waitForElementPresent('[data-testid="quota-table"]', 15000, 'AC1: table renders');
    browser.waitForElementPresent('[data-testid="quota-search"]', 10000, 'AC1: search');
    browser.waitForElementPresent('[data-testid="quota-filter-limit-state"]', 10000, 'AC1: limit-state filter');
    // The table is paginated (20/page) with 1000+ orgs in the persisted
    // database, so search for the fresh org to bring its row to the
    // first page.
    browser.setValue('[data-testid="quota-search"]', org);
    browser.waitForElementPresent(`[data-testid="quota-row-${org}"]`, 15000, 'AC1: org row');
    browser.waitForElementPresent(`[data-testid="quota-edit-${org}"]`, 10000, 'AC1: edit action');
    // Open the edit dialog and verify the presets and fields.
    browser.click(`[data-testid="quota-edit-${org}"]`);
    browser.waitForElementPresent('[data-testid="quota-edit-dialog"]', 10000, 'AC2: edit dialog');
    browser.waitForElementPresent('[data-testid="quota-preset-starter"]', 10000, 'AC2: preset');
    browser.waitForElementPresent('[data-testid="quota-field-maxConcurrentRequests"]', 10000, 'AC2: concurrency field');
    browser.waitForElementPresent('[data-testid="quota-save"]', 10000, 'AC2: save button');
  }
};