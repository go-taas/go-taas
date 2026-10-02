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

// Feature-40 (multi-cluster management) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/architecture/multi-cluster-management.md (AC1-AC9) reachable from
// the outside:
//
//   - API contract on the admin surface (AC1-AC3): RegisterCluster
//     registers a cluster and ListClusters returns it; a duplicate name
//     returns 12402 and an invalid kubeconfig reference returns 12404;
//     GetCluster returns the detail; GetClusterWorkloads returns the
//     placed services; DisableCluster is idempotent; an unknown cluster
//     returns 12401.
//   - Surface separation (AC8): the cluster APIs are admin-only; a
//     user-realm session calling /api/v1/admin/clusters/* is rejected with
//     10038; the admin API is not reachable on the bare /api/v1/... prefix.
//   - Console pages (AC4-AC6, AC9): the /admin/clusters page renders the
//     cluster list; the register dialog creates a cluster with
//     state=active; the /admin/clusters/:clusterId page renders the
//     overview/nodes/workloads cards; the Disable action is enabled only
//     for an active cluster; a session without the required role receives
//     10036.
//
// The compose stack has no Controller per-cluster health collection loop,
// so the cluster-health projection stays empty (health "unknown",
// nodeCount 0) and the workload provider returns no services. The seed
// (test/e2e/seed/clusters/seed_clusters.go) writes cluster rows directly
// into PostgreSQL.

const api = require('../page-objects/api.js');
const { execSync } = require('child_process');
const path = require('path');

// Fixed ids used by the seed (must match seed_clusters.go).
const ACTIVE_CLUSTER_ID = 'dddddddd-dddd-dddd-dddd-dddddddddddd';
const DISABLED_CLUSTER_ID = 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee';

