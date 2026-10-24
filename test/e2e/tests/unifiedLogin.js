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

// Feature-22 (unified login & role-based routing) e2e suite, run against
// the compose stack gateway. Covers the acceptance criteria of
// docs/design/unified-login-role-routing.md (AC1-AC14): the Keycloak
// provider + admin/admin user seeded on compose startup, the TaaS custom
// login page, role-based landing, and the surface switch buttons.
//
// The compose stack seeds the Keycloak provider (issuer
// http://keycloak:8080/realms/go-taas) and the admin/admin user with the
// admin role. The custom login page authenticates via the OIDC password
// grant (Direct Access Grants), which the realm-export.json enables.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['unified-login', 'feature-22'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.orgA = `org-e2e-ul-${browser.globals.runId}`;
    api.ensureOrg(browser, browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // AC1: the login page lists the seeded Keycloak provider.
  'AC1: login page lists the Keycloak provider': function (browser) {
    browser.url(browser.globals.baseUrl + '/login');
    browser.waitForElementPresent('[data-testid="sso-login-keycloak"]', 15000, 'AC1: sso-login-keycloak button');
  },

  // AC2: selecting the Keycloak provider navigates to the custom login
  // page, not the Keycloak-hosted page.
  'AC2: selecting Keycloak navigates to the custom login page': function (browser) {
    browser.url(browser.globals.baseUrl + '/login');
    browser.waitForElementPresent('[data-testid="sso-login-keycloak"]', 15000, 'AC2: provider button');
    browser.click('[data-testid="sso-login-keycloak"]');
    browser.waitForElementPresent('[data-testid="custom-login-form"]', 15000, 'AC2: custom login form');
    browser.assert.urlContains('/login/keycloak', 'AC2: navigated to /login/keycloak');
    browser.assert.not.urlContains('keycloak:8080', 'AC2: did not redirect to the Keycloak-hosted page');
  },

  // AC3: submitting admin/admin authenticates, stores the admin token,
  // and lands on /admin/models.
  'AC3: admin/admin lands on the admin console': function (browser) {
    browser.url(browser.globals.baseUrl + '/login/keycloak');
    browser.waitForElementPresent('[data-testid="custom-login-username"]', 15000, 'AC3: username field');
    browser.setValue('[data-testid="custom-login-username"]', 'admin');
    browser.setValue('[data-testid="custom-login-password"]', 'admin');
    browser.click('[data-testid="custom-login-submit"]');
    browser.waitForElementPresent('[data-testid="admin-shell"]', 20000, 'AC3: admin shell');
    browser.assert.urlContains('/admin/models', 'AC3: landed on /admin/models');
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      browser.assert.ok(Boolean(result.value), 'AC3: admin session token stored');
    });
  },

  // AC4: a non-admin user's credentials land on the user console.
  'AC4: non-admin user lands on the user console': function (browser) {
    browser.url(browser.globals.baseUrl + '/login/keycloak');
    browser.waitForElementPresent('[data-testid="custom-login-username"]', 15000, 'AC4: username field');
    browser.setValue('[data-testid="custom-login-username"]', 'alice');
    browser.setValue('[data-testid="custom-login-password"]', 'alice-password');
    browser.click('[data-testid="custom-login-submit"]');
    browser.waitForElementPresent('[data-testid="user-shell"]', 20000, 'AC4: user shell');
    browser.assert.urlContains('/usage', 'AC4: landed on /usage');
    browser.execute(function () {
      return localStorage.getItem('go-taas.user.session-token');
    }, [], (result) => {
      browser.assert.ok(Boolean(result.value), 'AC4: user session token stored');
    });
  },

  // AC5: invalid credentials show the inline error and stay on the page.
  'AC5: invalid credentials show the inline error': function (browser) {
    browser.url(browser.globals.baseUrl + '/login/keycloak');
    browser.waitForElementPresent('[data-testid="custom-login-username"]', 15000, 'AC5: username field');
    browser.setValue('[data-testid="custom-login-username"]', 'admin');
    browser.setValue('[data-testid="custom-login-password"]', 'wrong-password');
    browser.click('[data-testid="custom-login-submit"]');
    browser.waitForElementPresent('[data-testid="custom-login-error"]', 20000, 'AC5: inline error');
    browser.assert.containsText('[data-testid="custom-login-error"]', 'The username or password is incorrect.', 'AC5: error copy');
    browser.assert.urlContains('/login/keycloak', 'AC5: stays on /login/keycloak');
  },

  // AC6: the submit button is disabled until both fields are non-empty.
  'AC6: submit is disabled until both fields are non-empty': function (browser) {
    browser.url(browser.globals.baseUrl + '/login/keycloak');
    browser.waitForElementPresent('[data-testid="custom-login-submit"]', 15000, 'AC6: submit button');
    browser.assert.attributeEquals('[data-testid="custom-login-submit"]', 'disabled', 'true', 'AC6: disabled when empty');
    browser.setValue('[data-testid="custom-login-username"]', 'admin');
    browser.assert.attributeEquals('[data-testid="custom-login-submit"]', 'disabled', 'true', 'AC6: disabled with only username');
    browser.setValue('[data-testid="custom-login-password"]', 'admin');
    browser.assert.not.attributeEquals('[data-testid="custom-login-submit"]', 'disabled', 'true', 'AC6: enabled with both fields');
  },

  // AC7: an admin on the user console sees switch-to-admin and it works.
  'AC7: admin sees and uses switch-to-admin': function (browser) {
    // Seed an admin-role user session on the user console.
    api.seedSession(browser, 'user', browser.globals.orgA);
    browser.url(browser.globals.baseUrl + '/usage');
    browser.waitForElementPresent('[data-testid="switch-to-admin"]', 15000, 'AC7: switch-to-admin button');
    browser.click('[data-testid="switch-to-admin"]');
    browser.waitForElementPresent('[data-testid="admin-shell"]', 20000, 'AC7: admin shell after switch');
    browser.assert.urlContains('/admin/models', 'AC7: navigated to /admin/models');
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      browser.assert.ok(Boolean(result.value), 'AC7: admin token stored after switch');
    });
  },

  // AC8: a non-admin user does not see switch-to-admin.
  'AC8: non-admin does not see switch-to-admin': function (browser) {
    // Seed a non-admin-role user session on the user console.
    api.seedSession(browser, 'user', browser.globals.orgA, 'member');
    browser.url(browser.globals.baseUrl + '/usage');
    browser.waitForElementPresent('[data-testid="user-shell"]', 15000, 'AC8: user shell');
    browser.assert.not.elementPresent('[data-testid="switch-to-admin"]', 'AC8: no switch-to-admin for non-admin');
  },

  // AC9: the admin console offers switch-to-user and it works.
  'AC9: admin console offers switch-to-user': function (browser) {
    api.seedSession(browser, 'admin', browser.globals.orgA);
    browser.url(browser.globals.baseUrl + '/admin/models');
    browser.waitForElementPresent('[data-testid="switch-to-user"]', 15000, 'AC9: switch-to-user button');
    browser.click('[data-testid="switch-to-user"]');
    browser.waitForElementPresent('[data-testid="user-shell"]', 20000, 'AC9: user shell after switch');
    browser.assert.urlContains('/usage', 'AC9: navigated to /usage');
    browser.execute(function () {
      return localStorage.getItem('go-taas.user.session-token');
    }, [], (result) => {
      browser.assert.ok(Boolean(result.value), 'AC9: user token stored after switch');
    });
  },

  // AC10: an admin who signs in at /login lands on /admin/models.
  'AC10: admin at /login lands on /admin/models': function (browser) {
    browser.url(browser.globals.baseUrl + '/login/keycloak');
    browser.waitForElementPresent('[data-testid="custom-login-username"]', 15000, 'AC10: username field');
    browser.setValue('[data-testid="custom-login-username"]', 'admin');
    browser.setValue('[data-testid="custom-login-password"]', 'admin');
    browser.click('[data-testid="custom-login-submit"]');
    browser.waitForElementPresent('[data-testid="admin-shell"]', 20000, 'AC10: admin shell');
    browser.assert.urlContains('/admin/models', 'AC10: landed on /admin/models');
  },

  // AC11: a non-admin who signs in at /login lands on /usage.
  'AC11: non-admin at /login lands on /usage': function (browser) {
    browser.url(browser.globals.baseUrl + '/login/keycloak');
    browser.waitForElementPresent('[data-testid="custom-login-username"]', 15000, 'AC11: username field');
    browser.setValue('[data-testid="custom-login-username"]', 'alice');
    browser.setValue('[data-testid="custom-login-password"]', 'alice-password');
    browser.click('[data-testid="custom-login-submit"]');
    browser.waitForElementPresent('[data-testid="user-shell"]', 20000, 'AC11: user shell');
    browser.assert.urlContains('/usage', 'AC11: landed on /usage');
  },

  // AC12: the user token is stored under go-taas.user.session-token and
  // the admin token under go-taas.admin.session-token.
  'AC12: tokens are stored under the realm-matching keys': function (browser) {
    // Clear any user token left by an earlier test in this browser
    // profile so the assertion that an admin login stores no user token
    // is meaningful.
    browser.execute(function () {
      localStorage.removeItem('go-taas.user.session-token');
      localStorage.removeItem('go-taas.admin.session-token');
    });
    browser.url(browser.globals.baseUrl + '/login/keycloak');
    browser.waitForElementPresent('[data-testid="custom-login-username"]', 15000, 'AC12: username field');
    browser.setValue('[data-testid="custom-login-username"]', 'admin');
    browser.setValue('[data-testid="custom-login-password"]', 'admin');
    browser.click('[data-testid="custom-login-submit"]');
    browser.waitForElementPresent('[data-testid="admin-shell"]', 20000, 'AC12: admin shell');
    browser.execute(function () {
      return {
        admin: localStorage.getItem('go-taas.admin.session-token'),
        user: localStorage.getItem('go-taas.user.session-token')
      };
    }, [], (result) => {
      browser.assert.ok(Boolean(result.value.admin), 'AC12: admin token under go-taas.admin.session-token');
      browser.assert.ok(!result.value.user, 'AC12: no user token stored for an admin login');
    });
  },

  // AC13: a disabled provider is not listed on the login page. The
  // public projection (GET /api/v1/auth/sso/providers) filters enabled
  // providers, so a provider disabled after creation must not render a
  // sign-in button.
  'AC13: disabled provider is not listed on the login page': function (browser) {
    const providerId = `disabled-${browser.globals.runId}`;
    api.ensureSSOProvider(browser, providerId, {
      type: 'oidc', displayName: 'Disabled IdP',
      issuer: 'https://idp.example.com', clientId: 'c1', clientSecret: 'secret',
      redirectUri: 'https://console.example.com/callback',
      attribute_mapping: '{"username":"preferred_username","email":"email"}'
    });
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/auth/sso/providers/${providerId}:disable`,
      org: browser.globals.orgA
    }, (res) => {
      api.assertOk(browser, res, 'AC13: disable provider');
    });
    browser.url(browser.globals.baseUrl + '/login');
    browser.waitForElementPresent('[data-testid="sso-login-list"]', 15000, 'AC13: provider list');
    browser.assert.not.elementPresent(`[data-testid="sso-login-${providerId}"]`, 'AC13: disabled provider not listed');
  },

  // AC14: the custom login page footer states credentials are verified
  // by the provider and not stored by go-taas.
  'AC14: custom login footer states credentials are not stored': function (browser) {
    browser.url(browser.globals.baseUrl + '/login/keycloak');
    browser.waitForElementPresent('[data-testid="custom-login-form"]', 15000, 'AC14: custom login form');
    browser.assert.containsText('body', 'not stored by go-taas', 'AC14: footer copy');
  }
};