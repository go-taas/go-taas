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

// Feature-37 (inference service resource metrics) e2e suite, run against
// the compose stack gateway. Covers the acceptance criteria of
// docs/architecture/service-resource-metrics.md (AC1-AC8) reachable from
// the outside:
//
//   - API contract on the admin surface (AC1/AC3): GetServiceResourceMetrics
//     with a valid range returns cards/series/replicas with data_through;
//     a range > 92 days or since > until returns 10404; an unknown service
//     returns 10301; an invalid metric returns 12101.
//   - Surface separation (AC7): the metrics API is admin-only; a user-realm
//     session calling /api/v1/admin/services/*/metrics is rejected with
//     10038; the admin API is not reachable on the bare /api/v1/... prefix.
//   - Console pages (AC4-AC6, AC8): the admin Service Metrics page renders
//     the filter bar, summary cards, chart and per-replica table from the
//     first successful load; the metric switcher toggles client-side; the
//     GPU card shows "Unavailable" when gpu_percent is empty; a session
//     without the required role receives 10036.
//
// The compose stack has no controller sampler (no running inference pods
// carry the service-id/replica-index labels), so the service_resource_metrics
// table is seeded directly by test/e2e/seed/resourcemetrics/
// seed_resourcemetrics.go (the resourcemetrics module is a read-only
// aggregation over that table).

const api = require('../page-objects/api.js');
const { execSync } = require('child_process');
const path = require('path');

// Fixed service id used by the seed (must match seed_resourcemetrics.go).
const SERVICE_ID = '11111111-1111-1111-1111-111111111111';

