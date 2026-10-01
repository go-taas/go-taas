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

// Feature-33 (inference service logs viewer) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/architecture/service-logs-viewer.md (AC1-AC9) reachable from the
// outside:
//
//   - API contract on the admin surface (AC1/AC2): ListServiceLogPods
//     validates the service (unknown -> 10301); GetServiceLogs validates
//     tail/since (invalid tail or malformed since -> 10404) and the
//     service (unknown -> 10301). The compose stack has no controller
//     kubeconfig, so the LogFetcher is not wired and the log RPCs fail
//     closed with 10301 for a valid service (the full log-fetch flow is
//     covered by the FVT with a fake LogFetcher).
//   - Surface separation (AC8): the log APIs are admin-only; a user-realm
//     session calling /api/v1/admin/services/*/logs is rejected with
//     10038; the admin API is not reachable on the bare /api/v1/...
//     prefix.
//   - Console pages (AC4, AC9): the admin Service Logs page renders the
//     filter bar and the log pane from first load; a session without the
//     required role receives 10036.
//
// The compose stack has no controller and no interactive IdP login, so the
// suites authenticate with a seeded server-side session (see
// page-objects/api.js seedSession) and drive the real UI served by the
// compose stack. Inference services are created through the real admin
// API.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['service-logs-viewer', 'feature-33'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-sl-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed admin- and user-realm sessions so both the admin protected page
    // renders and the wrong-realm rejection can be exercised.
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Admin surface: pod enumeration API (AC1) ----

  'AC1: ListServiceLogPods validates the service; unknown service -> 10301': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-sl-${browser.globals.runId}-${browser.globals.testSeq}`;

    // Register a model and create a service.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const modelId = api.assertOk(browser, res, 'AC1: register model').modelId;
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name: `svc-${browser.globals.runId}-${browser.globals.testSeq}`,
          modelId,
          modelVersion: 'v1',
          imageId: 'img-vllm-nvidia-v063',
          replicas: '2',
          accelerator: 'nvidia',
          acceleratorType: 'gpu'
        }
      }, (res2) => {
        const serviceId = api.assertOk(browser, res2, 'AC1: create service').serviceId;
        browser.assert.ok(Boolean(serviceId), 'AC1: serviceId returned');

        // The compose stack has no controller kubeconfig, so the LogFetcher
        // is not wired and the RPC fails closed with 10301 for a valid
        // service (the full log-fetch flow is covered by the FVT with a
        // fake LogFetcher).
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/services/${serviceId}/logs/pods`,
          org
        }, (res3) => {
          api.assertBusinessError(browser, res3, 10301, 'AC1: valid service, no kubeconfig -> 10301');
        });
      });
    });

    // Unknown service -> 10301.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/services/99999999-9999-9999-9999-999999999999/logs/pods',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10301, 'AC1: unknown service -> 10301');
    });
  },

  // ---- Admin surface: bounded log fetch API (AC2) ----

  'AC2: GetServiceLogs validates tail/since; invalid tail or malformed since -> 10404': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-sl-logs-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const modelId = api.assertOk(browser, res, 'AC2: register model').modelId;
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name: `svc-logs-${browser.globals.runId}-${browser.globals.testSeq}`,
          modelId,
          modelVersion: 'v1',
          imageId: 'img-vllm-nvidia-v063',
          replicas: '2',
          accelerator: 'nvidia',
          acceleratorType: 'gpu'
        }
      }, (res2) => {
        const serviceId = api.assertOk(browser, res2, 'AC2: create service').serviceId;

        // Invalid tail (> 5000) -> 10404.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/services/${serviceId}/logs?pod=replica-1&tail=99999`,
          org
        }, (res3) => {
          api.assertBusinessError(browser, res3, 10404, 'AC2: invalid tail -> 10404');
        });

        // Malformed since -> 10404.
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/services/${serviceId}/logs?pod=replica-1&since=not-a-time`,
          org
        }, (res4) => {
          api.assertBusinessError(browser, res4, 10404, 'AC2: malformed since -> 10404');
        });

        // Valid request on the compose stack (no kubeconfig) fails closed
        // with 10301 (the full log-fetch flow is covered by the FVT).
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/services/${serviceId}/logs?pod=replica-1&tail=500`,
          org
        }, (res5) => {
          api.assertBusinessError(browser, res5, 10301, 'AC2: valid request, no kubeconfig -> 10301');
        });
      });
    });

    // Unknown service -> 10301.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/services/99999999-9999-9999-9999-999999999999/logs?pod=replica-1',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10301, 'AC2: unknown service -> 10301');
    });
  },

  // ---- Surface separation (AC8) ----

  'AC8: a user-realm session calling the admin service-logs prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      return localStorage.getItem('go-taas.user.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC8: user session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/services/99999999-9999-9999-9999-999999999999/logs/pods',
        org: browser.globals.orgA,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC8: user session on admin prefix -> 10038');
      });
    });
  },

  'AC8b: the admin service-logs API is not reachable on the bare /api/v1/... prefix': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/services/99999999-9999-9999-9999-999999999999/logs/pods',
      org
    }, (res) => {
      // Either a 404 (route not bound) or a user-surface business error;
      // it must NOT be the admin ListServiceLogPods success envelope.
      browser.assert.ok(
        res.status === 404 || (res.body && res.body.code !== 0),
        'AC8b: admin service-logs route not served on bare prefix'
      );
    });
  },

  // ---- Console pages: admin Service Logs page (AC4) ----

  'AC4: admin Service Logs page renders the filter bar and log pane': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-sl-page-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/models',
      org,
      body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
    }, (res) => {
      const modelId = api.assertOk(browser, res, 'AC4: register model').modelId;
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/inference-services',
        org,
        body: {
          name: `svc-page-${browser.globals.runId}-${browser.globals.testSeq}`,
          modelId,
          modelVersion: 'v1',
          imageId: 'img-vllm-nvidia-v063',
          replicas: '2',
          accelerator: 'nvidia',
          acceleratorType: 'gpu'
        }
      }, (res2) => {
        const serviceId = api.assertOk(browser, res2, 'AC4: create service').serviceId;
        browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
        browser.url(browser.globals.baseUrl + `/admin/services/${serviceId}/logs`);
        browser.waitForElementPresent('[data-testid="service-logs-title"]', 15000, 'AC4: page renders');
        browser.waitForElementPresent('[data-testid="logs-filter-bar"]', 10000, 'AC4: filter bar');
        browser.waitForElementPresent('[data-testid="logs-replica-select"]', 10000, 'AC4: replica select');
        browser.waitForElementPresent('[data-testid="logs-level-select"]', 10000, 'AC4: level select');
        browser.waitForElementPresent('[data-testid="logs-range-select"]', 10000, 'AC4: range select');
        browser.waitForElementPresent('[data-testid="logs-follow-toggle"]', 10000, 'AC4: follow toggle');
        browser.waitForElementPresent('[data-testid="logs-search-input"]', 10000, 'AC4: search input');
        // The compose stack has no kubeconfig, so the pod enumeration fails
        // closed (10301). The page must surface the error state with a
        // Retry button instead of hanging on the loading spinner (SL-1,
        // design §5.3 error state). Assert the error banner and Retry
        // button render.
        browser.waitForElementPresent('[data-testid="error-banner"]', 10000, 'AC4: error banner on load failure');
        browser.waitForElementPresent('[data-testid="logs-retry"]', 10000, 'AC4: retry button on load failure');
      });
    });
  },

  // ---- Console pages: permission denied (AC9) ----

  'AC9: a session without the required role receives 10036 on the admin service-logs API': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC9: non-member admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/services/99999999-9999-9999-9999-999999999999/logs/pods',
        org,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10036, 'AC9: non-member session on admin service-logs API -> 10036');
      });
    });
  }
};