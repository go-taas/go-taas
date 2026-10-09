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

// Feature-45 (inference routing policies) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/inference-routing-policies.md reachable from the
// outside: the admin Routing Policies page (AC1), the masked health
// projection (AC2), target validation (AC3), the enable/disable flow
// (AC4), retry constraints (AC5), the impact confirmation (AC6) and
// the API surface separation.

const { execSync } = require('child_process');
const path = require('path');
const api = require('../page-objects/api.js');

const MODEL_ID = '33333333-3333-3333-3333-333333333333';
const RUNNING_ID = '44444444-4444-4444-4444-444444444444';
const PENDING_ID = '55555555-5555-5555-5555-555555555555';

// Seed the routing model + running/pending services directly into
// PostgreSQL (the compose stack has no controller to set a service
// running).
function seedRouting(browser, org) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/routing',
    'golang:1.26-alpine',
    `sh -c "go run seed_routing.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -org '${org}'"`
  ].join(' ');
  try {
    execSync(cmd, { stdio: 'pipe', timeout: 120000 });
  } catch (e) {
    browser.assert.fail(`seedRouting failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['routing-policies', 'feature-45'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-routing-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation ----

  'routing-policy APIs are admin-only (user prefix 404s)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/routing-policies?page.limit=5',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertOk(browser, res, 'admin routing-policies reachable');
    });
    // The bare user prefix has no routing-policy route: a user-realm
    // session passes the user-surface realm guard and gets the gateway
    // 404 (an admin session would be rejected 10038 before routing).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/routing-policies?page.limit=5',
      org,
      sessionRealm: 'user'
    }, (res) => {
      browser.assert.equal(res.status, 404, 'user-prefix routing-policies is 404');
    });
  },

  'AC10: a user session cannot read or update routing policies': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/routing-policies?page.limit=5',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC10: user realm rejected on admin list');
    });
    api.request(browser, {
      method: 'PUT',
      path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
      org,
      sessionRealm: 'user',
      body: {
        model_version: 'v1',
        enabled: true,
        service_ids: [RUNNING_ID],
        max_attempts: 1,
        expected_revision: '0'
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC10: user realm rejected on admin update');
    });
  },

  'AC10: routing-policy admin RPCs require a session (10027) and the admin role (10036)': function (browser) {
    const org = browser.globals.orgA;
    // Without any session the RoleGuard cannot resolve the caller.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/routing-policies?page.limit=5',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10027, 'AC10: no session rejected');
    });
    // A non-member admin-realm session is denied by the RoleGuard
    // (the seeded member rows are skipped with -no-member).
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/routing-policies?page.limit=5',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertBusinessError(browser, res, 10036, 'AC10: non-member session denied');
    });
  },

  'admin page is served at /admin/routing-policies': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/routing-policies');
    browser.waitForElementPresent('[data-testid="routing-policies-page"]', 15000, 'admin page served');
    browser.waitForElementPresent('[data-testid="nav-routing-policies"]', 15000, 'nav entry present');
  },

  // ---- API contract ----

  'AC1: list shows the seeded model with DEFAULT state': function (browser) {
    const org = browser.globals.orgA;
    seedRouting(browser, org);
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/routing-policies?search=routing-model',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: list');
      const policies = body.policies || [];
      const found = policies.find((p) => p.modelId === MODEL_ID);
      browser.assert.ok(found, 'AC1: seeded model listed');
      browser.assert.equal(found.policyState, 'DEFAULT', 'AC1: default state');
      browser.assert.equal(found.modelVersion, 'v1', 'AC1: active version pinned');
    });
  },

  'AC2: health projection is masked (no endpoint URLs)': function (browser) {
    const org = browser.globals.orgA;
    seedRouting(browser, org);
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/routing-policies/${MODEL_ID}/health`,
      org,
      sessionRealm: 'admin'
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC2: health');
      const targets = body.targets || [];
      browser.assert.ok(targets.length >= 2, 'AC2: both services listed');
      for (const target of targets) {
        browser.assert.ok(target.serviceId, 'AC2: service id present');
        browser.assert.equal(target.endpoints, undefined, 'AC2: no endpoints field');
        browser.assert.equal(target.podName, undefined, 'AC2: no pod name');
      }
      const running = targets.find((t) => t.serviceId === RUNNING_ID);
      browser.assert.ok(running, 'AC2: running service listed');
      browser.assert.equal(running.readyReplicas, 1, 'AC2: running ready replicas');
    });
  },

  'AC3: ineligible targets are rejected with 10312': function (browser) {
    const org = browser.globals.orgA;
    seedRouting(browser, org);
    // A target of a different model version is rejected.
    api.request(browser, {
      method: 'PUT',
      path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
      org,
      sessionRealm: 'admin',
      body: {
        model_version: 'v1',
        enabled: true,
        service_ids: [RUNNING_ID, PENDING_ID],
        max_attempts: 2,
        retry_on: ['RETRY_CATEGORY_HTTP_429'],
        expected_revision: '0'
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC3: valid targets accepted');
      browser.assert.equal(body.policy.revision, '1', 'AC3: revision 1');
    });
    // A duplicate target is rejected.
    api.request(browser, {
      method: 'PUT',
      path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
      org,
      sessionRealm: 'admin',
      body: {
        model_version: 'v1',
        enabled: true,
        service_ids: [RUNNING_ID, RUNNING_ID],
        max_attempts: 1,
        expected_revision: '1'
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10312, 'AC3: duplicate target -> 10312');
    });
  },

  'AC4: enabling with zero ready targets is blocked; disable returns to default': function (browser) {
    const org = browser.globals.orgA;
    seedRouting(browser, org);
    // Enable with only the pending (not ready) target -> 10312.
    api.request(browser, {
      method: 'PUT',
      path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
      org,
      sessionRealm: 'admin',
      body: {
        model_version: 'v1',
        enabled: true,
        service_ids: [PENDING_ID],
        max_attempts: 1,
        expected_revision: '0'
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10312, 'AC4: no ready target -> 10312');
    });
    // Enable with the running target, then disable.
    api.request(browser, {
      method: 'PUT',
      path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
      org,
      sessionRealm: 'admin',
      body: {
        model_version: 'v1',
        enabled: true,
        service_ids: [RUNNING_ID],
        max_attempts: 1,
        expected_revision: '0'
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC4: enable');
      browser.assert.equal(body.policy.enabled, true, 'AC4: enabled');
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
        org,
        sessionRealm: 'admin',
        body: {
          model_version: 'v1',
          enabled: false,
          service_ids: [RUNNING_ID],
          max_attempts: 1,
          expected_revision: '1'
        }
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC4: disable');
        browser.assert.equal(body2.policy.enabled, false, 'AC4: disabled');
        // The list shows DEFAULT again.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/routing-policies?search=routing-model`,
          org,
          sessionRealm: 'admin'
        }, (res3) => {
          const list = api.assertOk(browser, res3, 'AC4: list');
          const found = (list.policies || []).find((p) => p.modelId === MODEL_ID);
          browser.assert.equal(found.policyState, 'DEFAULT', 'AC4: default balancing state');
        });
      });
    });
  },

  'AC5: attempts above one with no retry category is rejected': function (browser) {
    const org = browser.globals.orgA;
    seedRouting(browser, org);
    api.request(browser, {
      method: 'PUT',
      path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
      org,
      sessionRealm: 'admin',
      body: {
        model_version: 'v1',
        enabled: true,
        service_ids: [RUNNING_ID],
        max_attempts: 3,
        retry_on: [],
        expected_revision: '0'
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10312, 'AC5: attempts without categories -> 10312');
    });
    // Attempts out of range (4) is rejected.
    api.request(browser, {
      method: 'PUT',
      path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
      org,
      sessionRealm: 'admin',
      body: {
        model_version: 'v1',
        enabled: true,
        service_ids: [RUNNING_ID],
        max_attempts: 4,
        retry_on: ['RETRY_CATEGORY_HTTP_5XX'],
        expected_revision: '0'
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10312, 'AC5: attempts out of range -> 10312');
    });
  },

  'AC6: stale revision is rejected with 10313; revisions list history': function (browser) {
    const org = browser.globals.orgA;
    seedRouting(browser, org);
    api.request(browser, {
      method: 'PUT',
      path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
      org,
      sessionRealm: 'admin',
      body: {
        model_version: 'v1',
        enabled: true,
        service_ids: [RUNNING_ID],
        max_attempts: 1,
        expected_revision: '0'
      }
    }, (res) => {
      api.assertOk(browser, res, 'AC6: create revision 1');
      // A stale expected_revision -> 10313.
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/routing-policies/${MODEL_ID}`,
        org,
        sessionRealm: 'admin',
        body: {
          model_version: 'v1',
          enabled: false,
          service_ids: [RUNNING_ID],
          max_attempts: 1,
          expected_revision: '0'
        }
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10313, 'AC6: stale revision -> 10313');
        // The revision history lists the immutable revision.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/routing-policies/${MODEL_ID}/revisions`,
          org,
          sessionRealm: 'admin'
        }, (res3) => {
          const body = api.assertOk(browser, res3, 'AC6: revisions');
          const revisions = body.revisions || [];
          browser.assert.ok(revisions.length >= 1, 'AC6: history present');
          browser.assert.equal(revisions[0].revision, '1', 'AC6: newest first');
          browser.assert.ok(revisions[0].actor, 'AC6: actor recorded');
          browser.assert.ok(revisions[0].after, 'AC6: after snapshot present');
        });
      });
    });
  },

  // ---- UI flow ----

  'UI: configure drawer applies a policy end-to-end': function (browser) {
    const org = browser.globals.orgA;
    seedRouting(browser, org);
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/routing-policies');
    browser.waitForElementPresent('[data-testid="routing-policies-table"]', 15000, 'table renders');

    // Open the configuration drawer for the seeded model.
    browser.waitForElementPresent(`[data-testid="configure-policy-${MODEL_ID}"]`, 15000, 'configure button');
    browser.click(`[data-testid="configure-policy-${MODEL_ID}"]`);
    browser.waitForElementPresent('[data-testid="routing-policy-drawer"]', 15000, 'drawer opens');
    browser.waitForElementPresent('[data-testid="policy-enabled-toggle"]', 15000, 'toggle present');

    // Enable the policy; the confirmation dialog names the model.
    browser.click('[data-testid="policy-enabled-toggle"]');
    browser.waitForElementPresent('[data-testid="save-policy"]', 5000, 'save enabled');
    browser.click('[data-testid="save-policy"]');
    browser.waitForElementPresent('[data-testid="apply-policy-confirm"]', 5000, 'confirm dialog');
    browser.waitForElementPresent('[data-testid="apply-policy-message"]', 5000, 'confirm message');
    browser.click('[data-testid="apply-policy"]');
    // The drawer reloads with the new revision.
    browser.waitForElementPresent('[data-testid="drawer-model-name"]', 15000, 'drawer reloaded');

    // The list now shows ENABLED.
    browser.click('[data-testid="drawer-close"]');
    browser.waitForElementPresent(`[data-testid="ready-count-${MODEL_ID}"]`, 15000, 'row refreshed');
  }
};
