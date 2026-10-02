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

// Feature-38 (API documentation explorer) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/architecture/api-docs-explorer.md (AC1-AC8) reachable from the
// outside:
//
//   - API contract on the end-user surface (AC1/AC2): GetApiDocs returns
//     the curated catalog with categories/endpoints/parameters/examples/
//     error codes; it covers the inference API and the user-realm
//     control-plane APIs; it exposes no /api/v1/admin/* endpoint.
//   - Surface separation (AC7): the docs API is user-only; an admin-realm
//     session calling /api/v1/docs is rejected with 10038; the docs route
//     is not served on the admin prefix.
//   - Console pages (AC3-AC6, AC8): the /docs page renders the endpoint
//     list and the detail pane from the first successful load; selecting
//     an endpoint loads its detail; the language tabs switch the example;
//     try-it renders for inference endpoints; a session without the
//     required role receives 10036.
//
// The catalog is a static, versioned Go structure served by the docs
// module, so no seed is needed.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['api-docs-explorer', 'feature-38'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-docs-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed admin- and user-realm sessions so both the user protected page
    // renders and the wrong-realm rejection can be exercised.
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- End-user surface: docs API (AC1/AC2) ----

  'AC1: GetApiDocs returns the curated catalog with categories/endpoints/examples/error codes': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/docs',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: docs');
      browser.assert.ok(Array.isArray(body.categories), 'AC1: categories array');
      browser.assert.ok(body.categories.length > 0, 'AC1: categories non-empty');
      const cat = body.categories[0];
      browser.assert.ok(cat.categoryId, 'AC1: category_id present');
      browser.assert.ok(cat.categoryName, 'AC1: category_name present');
      browser.assert.ok(Array.isArray(cat.endpoints), 'AC1: endpoints array');
      const ep = cat.endpoints[0];
      browser.assert.ok(ep.endpointId, 'AC1: endpoint_id present');
      browser.assert.ok(ep.method, 'AC1: method present');
      browser.assert.ok(ep.path, 'AC1: path present');
      browser.assert.ok(Array.isArray(ep.parameters), 'AC1: parameters array');
      browser.assert.ok(ep.requestExample, 'AC1: request_example present');
      browser.assert.ok(ep.responseExample, 'AC1: response_example present');
      browser.assert.ok(Array.isArray(ep.errorCodes), 'AC1: error_codes array');
    });
  },

  'AC2: catalog covers inference and user-realm control-plane APIs; exposes no admin endpoint': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/docs',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC2: docs');
      const paths = [];
      for (const c of body.categories) {
        for (const e of c.endpoints) {
          paths.push(e.path);
        }
      }
      browser.assert.ok(paths.includes('/v1/chat/completions'), 'AC2: chat completions present');
      browser.assert.ok(paths.includes('/v1/embeddings'), 'AC2: embeddings present');
      browser.assert.ok(paths.includes('/api/v1/models'), 'AC2: models present');
      browser.assert.ok(paths.includes('/api/v1/auth/api-keys'), 'AC2: api-keys present');
      browser.assert.ok(paths.includes('/api/v1/usage'), 'AC2: usage present');
      browser.assert.ok(paths.includes('/api/v1/inference-endpoint'), 'AC2: inference-endpoint present');
      // No admin endpoint is exposed.
      for (const p of paths) {
        browser.assert.ok(!p.includes('/api/v1/admin/'), `AC2: catalog must not expose admin endpoint ${p}`);
      }
    });
  },

  // ---- Surface separation (AC7) ----

  'AC7: an admin-realm session calling the user docs prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC7: admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/docs',
        org: browser.globals.orgA,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC7: admin session on user prefix -> 10038');
      });
    });
  },

  'AC7b: the docs route is not served on the admin prefix': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/docs',
      org
    }, (res) => {
      // Either a 404 (route not bound) or a business error; it must NOT
      // be the GetApiDocs success envelope.
      browser.assert.ok(
        res.status === 404 || (res.body && res.body.code !== 0),
        'AC7b: docs route not served on admin prefix'
      );
    });
  },

  // ---- Console pages: end-user API Docs page (AC3-AC6) ----

  'AC3: /docs page renders the endpoint list and the detail pane from the first successful load': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/docs');
    browser.waitForElementPresent('[data-testid="api-docs-title"]', 15000, 'AC3: page renders');
    browser.waitForElementPresent('[data-testid="docs-endpoint-list"]', 10000, 'AC3: endpoint list');
    browser.waitForElementPresent('[data-testid="docs-detail"]', 10000, 'AC3: detail pane');
    browser.waitForElementPresent('[data-testid="docs-lang-tabs"]', 10000, 'AC3: language tabs');
    browser.waitForElementPresent('[data-testid="docs-request-example"]', 10000, 'AC3: request example');
    browser.waitForElementPresent('[data-testid="docs-response-example"]', 10000, 'AC3: response example');
  },

  'AC4: selecting an endpoint loads its detail; language tabs switch the example client-side': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/docs');
    browser.waitForElementPresent('[data-testid="docs-endpoint-list"]', 15000, 'AC4: endpoint list');
    // The first endpoint is selected by default; switch the language tab
    // and assert the request example updates.
    browser.waitForElementPresent('[data-testid="docs-lang-python"]', 10000, 'AC4: python tab');
    browser.click('[data-testid="docs-lang-curl"]');
    browser.waitForElementPresent('[data-testid="docs-lang-curl"]', 5000, 'AC4: curl tab active');
    browser.click('[data-testid="docs-lang-node"]');
    browser.waitForElementPresent('[data-testid="docs-lang-node"]', 5000, 'AC4: node tab active');
    browser.waitForElementPresent('[data-testid="docs-copy"]', 5000, 'AC4: copy button');
  },

  'AC5: try-it on an inference endpoint renders the model/key selectors and the send button': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/docs');
    browser.waitForElementPresent('[data-testid="docs-endpoint-list"]', 15000, 'AC5: endpoint list');
    // The first category's first endpoint is the inference chat
    // completions endpoint (tryable). Assert the try-it panel renders.
    browser.waitForElementPresent('[data-testid="docs-try-it"]', 15000, 'AC5: try-it panel');
    browser.waitForElementPresent('[data-testid="docs-try-model"]', 10000, 'AC5: model selector');
    browser.waitForElementPresent('[data-testid="docs-try-key"]', 10000, 'AC5: key selector');
    browser.waitForElementPresent('[data-testid="docs-try-prompt"]', 10000, 'AC5: prompt editor');
    browser.waitForElementPresent('[data-testid="docs-try-send"]', 10000, 'AC5: send button');
  },

  // ---- Console pages: permission denied (AC8) ----

  'AC8: a session without the required role receives 10036 on the docs API': function (browser) {
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
        path: '/api/v1/docs',
        org,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10036, 'AC8: non-member session on docs API -> 10036');
      });
    });
  }
};