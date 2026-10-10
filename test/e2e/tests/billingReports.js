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

// Feature-25 (billing reports & CSV export) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/billing-reports.md (AC1-AC14) reachable from the outside:
//
//   - API contract on the admin surface (AC1/AC2/AC3/AC4/AC5): CreateReport
//     returns a pending report; a range > 366 days or since > until returns
//     10404; an invalid dimension returns 10903; GetReport returns the
//     status/definition/row_count/data_through and an unknown report_id
//     returns 10901; DownloadReport returns the BOM CSV once ready and
//     10907 before ready; CreateSchedule/UpdateSchedule/DeleteSchedule and
//     ListScheduleRuns work; a duplicate name returns 10906 and an invalid
//     frequency returns 10905; an unknown schedule_id returns 10902.
//   - End-user surface (AC6): CreateReport on the user prefix is
//     tenant-scoped — it aggregates only the caller's org.
//   - Surface separation (AC12/AC13): admin pages call only
//     /api/v1/admin/billing/reports/*, user pages call only
//     /api/v1/billing/reports/*; a wrong-realm session is rejected with
//     10038.
//   - Console pages (AC7-AC11, AC14): the admin page renders the builder,
//     report history and schedule list; generating a report shows the
//     "Generating…" progress, polls until ready, then enables Download;
//     the empty states render; creating/deleting a schedule and View runs
//     work; the end-user page renders the tenant-scoped builder/history/
//     schedules with no org dropdown; a session without the required role
//     receives 10036.
//
// The compose stack has no controller and no interactive IdP login, so the
// suites authenticate with a seeded server-side session (see
// page-objects/api.js seedSession) and drive the real UI served by the
// compose stack. Charge records are seeded directly into PostgreSQL by
// test/e2e/seed/billingreports/seed_billingreports.go (the billing-reports
// module is a read-only aggregation over charge_records).

const api = require('../page-objects/api.js');
const { execSync } = require('child_process');
const path = require('path');

// Fixed model id used by the seed (must match seed_billingreports.go).
const MODEL_A = '11111111-1111-1111-1111-111111111111';