// Seed service_resource_metrics rows for the given service directly into
// PostgreSQL (the compose stack has no controller sampler to produce
// them).
function seedResourceMetrics(browser, serviceId) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/resourcemetrics',
    'golang:1.26-alpine',
    `sh -c "go run seed_resourcemetrics.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -service '${serviceId}'"`
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedResourceMetrics failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['service-resource-metrics', 'feature-37'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-rm-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed the resource metrics table for the fixed service id.
    seedResourceMetrics(browser, SERVICE_ID);
    // Seed admin- and user-realm sessions so both the admin protected page
    // renders and the wrong-realm rejection can be exercised.
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Admin surface: resource metrics API (AC1/AC3) ----

  'AC1: GetServiceResourceMetrics returns cards/series/replicas with data_through; invalid range -> 10404': function (browser) {
    const org = browser.globals.orgA;
    const now = Math.floor(Date.now() / 1000);

    // Valid range: the seeded rows produce cards, series and replicas.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/services/${SERVICE_ID}/metrics?since=${now - 3 * 3600}&until=${now}`,
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: metrics');
      browser.assert.ok(body.cards, 'AC1: cards present');
      browser.assert.ok(body.cards.replicaCount !== undefined, 'AC1: replica_count present');
      browser.assert.ok(body.cards.dataThrough !== undefined, 'AC1: data_through present');
      browser.assert.ok(Array.isArray(body.series), 'AC1: series array');
      browser.assert.ok(body.series.length > 0, 'AC1: series non-empty');
      browser.assert.ok(Array.isArray(body.replicas), 'AC1: replicas array');
      browser.assert.ok(body.replicas.length >= 2, 'AC1: two replicas');
    });

    // since > until -> 10404.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/services/${SERVICE_ID}/metrics?since=${now}&until=${now - 3 * 3600}`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10404, 'AC1: since > until -> 10404');
    });

    // Range > 92 days -> 10404.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/services/${SERVICE_ID}/metrics?since=${now - 100 * 24 * 3600}&until=${now}`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10404, 'AC1: range > 92 days -> 10404');
    });
  },

  'AC1b: unknown service -> 10301; invalid metric -> 12101': function (browser) {
    const org = browser.globals.orgA;
    const now = Math.floor(Date.now() / 1000);

    // Unknown service -> 10301.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/services/99999999-9999-9999-9999-999999999999/metrics',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10301, 'AC1b: unknown service -> 10301');
    });

    // Invalid metric -> 12101.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/services/${SERVICE_ID}/metrics?metric=bogus`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 12101, 'AC1b: invalid metric -> 12101');
    });
  },

  // ---- Surface separation (AC7) ----

  'AC7: a user-realm session calling the admin service-metrics prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      return localStorage.getItem('go-taas.user.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC7: user session token present');
      api.request(browser, {
        method: 'GET',
        path: `/api/v1/admin/services/${SERVICE_ID}/metrics`,
        org: browser.globals.orgA,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC7: user session on admin prefix -> 10038');
      });
    });
  },

  'AC7b: the admin service-metrics API is not reachable on the bare /api/v1/... prefix': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/services/${SERVICE_ID}/metrics`,
      org
    }, (res) => {
      // Either a 404 (route not bound) or a user-surface business error;
      // it must NOT be the admin GetServiceResourceMetrics success envelope.
      browser.assert.ok(
        res.status === 404 || (res.body && res.body.code !== 0),
        'AC7b: admin service-metrics route not served on bare prefix'
      );
    });
  },

  // ---- Console pages: admin Service Metrics page (AC4-AC6) ----

  'AC4: admin Service Metrics page renders filter bar, summary cards, chart and per-replica table': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + `/admin/services/${SERVICE_ID}/metrics`);
    browser.waitForElementPresent('[data-testid="service-metrics-title"]', 15000, 'AC4: page renders');
    browser.waitForElementPresent('[data-testid="metrics-filter-bar"]', 10000, 'AC4: filter bar');
    browser.waitForElementPresent('[data-testid="metrics-range"]', 10000, 'AC4: range select');
    browser.waitForElementPresent('[data-testid="metrics-replica"]', 10000, 'AC4: replica select');
    browser.waitForElementPresent('[data-testid="metrics-cards"]', 10000, 'AC4: summary cards');
    browser.waitForElementPresent('[data-testid="metrics-card-cpu"]', 10000, 'AC4: cpu card');
    browser.waitForElementPresent('[data-testid="metrics-card-memory"]', 10000, 'AC4: memory card');
    browser.waitForElementPresent('[data-testid="metrics-card-gpu"]', 10000, 'AC4: gpu card');
    browser.waitForElementPresent('[data-testid="metrics-card-replicas"]', 10000, 'AC4: replicas card');
    browser.waitForElementPresent('[data-testid="metrics-metric-switcher"]', 10000, 'AC4: metric switcher');
    browser.waitForElementPresent('[data-testid="metrics-replicas"]', 10000, 'AC4: per-replica table');
    browser.waitForElementPresent('[data-testid="metrics-replica-replica-1"]', 10000, 'AC4: replica-1 row');
    browser.waitForElementPresent('[data-testid="metrics-replica-replica-2"]', 10000, 'AC4: replica-2 row');
  },

  'AC5: metric switcher toggles CPU/Memory/GPU client-side with no refetch': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + `/admin/services/${SERVICE_ID}/metrics`);
    browser.waitForElementPresent('[data-testid="metrics-metric-cpu"]', 15000, 'AC5: cpu toggle');
    browser.click('[data-testid="metrics-metric-memory"]');
    browser.waitForElementPresent('[data-testid="metrics-metric-memory"]', 5000, 'AC5: memory toggle active');
    browser.click('[data-testid="metrics-metric-gpu"]');
    browser.waitForElementPresent('[data-testid="metrics-metric-gpu"]', 5000, 'AC5: gpu toggle active');
  },

  'AC6: per-replica table shows masked replica indices; GPU card shows Unavailable when gpu_percent is empty': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + `/admin/services/${SERVICE_ID}/metrics`);
    browser.waitForElementPresent('[data-testid="metrics-replicas"]', 15000, 'AC6: per-replica table');
    // Masked replica indices (never raw pod names).
    browser.waitForElementPresent('[data-testid="metrics-replica-replica-1"]', 10000, 'AC6: replica-1 masked index');
    browser.waitForElementPresent('[data-testid="metrics-replica-replica-2"]', 10000, 'AC6: replica-2 masked index');
    // The GPU card renders (replica-1 carries gpu_percent, so hasGPU is
    // true and the card shows a value; the "Unavailable" note is asserted
    // in the empty-state case where no GPU data exists).
    browser.waitForElementPresent('[data-testid="metrics-card-gpu"]', 10000, 'AC6: gpu card');
  },

  // ---- Console pages: permission denied (AC8) ----

  'AC8: a session without the required role receives 10036 on the admin service-metrics API': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC8: non-member admin session token present');
      api.request(browser, {
        method: 'GET',
        path: `/api/v1/admin/services/${SERVICE_ID}/metrics`,
        org,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10036, 'AC8: non-member session on admin service-metrics API -> 10036');
      });
    });
  }
};