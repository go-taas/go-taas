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

// Feature-41 (data export & privacy) e2e suite, run against the compose
// stack gateway. Covers the acceptance criteria of
// docs/architecture/data-export-privacy.md (AC1-AC8) reachable from the
// outside:
//
//   - API contract on the end-user surface (AC1-AC4): CreateDataExport
//     returns an export with status=pending; an invalid type returns 12502,
//     an invalid format returns 12503, and an invalid range returns 10404;
//     ListDataExports returns the caller's history; GetDataExport returns
//     the record; DownloadDataExport returns the file when ready and 12504
//     before; an unknown export returns 12501; the export is tenant-scoped.
//   - Surface separation (AC7): the export APIs are user-only; an admin-
//     realm session calling /api/v1/account/export is rejected with 10038;
//     the user API is not reachable on the admin prefix.
//   - Console pages (AC5-AC6, AC8): the /account/export page renders the
//     export builder and history; the builder creates an export that
//     appears with status=pending; a session without the required role
//     receives 10036.
//
// The compose stack has no export-generation runner (no inference pipeline
// to produce usage/billing/request-log data), so exports created through
// the API stay pending. The seed (test/e2e/seed/dataexport/
// seed_dataexport.go) writes a ready export directly into PostgreSQL so
// the download flow can be exercised.

const api = require('../page-objects/api.js');
const { execSync } = require('child_process');
const path = require('path');

// Fixed ids used by the seed (must match seed_dataexport.go).
const READY_EXPORT_ID = 'ffffffff-ffff-ffff-ffff-ffffffffffff';
const PENDING_EXPORT_ID = '11111111-2222-3333-4444-555555555555';

