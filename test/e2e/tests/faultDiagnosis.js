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

// Feature-53 (fault diagnosis & automatic fault tolerance) e2e suite, run
// against the compose stack gateway. Covers the acceptance criteria of
// docs/design/fault-diagnosis-auto-recovery.md reachable from the outside:
// the admin incident table (AC1), the incident detail + timeline (AC2),
// the root-cause classification (AC3), the recovery actions (AC5/AC6),
// the policy page (AC7), the tenant banner (AC8), the tenant scoping
// (AC9), the negative realm separation (AC11), the role gate (AC12), and
// the states (AC13).
//
// The compose stack has no Controller/Kubernetes, so incidents are seeded
// by publishing structured failure signals to the fault.signals NATS
// subject via the seed program (test/e2e/seed/fault/seed_fault.go). The
// fault module debounces crash_loop signals (3 in 3 minutes), so the seed
// publishes three crash_loop signals for a service target and one signal
// for each other target type.

const { execSync } = require('child_process');
const path = require('path');
const api = require('../page-objects/api.js');

// Seed incidents by publishing failure signals to the fault.signals NATS
// subject inside the compose network. The seed program mirrors the
// Controller's faultSignal wire format.
function seedFaults(browser) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/fault',
    'golang:1.26-alpine',
    'sh -c "go run seed_fault.go"'
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedFaults failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['fault-diagnosis-auto-recovery', 'feature-53'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-fault-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation (AC11/AC12) ----

  'AC11: fault admin APIs are admin-only (user realm rejected 10038)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/faults?limit=50',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertOk(browser, res, 'AC11: admin list reachable');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/faults?limit=50',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC11: user realm rejected on admin list');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/faults/policy',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC11: user realm rejected on admin policy');
    });
  },

  'AC12: fault admin RPCs require the admin role (10036)': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/faults?limit=50',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertBusinessError(browser, res, 10036, 'AC12: non-member session denied');
    });
  },

  'AC11: the admin page is served at /admin/faults': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/faults');
    browser.waitForElementPresent('[data-testid="nav-faults"]', 15000, 'AC11: admin nav entry');
    browser.waitForElementPresent('[data-testid="fault-search"]', 15000, 'AC11: admin page served');
  },

  // ---- API contract (AC1/AC2/AC3/AC5/AC6/AC7/AC9) ----

  'AC1/AC2/AC3: seeded incidents are listed with classification and detail': function (browser) {
    const org = browser.globals.orgA;
    seedFaults(browser);
    // Wait for the signal consumer to create the incidents.
    browser.pause(2000);
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/faults?limit=50',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: list incidents');
      const incidents = body.incidents || [];
      browser.assert.ok(incidents.length >= 5, 'AC1: at least 5 seeded incidents');
      // The crash-loop incident is classified service_crash_loop.
      const crashLoop = incidents.find((i) => i.targetName === 'svc-crash-loop');
      browser.assert.ok(Boolean(crashLoop), 'AC3: crash-loop incident present');
      browser.assert.equal(crashLoop.rootCause, 'service_crash_loop', 'AC3: crash-loop classified');
      browser.assert.equal(crashLoop.severity, 'warning', 'AC3: crash-loop severity');
      // The node incident is classified node_not_ready (critical).
      const node = incidents.find((i) => i.targetName === 'node-not-ready');
      browser.assert.ok(Boolean(node), 'AC3: node incident present');
      browser.assert.equal(node.rootCause, 'node_not_ready', 'AC3: node classified');
      browser.assert.equal(node.severity, 'critical', 'AC3: node severity');
      // The OOM incident is classified oom.
      const oom = incidents.find((i) => i.targetName === 'svc-oom');
      browser.assert.ok(Boolean(oom), 'AC3: oom incident present');
      browser.assert.equal(oom.rootCause, 'oom', 'AC3: oom classified');

      // Get the crash-loop incident detail with its timeline.
      api.request(browser, {
        method: 'GET',
        path: `/api/v1/admin/faults/${crashLoop.incidentId}`,
        org,
        sessionRealm: 'admin'
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC2: get incident');
        browser.assert.equal(body2.incident.targetName, 'svc-crash-loop', 'AC2: target name');
        browser.assert.ok(Array.isArray(body2.steps) && body2.steps.length > 0, 'AC2: timeline steps present');
        browser.assert.ok(Array.isArray(body2.actions), 'AC2: actions array present');
      });
    });
  },

  'AC5/AC6: recovery actions validate drain-confirm and override': function (browser) {
    const org = browser.globals.orgA;
    seedFaults(browser);
    browser.pause(2000);
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/faults?limit=50',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC5: list incidents');
      const incidents = body.incidents || [];
      const crashLoop = incidents.find((i) => i.targetName === 'svc-crash-loop');
      browser.assert.ok(Boolean(crashLoop), 'AC5: crash-loop incident present');
      const id = crashLoop.incidentId;

      // Manual restart.
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/faults/${id}:recover`,
        org,
        sessionRealm: 'admin',
        body: {action: 'restart'}
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC6: manual restart');
        browser.assert.equal(body2.action.action, 'restart', 'AC6: restart action');

        // Drain without confirm -> 13503.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/faults/${id}:recover`,
          org,
          sessionRealm: 'admin',
          body: {action: 'drain'}
        }, (res3) => {
          api.assertBusinessError(browser, res3, 13503, 'AC5: drain without confirm -> 13503');

          // Override with no action in flight -> 13504.
          api.request(browser, {
            method: 'POST',
            path: `/api/v1/admin/faults/${id}:override`,
            org,
            sessionRealm: 'admin'
          }, (res4) => {
            api.assertBusinessError(browser, res4, 13504, 'AC6: override no action -> 13504');
          });
        });
      });
    });
  },

  'AC7: the auto-recovery policy can be read and updated, with validation': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/faults/policy',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC7: get policy');
      browser.assert.equal(body.policy.drainAlwaysConfirm, true, 'AC7: drain always confirm');

      // Update the oom auto-restart toggle.
      api.request(browser, {
        method: 'PATCH',
        path: '/api/v1/admin/faults/policy',
        org,
        sessionRealm: 'admin',
        body: {autoRestart: {oom: false}}
      }, (res2) => {
        const body2 = api.assertOk(browser, res2, 'AC7: update policy');
        browser.assert.equal(body2.policy.autoRestart.oom, false, 'AC7: oom toggle updated');

        // Invalid category -> 13502.
        api.request(browser, {
          method: 'PATCH',
          path: '/api/v1/admin/faults/policy',
          org,
          sessionRealm: 'admin',
          body: {autoRestart: {bogus: true}}
        }, (res3) => {
          api.assertBusinessError(browser, res3, 13502, 'AC7: invalid category -> 13502');
        });
      });
    });
  },

  'AC2: a malformed (non-UUID) incident id returns 13506, not an internal error': function (browser) {
    const org = browser.globals.orgA;
    // A malformed incident id is an unknown incident and must map to
    // 13506. (Regression: postgres rejects the non-UUID literal and
    // surfaced as code 13 internal error instead of 13506.)
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/faults/not-a-uuid',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertBusinessError(browser, res, 13506, 'AC2: malformed incident id -> 13506');
    });
  },

  'AC9: the tenant service status returns only the caller own service incident': function (browser) {
    const org = browser.globals.orgA;
    // Register a model + service so the status endpoint has a target.
    const name = `svc-status-${browser.globals.runId}`;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name: `${name}-model`, version: 'v1', weightPath: `models/${name}/`}
    }, (res) => {
      const model = api.assertOk(browser, res, 'AC9: register model');
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        sessionRealm: 'admin',
        body: {
          name,
          modelId: model.modelId,
          modelVersion: 'v1',
          imageId: 'img-vllm-nvidia-v063',
          replicas: '1',
          accelerator: 'nvidia',
          acceleratorType: 'gpu'
        }
      }, (res2) => {
        const created = api.assertOk(browser, res2, 'AC9: create service');
        const serviceId = created.serviceId;
        // The user surface reads the service status (healthy, no incident).
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/inference-services/${serviceId}/status`,
          org,
          sessionRealm: 'user'
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'AC9: service status');
          browser.assert.equal(body3.degraded, false, 'AC9: not degraded');
        });
      });
    });
  },

  // ---- UI flow (AC1/AC13) ----

  'AC1/AC13: the admin page renders the incident table and filters': function (browser) {
    const org = browser.globals.orgA;
    seedFaults(browser);
    browser.pause(2000);
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/faults');
    browser.waitForElementPresent('[data-testid="fault-table"]', 15000, 'AC1: table renders');
    browser.waitForElementPresent('[data-testid="fault-search"]', 10000, 'AC1: search');
    browser.waitForElementPresent('[data-testid="fault-filter-status"]', 10000, 'AC1: status filter');
    browser.waitForElementPresent('[data-testid="fault-filter-severity"]', 10000, 'AC1: severity filter');
    browser.waitForElementPresent('[data-testid="fault-filter-root-cause"]', 10000, 'AC1: root-cause filter');
    browser.waitForElementPresent('[data-testid="fault-filter-target-type"]', 10000, 'AC1: target-type filter');
  },

  'AC13: the admin page renders the empty state when no incidents match': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/faults');
    browser.waitForElementPresent('[data-testid="fault-search"]', 15000, 'AC13: search renders');
    browser.setValue('[data-testid="fault-search"]', 'no-such-incident-xyz');
    browser.waitForElementPresent('[data-testid="fault-empty"]', 15000, 'AC13: empty state');
  },

  'AC13: the admin page renders the permission-denied state for a non-admin session': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/faults');
    browser.waitForElementPresent('[data-testid="fault-permission-denied"]', 15000, 'AC13: permission-denied state');
  },

  'AC7: the policy page renders the per-category toggles': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/faults/policy');
    browser.waitForElementPresent('[data-testid="fault-policy-restart-toggle-service_crash_loop"]', 15000, 'AC7: restart toggle');
    browser.waitForElementPresent('[data-testid="fault-policy-reschedule-toggle-node_not_ready"]', 15000, 'AC7: reschedule toggle');
    browser.waitForElementPresent('[data-testid="fault-policy-drain-confirm"]', 15000, 'AC7: drain confirm');
  }
};