// Seed charge records for the given org directly into PostgreSQL (the
// billing-reports module is a read-only aggregation over charge_records,
// and the compose stack has no inference pipeline to produce them).
function seedBillingReports(browser, org, otherOrg) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const otherArg = otherOrg ? ` -other-org '${otherOrg}'` : '';
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/billingreports',
    'golang:1.26-alpine',
    `sh -c "go run seed_billingreports.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -org '${org}' -model '${MODEL_A}'${otherArg}"`
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedBillingReports failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['billing-reports', 'feature-25'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-br-${browser.globals.runId}-${browser.globals.testSeq}`;
    browser.globals.orgB = `org-e2e-br-b-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.ensureOrg(browser, browser.globals.orgB);
    // Seed charge records for orgA so the reports show data; orgB is the
    // other tenant whose usage must never appear in orgA's report.
    seedBillingReports(browser, browser.globals.orgA, browser.globals.orgB);
    // Seed admin- and user-realm sessions so both the admin and user
    // protected pages render.
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Admin surface: report API (AC1/AC2/AC3) ----

  'AC1: CreateReport returns pending; over-long range returns 10404; invalid dimension returns 10903': function (browser) {
    const org = browser.globals.orgA;
    const now = Math.floor(Date.now() / 1000);

    // Valid create on the admin surface.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/reports',
      org,
      body: {
        name: 'Monthly',
        dimension: 'REPORT_DIMENSION_MODEL',
        granularity: 'REPORT_GRANULARITY_DAILY',
        since: now - 7 * 24 * 3600,
        until: now
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: create report');
      browser.assert.equal(body.report.status, 'pending', 'AC1: status pending');
      browser.assert.ok(Boolean(body.report.reportId), 'AC1: report id present');
    });

    // Range > 366 days -> 10404.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/reports',
      org,
      body: {
        name: 'r',
        dimension: 'REPORT_DIMENSION_MODEL',
        granularity: 'REPORT_GRANULARITY_DAILY',
        since: 1000,
        until: 1000 + 400 * 24 * 3600
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10404, 'AC1: over-long range');
    });

    // Invalid dimension -> 10903.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/reports',
      org,
      body: {
        name: 'r',
        dimension: 'REPORT_DIMENSION_UNSPECIFIED',
        granularity: 'REPORT_GRANULARITY_DAILY'
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10903, 'AC1: invalid dimension');
    });
  },

  'AC2/AC3: GetReport returns status; DownloadReport returns BOM CSV once ready and 10907 before ready': function (browser) {
    const org = browser.globals.orgA;
    const now = Math.floor(Date.now() / 1000);

    // Create a report over the seeded charge records.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/reports',
      org,
      body: {
        name: 'Monthly',
        dimension: 'REPORT_DIMENSION_MODEL',
        granularity: 'REPORT_GRANULARITY_DAILY',
        since: now - 7 * 24 * 3600,
        until: now
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC2: create report');
      const reportId = body.report.reportId;

      // Download before ready -> 10907.
      api.request(browser, {
        method: 'GET',
        path: `/api/v1/admin/billing/reports/${reportId}/download`,
        org
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10907, 'AC2: download before ready');
      });

      // Poll GetReport until ready (the generator runner runs on a 5s
      // tick in the compose stack).
      const poll = (attempts) => {
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/billing/reports/${reportId}`,
          org
        }, (res3) => {
          const g = api.assertOk(browser, res3, 'AC2: get report');
          if (g.report.status === 'ready') {
            browser.assert.ok(parseInt(g.report.rowCount, 10) >= 1, 'AC2: row count >= 1');
            browser.assert.ok(g.report.dataThrough !== undefined, 'AC2: data_through present');

            // Download returns the BOM CSV.
            api.request(browser, {
              method: 'GET',
              path: `/api/v1/admin/billing/reports/${reportId}/download`,
              org
            }, (res4) => {
              const d = api.assertOk(browser, res4, 'AC3: download');
              browser.assert.ok(d.csv.indexOf('\uFEFF') === 0, 'AC3: CSV has UTF-8 BOM');
              browser.assert.ok(d.csv.indexOf('bucket') !== -1, 'AC3: CSV has header row');
              browser.assert.ok(d.csv.indexOf(MODEL_A) !== -1, 'AC3: CSV contains the model');
            });
          } else if (attempts > 0) {
            browser.pause(3000);
            poll(attempts - 1);
          } else {
            browser.assert.fail('AC2: report did not become ready in time');
          }
        });
      };
      poll(10);
    });
  },

  'AC4/AC5: schedule lifecycle and runs; duplicate name 10906, invalid frequency 10905, unknown schedule 10902': function (browser) {
    const org = browser.globals.orgA;

    // Invalid frequency -> 10905.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/reports/schedules',
      org,
      body: {
        name: 's',
        dimension: 'REPORT_DIMENSION_MODEL',
        relativeRange: 'RELATIVE_RANGE_LAST_7_DAYS',
        granularity: 'REPORT_GRANULARITY_DAILY',
        frequency: 'REPORT_FREQUENCY_UNSPECIFIED'
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10905, 'AC4: invalid frequency');
    });

    // Valid create.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/reports/schedules',
      org,
      body: {
        name: 'Weekly',
        dimension: 'REPORT_DIMENSION_MODEL',
        relativeRange: 'RELATIVE_RANGE_LAST_7_DAYS',
        granularity: 'REPORT_GRANULARITY_DAILY',
        frequency: 'REPORT_FREQUENCY_WEEKLY'
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC4: create schedule');
      const scheduleId = body.schedule.scheduleId;
      browser.assert.equal(body.schedule.status, 'active', 'AC4: schedule active');

      // Duplicate name -> 10906.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/billing/reports/schedules',
        org,
        body: {
          name: 'Weekly',
          dimension: 'REPORT_DIMENSION_MODEL',
          relativeRange: 'RELATIVE_RANGE_LAST_7_DAYS',
          granularity: 'REPORT_GRANULARITY_DAILY',
          frequency: 'REPORT_FREQUENCY_WEEKLY'
        }
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10906, 'AC4: duplicate name');
      });

      // Update.
      api.request(browser, {
        method: 'PATCH',
        path: `/api/v1/admin/billing/reports/schedules/${scheduleId}`,
        org,
        body: { name: 'Monthly', frequency: 'REPORT_FREQUENCY_MONTHLY' }
      }, (res3) => {
        const u = api.assertOk(browser, res3, 'AC4: update schedule');
        browser.assert.equal(u.schedule.name, 'Monthly', 'AC4: updated name');
      });

      // ListScheduleRuns (the schedule runner creates a run on its tick).
      api.request(browser, {
        method: 'GET',
        path: `/api/v1/admin/billing/reports/schedules/${scheduleId}/runs`,
        org
      }, (res4) => {
        const runs = api.assertOk(browser, res4, 'AC5: list runs');
        browser.assert.ok(Array.isArray(runs.runs), 'AC5: runs array present');
      });

      // Unknown schedule -> 10902.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/billing/reports/schedules/nope/runs',
        org
      }, (res5) => {
        api.assertBusinessError(browser, res5, 10902, 'AC5: unknown schedule');
      });

      // Delete.
      api.request(browser, {
        method: 'DELETE',
        path: `/api/v1/admin/billing/reports/schedules/${scheduleId}`,
        org
      }, (res6) => {
        api.assertOk(browser, res6, 'AC4: delete schedule');
      });
    });
  },

  // ---- End-user surface: tenant-scoped (AC6) ----

  'AC6: user-surface report is tenant-scoped and never leaks the other org': function (browser) {
    const org = browser.globals.orgA;
    const now = Math.floor(Date.now() / 1000);

    // Create a report on the user prefix for orgA.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/billing/reports',
      org,
      body: {
        name: 'r',
        dimension: 'REPORT_DIMENSION_MODEL',
        granularity: 'REPORT_GRANULARITY_DAILY',
        since: now - 7 * 24 * 3600,
        until: now
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC6: user create report');
      const reportId = body.report.reportId;

      // Poll until ready.
      const poll = (attempts) => {
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/billing/reports/${reportId}`,
          org
        }, (res2) => {
          const g = api.assertOk(browser, res2, 'AC6: get report');
          if (g.report.status === 'ready') {
            api.request(browser, {
              method: 'GET',
              path: `/api/v1/billing/reports/${reportId}/download`,
              org
            }, (res3) => {
              const d = api.assertOk(browser, res3, 'AC6: download');
              // The report contains orgA's model but NOT orgB's usage.
              browser.assert.ok(d.csv.indexOf(MODEL_A) !== -1, 'AC6: contains orgA model');
              browser.assert.ok(d.csv.indexOf('999') === -1, 'AC6: does not contain other-org usage');
            });
          } else if (attempts > 0) {
            browser.pause(3000);
            poll(attempts - 1);
          } else {
            browser.assert.fail('AC6: report did not become ready in time');
          }
        });
      };
      poll(10);
    });
  },

  // ---- Surface separation (AC12/AC13) ----

  'AC12: a user-realm session calling the admin billing-reports prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      const token = localStorage.getItem('go-taas.user.session-token');
      return token;
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC12: user session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/billing/reports',
        org: browser.globals.orgA,
        headers: { Authorization: `Bearer ${token}` }
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC12: user session on admin prefix -> 10038');
      });
    });
  },

  'AC13: an admin-realm session calling the user billing-reports prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      const token = localStorage.getItem('go-taas.admin.session-token');
      return token;
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC13: admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/billing/reports',
        org: browser.globals.orgA,
        headers: { Authorization: `Bearer ${token}` }
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC13: admin session on user prefix -> 10038');
      });
    });
  },

  // ---- Console pages: admin (AC7/AC8/AC9/AC10) ----

  'AC7: admin page renders builder, report history and schedule list': function (browser) {
    // The admin schedule list is fleet-wide and every other test in
    // this suite deletes the schedule it creates, so seed one via the
    // API first to make the list deterministically non-empty (AC7
    // asserts the populated rendering, not the empty state — AC9 does).
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/billing/reports/schedules',
      org: browser.globals.orgA,
      body: {
        name: `ac7-${browser.globals.runId}`,
        dimension: 'REPORT_DIMENSION_MODEL',
        relativeRange: 'RELATIVE_RANGE_LAST_7_DAYS',
        granularity: 'REPORT_GRANULARITY_DAILY',
        frequency: 'REPORT_FREQUENCY_WEEKLY'
      }
    }, (seedRes) => {
      const seeded = api.assertOk(browser, seedRes, 'AC7: seed schedule');
      const seededId = seeded.schedule.scheduleId;

      browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${browser.globals.orgA}')`);
      browser.url(browser.globals.baseUrl + '/admin/billing/reports');

      browser.waitForElementPresent('[data-testid="billing-reports-page"]', 15000, 'AC7: page renders');
      browser.waitForElementPresent('[data-testid="report-builder"]', 10000, 'AC7: report builder');
      browser.waitForElementPresent('[data-testid="report-name"]', 10000, 'AC7: report name field');
      browser.waitForElementPresent('[data-testid="report-dimension-model"]', 10000, 'AC7: dimension radio');
      browser.waitForElementPresent('[data-testid="report-org"]', 10000, 'AC7: org dropdown');
      browser.waitForElementPresent('[data-testid="report-granularity-daily"]', 10000, 'AC7: granularity radio');
      browser.waitForElementPresent('[data-testid="report-timezone"]', 10000, 'AC7: timezone dropdown');
      browser.waitForElementPresent('[data-testid="generate-report"]', 10000, 'AC7: generate button');
      browser.waitForElementPresent('[data-testid="reports-table"]', 10000, 'AC7: reports table');
      browser.waitForElementPresent('[data-testid="reports-refresh"]', 10000, 'AC7: refresh button');

      // Switch to the Schedules tab.
      browser.click('[data-testid="schedules-tab"]');
      browser.waitForElementPresent('[data-testid="schedule-builder"]', 10000, 'AC7: schedule builder');
      browser.waitForElementPresent('[data-testid="schedule-name"]', 10000, 'AC7: schedule name field');
      browser.waitForElementPresent('[data-testid="schedule-relative-range"]', 10000, 'AC7: relative range');
      browser.waitForElementPresent('[data-testid="schedule-frequency-weekly"]', 10000, 'AC7: frequency radio');
      browser.waitForElementPresent('[data-testid="create-schedule"]', 10000, 'AC7: create schedule button');
      browser.waitForElementPresent('[data-testid="schedules-table"]', 10000, 'AC7: schedules table');

      // Remove the seeded schedule so the suite leaves no residue.
      api.request(browser, {
        method: 'DELETE',
        path: `/api/v1/admin/billing/reports/schedules/${seededId}`,
        org: browser.globals.orgA
      }, (delRes) => {
        api.assertOk(browser, delRes, 'AC7: seeded schedule removed');
      });
    });
  },

  'AC8: generating a report shows progress, polls until ready, then enables Download': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/billing/reports');
    browser.waitForElementPresent('[data-testid="report-builder"]', 15000, 'AC8: builder renders');

    // Fill the builder and generate.
    browser.setValue('[data-testid="report-name"]', 'E2E Report');
    browser.click('[data-testid="report-dimension-model"]');
    browser.click('[data-testid="report-granularity-daily"]');
    browser.click('[data-testid="generate-report"]');

    // The report appears in the history with a Generating progress state.
    browser.waitForElementPresent('[data-testid="reports-table"]', 15000, 'AC8: reports table');
    browser.waitForElementPresent('[data-testid^="report-row-"]', 15000, 'AC8: a report row appears');

    // The generator runner (5s tick) marks the report ready; wait for the
    // download button to become enabled (not disabled).
    browser.waitForElementPresent('[data-testid^="report-download-"]:not([disabled])', 30000, 'AC8: download enabled when ready');
  },

  'AC9: empty states render for a fresh org': function (browser) {
    // The admin surface is fleet-wide (its report history spans all
    // orgs), so the empty state is reliably observable only on the
    // org-scoped end-user surface. Use orgB, which has no reports or
    // schedules, on the end-user page.
    api.seedSession(browser, 'user', browser.globals.orgB);
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${browser.globals.orgB}')`);
    browser.url(browser.globals.baseUrl + '/billing/reports');

    browser.waitForElementPresent('[data-testid="billing-reports-page"]', 15000, 'AC9: page renders');
    browser.waitForElementPresent('[data-testid="reports-empty"]', 15000, 'AC9: reports empty state');

    browser.click('[data-testid="schedules-tab"]');
    browser.waitForElementPresent('[data-testid="schedules-empty"]', 15000, 'AC9: schedules empty state');
  },

  'AC10: creating and deleting a schedule works; View runs shows the run history': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/billing/reports');
    browser.waitForElementPresent('[data-testid="billing-reports-page"]', 15000, 'AC10: page renders');

    // Switch to Schedules and create one.
    browser.click('[data-testid="schedules-tab"]');
    browser.waitForElementPresent('[data-testid="schedule-builder"]', 10000, 'AC10: schedule builder');
    browser.setValue('[data-testid="schedule-name"]', 'E2E Schedule');
    browser.click('[data-testid="schedule-frequency-weekly"]');
    browser.click('[data-testid="create-schedule"]');

    // The schedule appears in the list. Capture its id from the row
    // testid so we can assert this specific schedule is removed after
    // deletion (the admin schedule list is fleet-wide, so the whole list
    // is not empty even after deleting this schedule).
    browser.waitForElementPresent('[data-testid^="schedule-row-"]', 15000, 'AC10: schedule row appears');
    browser.getAttribute('[data-testid^="schedule-row-"]', 'data-testid', (result) => {
      const rowTestId = result.value;
      const scheduleId = rowTestId.replace('schedule-row-', '');
      browser.globals.ac10ScheduleId = scheduleId;
    });

    // View runs opens the drawer.
    browser.click('[data-testid^="schedule-runs-"]');
    browser.waitForElementPresent('[data-testid="schedule-runs-drawer"]', 10000, 'AC10: runs drawer');
    browser.waitForElementPresent('[data-testid="runs-empty"]', 10000, 'AC10: runs empty state');

    // Close the runs drawer (click the backdrop near its edge, away from
    // the dialog panel) so it no longer covers the schedule row's delete
    // button.
    browser.moveToElement('.dialog-backdrop', 5, 5).mouseButtonClick(0);
    browser.waitForElementNotPresent('[data-testid="schedule-runs-drawer"]', 10000, 'AC10: runs drawer closed');

    // Delete the schedule.
    browser.click('[data-testid^="schedule-delete-"]');
    browser.waitForElementPresent('[data-testid="delete-schedule-dialog"]', 10000, 'AC10: delete dialog');
    browser.click('[data-testid="confirm-delete-schedule"]');
    // The specific schedule row is removed (the fleet-wide admin list may
    // still show other orgs' schedules).
    browser.waitForElementNotPresent(`[data-testid="schedule-row-${browser.globals.ac10ScheduleId}"]`, 15000, 'AC10: schedule removed');
  },

  // ---- Console pages: end-user (AC11) ----

  'AC11: end-user page renders tenant-scoped builder/history/schedules with no org dropdown': function (browser) {
    // Re-seed the user session for orgA: AC9 re-seeded it for orgB, and
    // the user surface resolves the org from the session (not the
    // X-Organization-Id header), so without this the page would be scoped
    // to orgB.
    api.seedSession(browser, 'user', browser.globals.orgA);

    // Create a report for orgA on the user surface and block until it is
    // ready (single executeAsync that polls), so the tenant-scoped
    // history table renders when the page loads.
    const now = Math.floor(Date.now() / 1000);
    browser.executeAsync(function (org, since, until, done) {
      const token = localStorage.getItem('go-taas.user.session-token');
      const headers = { 'Content-Type': 'application/json', 'X-Organization-Id': org, Authorization: 'Bearer ' + token };
      fetch('/api/v1/billing/reports', {
        method: 'POST',
        headers,
        body: JSON.stringify({
          name: 'AC11 report',
          dimension: 'REPORT_DIMENSION_MODEL',
          granularity: 'REPORT_GRANULARITY_DAILY',
          since: String(since),
          until: String(until)
        })
      }).then((r) => r.json()).then((body) => {
        const reportId = body.report && body.report.reportId;
        const poll = (attempts) => {
          fetch('/api/v1/billing/reports/' + reportId, { headers: { 'X-Organization-Id': org, Authorization: 'Bearer ' + token } })
            .then((r) => r.json())
            .then((g) => {
              if (g.report && g.report.status === 'ready') done({ ok: true });
              else if (attempts > 0) setTimeout(() => poll(attempts - 1), 3000);
              else done({ ok: false, status: g.report && g.report.status });
            })
            .catch((e) => done({ err: String(e) }));
        };
        poll(10);
      }).catch((e) => done({ err: String(e) }));
    }, [browser.globals.orgA, now - 7 * 24 * 3600, now], (result) => {
      browser.assert.ok(result.value && result.value.ok, 'AC11: report ready before page load');
    });

    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/billing/reports');

    browser.waitForElementPresent('[data-testid="billing-reports-page"]', 15000, 'AC11: user page renders');
    browser.waitForElementPresent('[data-testid="report-builder"]', 10000, 'AC11: report builder');
    browser.waitForElementPresent('[data-testid="report-name"]', 10000, 'AC11: report name field');
    // No org dropdown on the user surface.
    browser.assert.not.elementPresent('[data-testid="report-org"]', 'AC11: no org dropdown');
    browser.waitForElementPresent('[data-testid="reports-table"]', 10000, 'AC11: reports table');

    // Switch to Schedules; no org dropdown there either.
    browser.click('[data-testid="schedules-tab"]');
    browser.waitForElementPresent('[data-testid="schedule-builder"]', 10000, 'AC11: schedule builder');
    browser.assert.not.elementPresent('[data-testid="schedule-org"]', 'AC11: no schedule org dropdown');
  },

  // ---- Console pages: permission denied (AC14) ----

  'AC14: session without the required role receives 10036 on the admin billing-reports API': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, { noMember: true });
    browser.execute(function () {
      const token = localStorage.getItem('go-taas.admin.session-token');
      return token;
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC14: non-member admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/billing/reports',
        org,
        headers: { Authorization: `Bearer ${token}` }
      }, (res) => {
        api.assertBusinessError(browser, res, 10036, 'AC14: non-member session on admin billing-reports API -> 10036');
      });
    });
  }
};