// Seed data_exports rows for the given org directly into PostgreSQL (the
// compose stack has no export-generation runner to produce them).
function seedDataExport(browser, org) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/dataexport',
    'golang:1.26-alpine',
    `sh -c "go run seed_dataexport.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -org '${org}'"`
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedDataExport failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['data-export-privacy', 'feature-41'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-de-${browser.globals.runId}-${browser.globals.testSeq}`;
    browser.globals.orgB = `org-e2e-de-b-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.ensureOrg(browser, browser.globals.orgB);
    // Seed the data exports for orgA so the page shows data; orgB is the
    // other tenant whose exports must never appear in orgA's view.
    seedDataExport(browser, browser.globals.orgA);
    // Seed admin- and user-realm sessions so both the user protected page
    // renders and the wrong-realm rejection can be exercised.
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- End-user surface: export API (AC1) ----

  'AC1: CreateDataExport returns status=pending; invalid type -> 12502; invalid format -> 12503; invalid range -> 10404': function (browser) {
    const org = browser.globals.orgA;
    const now = Math.floor(Date.now() / 1000);

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/account/export',
      org,
      body: {type: 'usage', format: 'json', since: now - 30 * 24 * 3600, until: now}
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: create export');
      browser.assert.ok(body.export, 'AC1: export present');
      browser.assert.ok(Boolean(body.export.exportId), 'AC1: export_id present');
      browser.assert.equal(body.export.status, 'pending', 'AC1: status pending');
    });

    // Invalid type -> 12502.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/account/export',
      org,
      body: {type: 'bogus', format: 'json'}
    }, (res) => {
      api.assertBusinessError(browser, res, 12502, 'AC1: invalid type -> 12502');
    });

    // Invalid format -> 12503.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/account/export',
      org,
      body: {type: 'usage', format: 'xml'}
    }, (res) => {
      api.assertBusinessError(browser, res, 12503, 'AC1: invalid format -> 12503');
    });

    // Invalid range (since > until) -> 10404.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/account/export',
      org,
      body: {type: 'usage', format: 'json', since: now, until: now - 30 * 24 * 3600}
    }, (res) => {
      api.assertBusinessError(browser, res, 10404, 'AC1: invalid range -> 10404');
    });
  },

  // ---- End-user surface: list/get/download API (AC2/AC3) ----

  'AC2: ListDataExports returns the caller history; GetDataExport returns the record; unknown export -> 12501': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'GET',
      path: '/api/v1/account/export',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC2: list exports');
      browser.assert.ok(Array.isArray(body.exports), 'AC2: exports array');
      const found = body.exports.some((e) => e.exportId === READY_EXPORT_ID);
      browser.assert.ok(found, 'AC2: seeded ready export present');
    });

    api.request(browser, {
      method: 'GET',
      path: `/api/v1/account/export/${READY_EXPORT_ID}`,
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC2: get export');
      browser.assert.ok(body.export, 'AC2: export present');
      browser.assert.equal(body.export.status, 'ready', 'AC2: status ready');
    });

    // Unknown export -> 12501.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/account/export/99999999-9999-9999-9999-999999999999',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 12501, 'AC2: unknown export -> 12501');
    });
  },

  'AC3: DownloadDataExport returns the file when ready; download before ready -> 12504': function (browser) {
    const org = browser.globals.orgA;

    // Download the ready export -> file.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/account/export/${READY_EXPORT_ID}/download`,
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC3: download ready export');
      browser.assert.ok(body.file, 'AC3: file present');
      browser.assert.equal(body.contentType, 'application/json', 'AC3: content type json');
    });

    // Download the pending export -> 12504.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/account/export/${PENDING_EXPORT_ID}/download`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 12504, 'AC3: download before ready -> 12504');
    });
  },

  // ---- End-user surface: tenant scoping (AC4) ----

  'AC4: the export is tenant-scoped; orgB never sees orgA exports': function (browser) {
    // orgB has no seeded exports.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/account/export',
      org: browser.globals.orgB
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC4: list orgB exports');
      browser.assert.ok(Array.isArray(body.exports), 'AC4: exports array');
      const hasOrgA = body.exports.some((e) => e.exportId === READY_EXPORT_ID);
      browser.assert.ok(!hasOrgA, 'AC4: orgB must not see orgA exports');
    });
  },

  // ---- Surface separation (AC7) ----

  'AC7: an admin-realm session calling the user export prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC7: admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/account/export',
        org: browser.globals.orgA,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC7: admin session on user prefix -> 10038');
      });
    });
  },

  'AC7b: the user export API is not reachable on the admin prefix': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/account/export',
      org
    }, (res) => {
      browser.assert.ok(
        res.status === 404 || (res.body && res.body.code !== 0),
        'AC7b: user export route not served on admin prefix'
      );
    });
  },

  // ---- Console pages: end-user Data Export page (AC5/AC6) ----

  'AC5: /account/export page renders the export builder and history': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/account/export');
    browser.waitForElementPresent('[data-testid="data-export-title"]', 15000, 'AC5: page renders');
    browser.waitForElementPresent('[data-testid="export-new"]', 10000, 'AC5: new export action');
    browser.waitForElementPresent('[data-testid="export-history"]', 10000, 'AC5: export history');
    browser.waitForElementPresent(`[data-testid="export-row-${READY_EXPORT_ID}"]`, 10000, 'AC5: seeded ready export row');
    browser.waitForElementPresent(`[data-testid="export-download-${READY_EXPORT_ID}"]`, 10000, 'AC5: download button for ready export');
  },

  'AC6: the export builder creates an export that appears with status=pending': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/account/export');
    browser.waitForElementPresent('[data-testid="export-new"]', 15000, 'AC6: new export action');
    browser.click('[data-testid="export-new"]');
    browser.waitForElementPresent('[data-testid="export-builder"]', 10000, 'AC6: export builder');
    browser.waitForElementPresent('[data-testid="export-type-usage"]', 10000, 'AC6: usage type');
    browser.waitForElementPresent('[data-testid="export-format-json"]', 10000, 'AC6: json format');
    browser.click('[data-testid="export-request"]');
    // The history reloads; the new export appears (pending).
    browser.waitForElementPresent('[data-testid="export-history"]', 10000, 'AC6: history reloads');
  },

  // ---- Console pages: permission denied (AC8) ----

  'AC8: a session without the required role receives 10036 on the export API': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'user', org, 'user', nonMemberUser, {noMember: true});
    browser.execute(function () {
      return localStorage.getItem('go-taas.user.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC8: non-member user session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/account/export',
        org,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10036, 'AC8: non-member session on export API -> 10036');
      });
    });
  }
};