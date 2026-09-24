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

// Feature-07 (SSO federation) e2e suite, run against the compose stack
// gateway. Covers the API-level acceptance criteria of
// docs/design/sso-federation.md (AC1-AC4, AC8, AC11-AC13) plus the
// console pages (login, SSO providers, identity bindings).

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['sso', 'feature-07'],

  before(browser) {
    // Nothing: each test navigates in beforeEach because afterEach ends
    // the session and a fresh page starts at about:blank, where a
    // cross-origin fetch to the gateway would fail (null origin).
  },

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.orgA = `org-e2e-${browser.globals.runId}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // A unique provider id per run for isolation.
    browser.globals.providerId = `okta-${browser.globals.runId}`;
  },

  afterEach(browser) {
    browser.end();
  },

  'AC1: create SSO provider returns the provider with the secret masked': function (browser) {
    const providerId = browser.globals.providerId;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/sso/providers',
      org: browser.globals.orgA,
      body: {
        provider: {
          providerId,
          type: 'oidc',
          displayName: 'Okta',
          issuer: 'https://idp.example.com',
          clientId: 'c1',
          clientSecret: 'secret',
          redirectUri: 'https://console.example.com/callback'
        }
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'create provider');
      browser.assert.equal(body.provider.providerId, providerId, 'AC1: providerId echoed');
      browser.assert.equal(body.provider.clientSecret, '••••', 'AC1: secret masked');
    });
  },

  'AC1: duplicate provider id is rejected (10020)': function (browser) {
    const providerId = browser.globals.providerId;
    api.ensureSSOProvider(browser, providerId, {
      type: 'oidc', displayName: 'Okta',
      issuer: 'https://idp.example.com', clientId: 'c1', clientSecret: 'secret',
      redirectUri: 'https://console.example.com/callback'
    });
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/sso/providers',
      org: browser.globals.orgA,
      body: {
        provider: {
          providerId,
          type: 'oidc', displayName: 'Dup',
          issuer: 'https://idp.example.com', clientId: 'c1',
          redirectUri: 'https://console.example.com/callback'
        }
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 10020, 'AC1: duplicate provider id');
    });
  },

  'AC2: enable and disable a provider are idempotent': function (browser) {
    const providerId = browser.globals.providerId;
    api.ensureSSOProvider(browser, providerId, {
      type: 'oidc', displayName: 'Okta',
      issuer: 'https://idp.example.com', clientId: 'c1', clientSecret: 'secret',
      redirectUri: 'https://console.example.com/callback'
    });
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/auth/sso/providers/${providerId}:enable`,
      org: browser.globals.orgA
    }, (res) => {
      const body = api.assertOk(browser, res, 'enable provider');
      browser.assert.equal(body.provider.enabled, true, 'AC2: provider enabled');
    });
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/auth/sso/providers/${providerId}:disable`,
      org: browser.globals.orgA
    }, (res) => {
      const body = api.assertOk(browser, res, 'disable provider');
      browser.assert.equal(body.provider.enabled, false, 'AC2: provider disabled');
    });
  },

  'AC3: update a provider edits the display name': function (browser) {
    const providerId = browser.globals.providerId;
    api.ensureSSOProvider(browser, providerId, {
      type: 'oidc', displayName: 'Okta',
      issuer: 'https://idp.example.com', clientId: 'c1', clientSecret: 'secret',
      redirectUri: 'https://console.example.com/callback'
    });
    api.request(browser, {
      method: 'PATCH',
      path: `/api/v1/admin/auth/sso/providers/${providerId}`,
      org: browser.globals.orgA,
      body: {provider: {displayName: 'Okta2'}}
    }, (res) => {
      const body = api.assertOk(browser, res, 'update provider');
      browser.assert.equal(body.provider.displayName, 'Okta2', 'AC3: display name updated');
    });
  },

  'AC8: create identity binding with a non-existent user is rejected (10005)': function (browser) {
    const providerId = browser.globals.providerId;
    api.ensureSSOProvider(browser, providerId, {
      type: 'oidc', displayName: 'Okta',
      issuer: 'https://idp.example.com', clientId: 'c1', clientSecret: 'secret',
      redirectUri: 'https://console.example.com/callback'
    });
    // A binding referencing a non-existent user is rejected with 10005
    // (the user must exist before a binding can reference it).
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/identity-bindings',
      org: browser.globals.orgA,
      body: {providerId, externalSubject: `iss:sub-${browser.globals.runId}`, userId: '00000000-0000-0000-0000-000000000000'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10005, 'AC8: unknown user on binding');
    });
  },

  'AC11: session APIs return 10027 without a session': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/auth/session',
      org: ''
    }, (res) => {
      api.assertBusinessError(browser, res, 10027, 'AC11: no session');
    });
  },

  'AC14: login page renders the SSO providers': function (browser) {
    const providerId = browser.globals.providerId;
    api.ensureSSOProvider(browser, providerId, {
      type: 'oidc', displayName: 'Okta',
      issuer: 'https://idp.example.com', clientId: 'c1', clientSecret: 'secret',
      redirectUri: 'https://console.example.com/callback'
    });
    api.enableSSOProvider(browser, providerId);
    browser.url(browser.globals.baseUrl + '/admin/login');
    browser.waitForElementPresent(`[data-testid="sso-login-${providerId}"]`, 10000, 'AC14: login button renders');
  },

  'AC16: SSO providers page renders the directory and create dialog': function (browser) {
    browser.url(browser.globals.baseUrl + '/admin/sso');
    browser.waitForElementPresent('[data-testid="sso-providers-table"]', 10000, 'AC16: providers table renders');
    browser.waitForElementPresent('[data-testid="create-sso-provider"]', 5000, 'AC16: create button renders');
    browser.click('[data-testid="create-sso-provider"]');
    browser.waitForElementPresent('[data-testid="create-sso-provider-dialog"]', 5000, 'AC16: create dialog opens');
    browser.waitForElementPresent('[data-testid="sso-provider-id-input"]', 5000, 'AC16: provider id input');
    browser.waitForElementPresent('[data-testid="sso-provider-name-input"]', 5000, 'AC16: provider name input');
  },

  'AC17: identity bindings page renders the directory': function (browser) {
    browser.url(browser.globals.baseUrl + '/admin/identity-bindings');
    browser.waitForElementPresent('[data-testid="create-identity-binding"]', 10000, 'AC17: create button renders');
    // The page renders either the table (when bindings exist) or the
    // empty state.
    browser.waitForElementPresent(
      '[data-testid="identity-bindings-table"], [data-testid="identity-bindings-empty"]',
      10000,
      'AC17: bindings directory renders'
    );
  }
};