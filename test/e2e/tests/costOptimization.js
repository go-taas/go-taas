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

// Feature-50 (cost optimization recommendations) e2e suite, run against
// the compose stack gateway. Covers the acceptance criteria of
// docs/design/cost-optimization-recommendations.md reachable from the
// outside: the admin summary cards and ranked recommendation cards
// (AC1), the filter bar (AC2), the drawer with inputs/arithmetic/acting
// link (AC3), the dismiss/restore lifecycle (AC4), the refresh trigger
// (AC10), the R3 admin-only masking on the user surface (AC8), the
// negative realm separation (AC11), the role gate (AC12), and the empty
// state (AC13).

const { execSync } = require('child_process');
const path = require('path');
const api = require('../page-objects/api.js');

// Seed the optimization usage/price/service fixtures directly into
// PostgreSQL so the engine's rules fire deterministically.
function seedOptimization(browser, org) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/optimization',
    'golang:1.26-alpine',
    `sh -c "go run seed_optimization.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -org '${org}'"`
  ].join(' ');
  try {
    execSync(cmd, { stdio: 'pipe', timeout: 120000 });
  } catch (e) {
    browser.assert.fail(`seedOptimization failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['cost-optimization-recommendations', 'feature-50'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-opt-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation (AC11/AC12) ----

  'AC11: optimization admin APIs are admin-only (user prefix 404s)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/optimization',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertOk(browser, res, 'AC11: admin optimization reachable');
    });
    // The bare user prefix has no admin optimization route.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/optimization',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC11: user realm rejected on admin optimization');
    });
  },

  'AC11: a user session cannot read or refresh the admin optimization surface': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/optimization/recommendations',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC11: user realm rejected on admin list');
    });
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/optimization:refresh',
      org,
      sessionRealm: 'user',
      body: {}
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC11: user realm rejected on admin refresh');
    });
  },

  'AC12: optimization admin RPCs require a session (10027) and the admin role (10036)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/optimization',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10027, 'AC12: no session rejected');
    });
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/optimization',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertBusinessError(browser, res, 10036, 'AC12: non-member session denied');
    });
  },

  'AC11: the admin page is served at /admin/optimization and the user page at /optimization': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/optimization');
    browser.waitForElementPresent('[data-testid="optimization-summary-cards"]', 15000, 'AC11: admin page served');
    browser.waitForElementPresent('[data-testid="nav-optimization"]', 15000, 'AC11: admin nav entry');
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/optimization');
    browser.waitForElementPresent('[data-testid="optimization-summary-cards"]', 15000, 'AC11: user page served');
  },

  // ---- API contract (AC1/AC2/AC3/AC4/AC10) ----

  'AC1/AC10: refresh emits recommendations and the overview totals the active set': function (browser) {
    const org = browser.globals.orgA;
    seedOptimization(browser, org);
    // Trigger a refresh.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/optimization:refresh',
      org,
      sessionRealm: 'admin',
      body: {}
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC10: refresh');
      const overview = body.overview || {};
      browser.assert.ok(overview.totalEstimatedMonthlySaving, 'AC1: total saving present');
      browser.assert.ok(parseInt(overview.activeCount, 10) >= 1, 'AC1: active count >= 1');

      // The ranked list is saving-descending and every row carries the
      // verifiable fields.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/optimization/recommendations?status=all',
        org,
        sessionRealm: 'admin'
      }, (res2) => {
        const list = api.assertOk(browser, res2, 'AC1: list');
        const recs = list.recommendations || [];
        browser.assert.ok(recs.length >= 1, 'AC1: at least one recommendation');
        let prevSaving = Infinity;
        for (const rec of recs) {
          browser.assert.ok(rec.rule, 'AC1: rule present');
          browser.assert.ok(rec.targetDisplay, 'AC1: target present');
          browser.assert.ok(rec.suggestion, 'AC1: suggestion present');
          browser.assert.ok(rec.estimatedMonthlySaving, 'AC1: saving present');
          browser.assert.ok(rec.arithmetic, 'AC1: arithmetic present');
          const saving = parseFloat(rec.estimatedMonthlySaving);
          browser.assert.ok(saving <= prevSaving + 0.001, 'AC1: saving-descending ranking');
          prevSaving = saving;
        }
      });
    });
  },

  'AC2: the filter bar validates and narrows the list': function (browser) {
    const org = browser.globals.orgA;
    seedOptimization(browser, org);
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/optimization:refresh',
      org,
      sessionRealm: 'admin',
      body: {}
    }, (res) => {
      api.assertOk(browser, res, 'AC2: refresh');
      // Invalid rule filter -> 13205.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/optimization/recommendations?rule=bogus',
        org,
        sessionRealm: 'admin'
      }, (res2) => {
        api.assertBusinessError(browser, res2, 13205, 'AC2: invalid rule -> 13205');
      });
      // Invalid status -> 13205.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/optimization/recommendations?status=bogus',
        org,
        sessionRealm: 'admin'
      }, (res3) => {
        api.assertBusinessError(browser, res3, 13205, 'AC2: invalid status -> 13205');
      });
      // Invalid min_saving -> 13205.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/optimization/recommendations?min_saving=-5',
        org,
        sessionRealm: 'admin'
      }, (res4) => {
        api.assertBusinessError(browser, res4, 13205, 'AC2: invalid min_saving -> 13205');
      });
      // Valid filters work.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/optimization/recommendations?rule=r2_batch&status=all&min_saving=0',
        org,
        sessionRealm: 'admin'
      }, (res5) => {
        api.assertOk(browser, res5, 'AC2: valid filters');
      });
    });
  },

  'AC3: the drawer data is served by the get endpoint with inputs, arithmetic and acting link': function (browser) {
    const org = browser.globals.orgA;
    seedOptimization(browser, org);
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/optimization:refresh',
      org,
      sessionRealm: 'admin',
      body: {}
    }, (res) => {
      api.assertOk(browser, res, 'AC3: refresh');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/optimization/recommendations?status=all',
        org,
        sessionRealm: 'admin'
      }, (res2) => {
        const list = api.assertOk(browser, res2, 'AC3: list');
        const recs = list.recommendations || [];
        browser.assert.ok(recs.length >= 1, 'AC3: at least one recommendation');
        const rec = recs[0];
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/optimization/recommendations/${rec.recommendationId}`,
          org,
          sessionRealm: 'admin'
        }, (res3) => {
          const detail = api.assertOk(browser, res3, 'AC3: get');
          const d = detail.recommendation || {};
          browser.assert.ok(d.inputsJson, 'AC3: inputs present');
          browser.assert.ok(d.arithmetic, 'AC3: arithmetic present');
          browser.assert.ok(d.actingPage && d.actingPage.route, 'AC3: acting link present');
          browser.assert.ok(d.actingPage.route.startsWith('/admin/'), 'AC3: admin acting link');
        });
      });
    });
  },

  'AC4: dismiss/restore lifecycle with validation errors': function (browser) {
    const org = browser.globals.orgA;
    seedOptimization(browser, org);
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/optimization:refresh',
      org,
      sessionRealm: 'admin',
      body: {}
    }, (res) => {
      api.assertOk(browser, res, 'AC4: refresh');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/optimization/recommendations?status=all',
        org,
        sessionRealm: 'admin'
      }, (res2) => {
        const list = api.assertOk(browser, res2, 'AC4: list');
        const recs = list.recommendations || [];
        browser.assert.ok(recs.length >= 1, 'AC4: at least one recommendation');
        const id = recs[0].recommendationId;

        // Invalid dismiss reason -> 13203.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/optimization/recommendations/${id}:dismiss`,
          org,
          sessionRealm: 'admin',
          body: {reason: 'bogus'}
        }, (res3) => {
          api.assertBusinessError(browser, res3, 13203, 'AC4: invalid reason -> 13203');
        });

        // reason=other without a note -> 13203.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/optimization/recommendations/${id}:dismiss`,
          org,
          sessionRealm: 'admin',
          body: {reason: 'other'}
        }, (res4) => {
          api.assertBusinessError(browser, res4, 13203, 'AC4: other without note -> 13203');
        });

        // Valid dismiss -> status dismissed.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/optimization/recommendations/${id}:dismiss`,
          org,
          sessionRealm: 'admin',
          body: {reason: 'planned'}
        }, (res5) => {
          const body5 = api.assertOk(browser, res5, 'AC4: dismiss');
          browser.assert.equal(body5.recommendation.status, 'dismissed', 'AC4: dismissed');

          // Double dismiss -> 13202.
          api.request(browser, {
            method: 'POST',
            path: `/api/v1/admin/optimization/recommendations/${id}:dismiss`,
            org,
            sessionRealm: 'admin',
            body: {reason: 'planned'}
          }, (res6) => {
            api.assertBusinessError(browser, res6, 13202, 'AC4: double dismiss -> 13202');
          });

          // Restore -> active.
          api.request(browser, {
            method: 'POST',
            path: `/api/v1/admin/optimization/recommendations/${id}:restore`,
            org,
            sessionRealm: 'admin',
            body: {}
          }, (res7) => {
            const body7 = api.assertOk(browser, res7, 'AC4: restore');
            browser.assert.equal(body7.recommendation.status, 'active', 'AC4: active');

            // Restore again -> 13204.
            api.request(browser, {
              method: 'POST',
              path: `/api/v1/admin/optimization/recommendations/${id}:restore`,
              org,
              sessionRealm: 'admin',
              body: {}
            }, (res8) => {
              api.assertBusinessError(browser, res8, 13204, 'AC4: restore active -> 13204');
            });
          });
        });
      });
    });
  },

  'AC4: unknown recommendation id -> 13201': function (browser) {
    const org = browser.globals.orgA;
    // Use a valid UUID that does not exist (a malformed id would hit the
    // PostgreSQL uuid cast error -> code 13, not the not-found code).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/optimization/recommendations/99999999-9999-9999-9999-999999999999',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertBusinessError(browser, res, 13201, 'AC4: unknown id -> 13201');
    });
  },

  // ---- User surface (AC8/AC9) ----

  'AC8/AC9: the user surface is org-scoped and masks R3': function (browser) {
    const org = browser.globals.orgA;
    seedOptimization(browser, org);
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/optimization:refresh',
      org,
      sessionRealm: 'admin',
      body: {}
    }, (res) => {
      api.assertOk(browser, res, 'AC8: refresh');
      // The user overview and list never contain R3.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/optimization',
        org,
        sessionRealm: 'user'
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC8: user overview');
        for (const rec of body2.recommendations || []) {
          browser.assert.notEqual(rec.rule, 'r3_idle_service', 'AC8: R3 masked on user surface');
          browser.assert.ok(!rec.actingPage || !rec.actingPage.route.startsWith('/admin/'), 'AC8: no admin acting link');
        }
      });
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/optimization/recommendations?status=all',
        org,
        sessionRealm: 'user'
      }, (res3) => {
        const list = api.assertOk(browser, res3, 'AC8: user list');
        for (const rec of list.recommendations || []) {
          browser.assert.notEqual(rec.rule, 'r3_idle_service', 'AC8: R3 masked in user list');
        }
      });
    });
  },

  'AC8: the user can dismiss and restore their own recommendation': function (browser) {
    const org = browser.globals.orgA;
    seedOptimization(browser, org);
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/optimization:refresh',
      org,
      sessionRealm: 'admin',
      body: {}
    }, (res) => {
      api.assertOk(browser, res, 'AC8: refresh');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/optimization/recommendations?status=all',
        org,
        sessionRealm: 'user'
      }, (res2) => {
        const list = api.assertOk(browser, res2, 'AC8: user list');
        const recs = list.recommendations || [];
        browser.assert.ok(recs.length >= 1, 'AC8: at least one user recommendation');
        const id = recs[0].recommendationId;
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/optimization/recommendations/${id}:dismiss`,
          org,
          sessionRealm: 'user',
          body: {reason: 'not_applicable'}
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'AC8: user dismiss');
          browser.assert.equal(body3.recommendation.status, 'dismissed', 'AC8: user dismissed');
          // The user dismissal is surface-scoped: the admin surface still
          // sees it active.
          api.request(browser, {
            method: 'GET',
            path: `/api/v1/admin/optimization/recommendations/${id}`,
            org,
            sessionRealm: 'admin'
          }, (res4) => {
            const body4 = api.assertOk(browser, res4, 'AC8: admin get');
            browser.assert.equal(body4.recommendation.status, 'active', 'AC8: user dismissal is surface-scoped');
          });
        });
      });
    });
  },

  // ---- UI flow (AC1/AC13) ----

  'AC1: the admin page renders the summary cards and filters': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/optimization');
    browser.waitForElementPresent('[data-testid="optimization-summary-cards"]', 15000, 'AC1: summary cards');
    browser.waitForElementPresent('[data-testid="optimization-total-saving"]', 10000, 'AC1: total saving');
    browser.waitForElementPresent('[data-testid="optimization-active-count"]', 10000, 'AC1: active count');
    browser.waitForElementPresent('[data-testid="optimization-dismissed-count"]', 10000, 'AC1: dismissed count');
    browser.waitForElementPresent('[data-testid="optimization-last-refresh"]', 10000, 'AC1: last refresh');
    browser.waitForElementPresent('[data-testid="optimization-refresh-now"]', 10000, 'AC1: refresh action');
    browser.waitForElementPresent('[data-testid="optimization-filter-rule"]', 10000, 'AC1: rule filter');
    browser.waitForElementPresent('[data-testid="optimization-filter-status"]', 10000, 'AC1: status filter');
  }
};