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

// Feature-10 (org members, roles & invitations / RBAC) e2e suite, run
// against the compose stack gateway. Covers the acceptance criteria of
// docs/design/org-members-rbac.md reachable from the outside: the
// Members and Invitations pages render (AC10/AC11), and the member and
// invitation management APIs require an authenticated session (10027)
// since the RoleGuard resolves the caller from the session. The full
// member/invitation lifecycle is covered by the FVT suite.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['org-members-rbac', 'feature-10'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-rbac-${browser.globals.runId}-${browser.globals.testSeq}`;
    // The org-scoped APIs validate the org header against the
    // organizations table, so the org id must exist first.
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed an admin-realm session so the protected pages render (feature:
    // unauthenticated pages redirect to login).
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC9 (surface separation): a user session cannot call admin member APIs': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/tenancy/organizations/${org}/members`,
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC9: wrong realm rejected');
    });
  },

  'AC10: members page renders': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/members');
    browser.waitForElementPresent(
      '[data-testid="add-member-button"]',
      10000,
      'AC10: add-member button renders'
    );
    // The roster renders (empty state for a fresh org, or the table).
    browser.waitForElementPresent(
      '[data-testid="members-empty"], [data-testid="org-members-table"]',
      10000,
      'AC10: members list renders'
    );
  },

  'AC11: invitations page renders': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/invitations');
    browser.waitForElementPresent(
      '[data-testid="invite-button"]',
      10000,
      'AC11: invite button renders'
    );
    browser.waitForElementPresent(
      '[data-testid="invitations-empty"], [data-testid="invitations-table"]',
      10000,
      'AC11: invitations list renders'
    );
  },

  'AC10: member management requires a session (10027)': function (browser) {
    const org = browser.globals.orgA;
    // Without a session, the RoleGuard cannot resolve the caller.
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/tenancy/organizations/${org}/members`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10027, 'AC10: members list requires session');
    });
  },

  'AC11: invitation management requires a session (10027)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/tenancy/organizations/${org}/invitations`,
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10027, 'AC11: invitations list requires session');
    });
  }
};