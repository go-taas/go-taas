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

// Feature-15 (audit logging & activity export) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/audit-logging.md reachable from the outside: the admin
// audit log (AC4/AC5) and export (AC6), the end-user activity view
// (AC7), and the console pages (AC10/AC11).

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['audit-logging', 'feature-15'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-audit-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC4/AC5: admin audit log lists recorded events': function (browser) {
    const org = browser.globals.orgA;

    // A mutation that records an audit event: revoke an API key.
    // First create one, then revoke it.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org,
      body: {name: `audit-key-${browser.globals.testSeq}`}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create api key');
      const keyId = body.keyId;
      browser.assert.ok(keyId, 'key id returned');

      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/auth/api-keys/${keyId}:revoke`,
        org,
        body: {}
      }, (res2) => {
        api.assertOk(browser, res2, 'revoke api key');

        // The audit trail now has the revoke event.
        api.request(browser, {
          method: 'GET',
          path: '/api/v1/admin/audit/events?page.limit=100',
          org
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'AC4: list audit events');
          browser.assert.ok(Array.isArray(body3.auditEvents), 'AC4: events array');
          const revoke = body3.auditEvents.find((e) => e.action === 'api_key.revoke');
          browser.assert.ok(revoke, 'AC4: api_key.revoke recorded');
          browser.assert.equal(revoke.result, 'AUDIT_RESULT_SUCCESS', 'AC4: success result');

          // AC5: drill into one event.
          api.request(browser, {
            method: 'GET',
            path: `/api/v1/admin/audit/events/${revoke.auditEventId}`,
            org
          }, (res4) => {
            const body4 = api.assertOk(browser, res4, 'AC5: get audit event');
            browser.assert.equal(body4.auditEvent.action, 'api_key.revoke', 'AC5: detail action');
          });
        });
      });
    });
  },

  'AC6: audit export returns CSV': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/audit/events:export?format=AUDIT_EXPORT_FORMAT_CSV',
      org: browser.globals.orgA
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC6: export CSV');
      browser.assert.ok(
        typeof body.content === 'string' && body.content.startsWith('audit_event_id,'),
        'AC6: CSV has a header row'
      );
    });
  },

  'AC7: end-user activity view is reachable': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/audit/activity?page.limit=100',
      org: browser.globals.orgA
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC7: my activity');
      browser.assert.ok(Array.isArray(body.auditEvents), 'AC7: activity array');
    });
  },

  'AC10: admin audit logs page renders': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/audit-logs');
    browser.waitForElementPresent('[data-testid="audit-logs-table"], [data-testid="audit-logs-empty"]', 15000, 'AC10: audit logs page renders');
  },

  'AC11: end-user activity page renders': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/activity');
    browser.waitForElementPresent('[data-testid="activity-table"], [data-testid="activity-empty"]', 15000, 'AC11: activity page renders');
  }
};