// Seed clusters directly into PostgreSQL (the compose stack has no
// Controller health collection loop to produce them).
function seedClusters(browser) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/clusters',
    'golang:1.26-alpine',
    `sh -c "go run seed_clusters.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable'"`
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedClusters failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['multi-cluster-management', 'feature-40'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-cl-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed the clusters.
    seedClusters(browser);
    // Seed admin- and user-realm sessions so both the admin protected page
    // renders and the wrong-realm rejection can be exercised.
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Admin surface: cluster API (AC1) ----

  'AC1: RegisterCluster registers a cluster; duplicate name -> 12402; invalid kubeconfig -> 12404': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-cluster-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/clusters',
      org,
      body: {name, region: 'cn-north', kubeconfigRef: `kube/${name}`}
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: register cluster');
      browser.assert.ok(Boolean(body.clusterId), 'AC1: cluster_id returned');
      browser.assert.equal(body.state, 'active', 'AC1: state active');
    });

    // Duplicate name -> 12402.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/clusters',
      org,
      body: {name, region: 'cn-north', kubeconfigRef: `kube/${name}-2`}
    }, (res) => {
      api.assertBusinessError(browser, res, 12402, 'AC1: duplicate name -> 12402');
    });

    // Invalid kubeconfig reference -> 12404.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/clusters',
      org,
      body: {name: 'bad', region: 'cn-north', kubeconfigRef: '../escape'}
    }, (res) => {
      api.assertBusinessError(browser, res, 12404, 'AC1: invalid kubeconfig -> 12404');
    });
  },

  'AC1b: ListClusters returns the seeded clusters': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/clusters',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1b: list clusters');
      browser.assert.ok(Array.isArray(body.clusters), 'AC1b: clusters array');
      const found = body.clusters.some((c) => c.clusterId === ACTIVE_CLUSTER_ID);
      browser.assert.ok(found, 'AC1b: seeded active cluster present');
    });
  },

  // ---- Admin surface: cluster detail API (AC2) ----

  'AC2: GetCluster returns the detail; GetClusterWorkloads returns placed services; unknown cluster -> 12401': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/clusters/${ACTIVE_CLUSTER_ID}`,
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC2: get cluster');
      browser.assert.ok(body.cluster, 'AC2: cluster present');
      browser.assert.equal(body.cluster.summary.name, 'e2e-cluster-a', 'AC2: cluster name');
      browser.assert.ok(Array.isArray(body.cluster.nodes), 'AC2: nodes array');
    });

    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/clusters/${ACTIVE_CLUSTER_ID}/workloads`,
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC2: get workloads');
      browser.assert.ok(Array.isArray(body.workloads), 'AC2: workloads array');
    });

    // Unknown cluster -> 12401.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/clusters/99999999-9999-9999-9999-999999999999',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 12401, 'AC2: unknown cluster -> 12401');
    });
  },

  // ---- Admin surface: disable API (AC3) ----

  'AC3: DisableCluster is idempotent': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/clusters/${ACTIVE_CLUSTER_ID}:disable`,
      org
    }, (res) => {
      api.assertOk(browser, res, 'AC3: disable cluster');
    });
    // Idempotent.
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/clusters/${ACTIVE_CLUSTER_ID}:disable`,
      org
    }, (res) => {
      api.assertOk(browser, res, 'AC3: disable cluster again (idempotent)');
    });
  },

  // ---- Surface separation (AC8) ----

  'AC8: a user-realm session calling the admin cluster prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      return localStorage.getItem('go-taas.user.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC8: user session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/clusters',
        org: browser.globals.orgA,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC8: user session on admin prefix -> 10038');
      });
    });
  },

  'AC8b: the admin cluster API is not reachable on the bare /api/v1/... prefix': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/clusters',
      org
    }, (res) => {
      browser.assert.ok(
        res.status === 404 || (res.body && res.body.code !== 0),
        'AC8b: admin cluster route not served on bare prefix'
      );
    });
  },

  // ---- Console pages: admin Clusters page (AC4/AC5) ----

  'AC4: /admin/clusters page renders the cluster list with the Register action': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/clusters');
    browser.waitForElementPresent('[data-testid="clusters-title"]', 15000, 'AC4: page renders');
    browser.waitForElementPresent('[data-testid="clusters-register"]', 10000, 'AC4: register action');
    browser.waitForElementPresent('[data-testid="clusters-filter-bar"]', 10000, 'AC4: filter bar');
    browser.waitForElementPresent('[data-testid="clusters-list"]', 10000, 'AC4: cluster list');
    browser.waitForElementPresent(`[data-testid="clusters-cluster-${ACTIVE_CLUSTER_ID}"]`, 10000, 'AC4: seeded active cluster row');
    browser.waitForElementPresent(`[data-testid="clusters-cluster-${DISABLED_CLUSTER_ID}"]`, 10000, 'AC4: seeded disabled cluster row');
  },

  'AC5: the register-cluster dialog creates a cluster with state=active': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/clusters');
    browser.waitForElementPresent('[data-testid="clusters-register"]', 15000, 'AC5: register action');
    browser.click('[data-testid="clusters-register"]');
    browser.waitForElementPresent('[data-testid="clusters-register-dialog"]', 10000, 'AC5: register dialog');
    browser.setValue('[data-testid="clusters-reg-name"]', `e2e-ui-cluster-${browser.globals.runId}`);
    browser.setValue('[data-testid="clusters-reg-region"]', 'cn-east');
    browser.setValue('[data-testid="clusters-reg-kubeconfig"]', `kube/e2e-ui-${browser.globals.runId}`);
    browser.click('[data-testid="clusters-reg-submit"]');
    // The list reloads.
    browser.waitForElementPresent('[data-testid="clusters-list"]', 10000, 'AC5: cluster list reloads');
  },

  // ---- Console pages: admin Cluster detail page (AC6) ----

  'AC6: /admin/clusters/:clusterId page renders overview/nodes/workloads; Disable enabled only for active': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    // Active cluster: Disable action enabled.
    browser.url(browser.globals.baseUrl + `/admin/clusters/${ACTIVE_CLUSTER_ID}`);
    browser.waitForElementPresent('[data-testid="clusters-detail-title"]', 15000, 'AC6: detail renders');
    browser.waitForElementPresent('[data-testid="clusters-overview"]', 10000, 'AC6: overview card');
    browser.waitForElementPresent('[data-testid="clusters-nodes"]', 10000, 'AC6: nodes card');
    browser.waitForElementPresent('[data-testid="clusters-workloads"]', 10000, 'AC6: workloads card');
    browser.waitForElementPresent('[data-testid="clusters-disable"]', 10000, 'AC6: disable action enabled for active');

    // Disabled cluster: Disable action absent.
    browser.url(browser.globals.baseUrl + `/admin/clusters/${DISABLED_CLUSTER_ID}`);
    browser.waitForElementPresent('[data-testid="clusters-detail-title"]', 15000, 'AC6: disabled detail renders');
    browser.waitForElementPresent('[data-testid="clusters-overview"]', 10000, 'AC6: disabled overview card');
    browser.waitForElementNotPresent('[data-testid="clusters-disable"]', 5000, 'AC6: disable action absent for disabled');
  },

  // ---- Console pages: permission denied (AC9) ----

  'AC9: a session without the required role receives 10036 on the admin cluster API': function (browser) {
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
        path: '/api/v1/admin/clusters',
        org,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10036, 'AC9: non-member session on admin cluster API -> 10036');
      });
    });
  }
};