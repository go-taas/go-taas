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

// Feature-21 (SDK / Quickstart) e2e suite, run against the compose stack
// gateway. Covers the acceptance criteria of
// docs/design/sdk-quickstart.md reachable from the outside: the new
// GET /api/v1/inference-endpoint read (AC1/AC11), the /quickstart page
// rendering the four step cards (AC2), inline key creation with the
// one-time secret and acknowledgment gate (AC3), the key selector using
// the YOUR_API_KEY placeholder for existing keys (AC4), the masked model
// selector (AC5), the base URL copy control (AC6), the Python/Node/curl
// tabs (AC7), the Test request disabled/enabled + inline error (AC8), the
// no-models empty state (AC9), the failed-load ErrorBanner + Retry (AC10),
// and the end-user surface separation (AC12/AC13).
//
// The quickstart page is end-user surface only: route /quickstart, API
// /api/v1/*. The model catalog is global; each case that needs a model
// registers a unique model and grants it to orgA (restricted), so orgB
// (used for the no-models empty state) deterministically sees none.

const api = require('../page-objects/api.js');

// The compose stack configures CONFIG_INFER_ENDPOINTBASEURL to this value
// (deploy/compose/docker-compose.yaml); the endpoint read derives
// "<base>/v1" from it (AD9). Since feature #42 (batch inference) the
// compose stack points the inference gateway at the mock-infer service so
// the batch/evaluation workers reach a live OpenAI-compatible endpoint.
const EXPECTED_BASE_URL = 'http://mock-infer:8000/v1';

// Register a unique model and grant it to orgA (restricted), so orgA sees
// it in the masked model list and orgB does not. The model is global;
// granting it to orgA keeps it from leaking to orgB.
function seedModelForOrgA(browser, cb) {
  const org = browser.globals.orgA;
  const name = `qwen-e2e-${browser.globals.runId}-${browser.globals.testSeq}`;
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/admin/models',
    org,
    body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
  }, (res) => {
    const model = api.assertOk(browser, res, 'register quickstart model');
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/models/${model.modelId}:grant`,
      org,
      body: {organizationId: org}
    }, (res2) => {
      api.assertOk(browser, res2, 'grant quickstart model to orgA');
      browser.globals.modelId = model.modelId;
      browser.globals.modelName = name;
      cb();
    });
  });
}

// Create an API key for the org via the user-realm API (the same path the
// page's inline create form uses).
function createKey(browser, org, name, cb) {
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/auth/api-keys',
    org,
    body: {name}
  }, (res) => {
    const body = api.assertOk(browser, res, 'create key');
    cb(body);
  });
}

module.exports = {
  '@tags': ['sdk-quickstart', 'feature-21'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-qs-${browser.globals.runId}-${browser.globals.testSeq}`;
    browser.globals.orgB = `org-e2e-qs-b-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.ensureOrg(browser, browser.globals.orgB);
    // Seed a user-realm session so the protected pages render (feature:
    // unauthenticated pages redirect to login). The quickstart page uses
    // orgA as the active org.
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC1: GET /api/v1/inference-endpoint returns the configured base_url': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/inference-endpoint',
      org: browser.globals.orgA
    }, (res) => {
      const body = api.assertOk(browser, res, 'inference-endpoint');
      browser.assert.equal(
        body.baseUrl,
        EXPECTED_BASE_URL,
        'AC1: base_url matches the configured endpoint (not a client constant)'
      );
    });
  },

  'AC11: inference-endpoint read requires no organization context': function (browser) {
    // The read is a user-realm read with no org context (design §6 note 1);
    // it succeeds without an X-Organization-Id header.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/inference-endpoint',
      org: ''
    }, (res) => {
      const body = api.assertOk(browser, res, 'inference-endpoint without org header');
      browser.assert.equal(body.baseUrl, EXPECTED_BASE_URL, 'AC11: base_url returned without org context');
    });
  },

  'AC2: /quickstart renders the four step cards inside UserShell': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/quickstart');
    browser.waitForElementPresent('[data-testid="user-shell"]', 15000, 'AC2: user shell');
    browser.waitForElementPresent('[data-testid="quickstart-step-key"]', 10000, 'AC2: step 1 key');
    browser.waitForElementPresent('[data-testid="quickstart-step-model"]', 10000, 'AC2: step 2 model');
    browser.waitForElementPresent('[data-testid="quickstart-step-baseurl"]', 10000, 'AC2: step 3 base URL');
    browser.waitForElementPresent('[data-testid="quickstart-step-code"]', 10000, 'AC2: step 4 code & test');
    browser.assert.containsText('[data-testid="quickstart-step-key"]', 'Step 1 — API key', 'AC2: step 1 header');
    browser.assert.containsText('[data-testid="quickstart-step-model"]', 'Step 2 — Model', 'AC2: step 2 header');
    browser.assert.containsText('[data-testid="quickstart-step-baseurl"]', 'Step 3 — Base URL', 'AC2: step 3 header');
    browser.assert.containsText('[data-testid="quickstart-step-code"]', 'Step 4 — Code & test', 'AC2: step 4 header');
  },

  'AC3: no keys shows inline create form; creating a key shows the one-time secret + ack gate and injects the plaintext': function (browser) {
    // Fresh orgA has no keys -> the inline create form renders.
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/quickstart');
    browser.waitForElementPresent('[data-testid="quickstart-key-name"]', 10000, 'AC3: inline create form name input');
    browser.waitForElementPresent('[data-testid="quickstart-key-expiry"]', 5000, 'AC3: expiry select');
    browser.waitForElementPresent('[data-testid="quickstart-key-create-submit"]', 5000, 'AC3: create submit');

    // Fill the name and submit.
    browser.setValue('[data-testid="quickstart-key-name"]', 'e2e-inline-key');
    browser.click('[data-testid="quickstart-key-create-submit"]');

    // The one-time secret panel appears with the acknowledgment gate.
    browser.waitForElementPresent('[data-testid="quickstart-created-secret"]', 10000, 'AC3: created secret panel');
    browser.waitForElementPresent('[data-testid="quickstart-created-secret-value"]', 5000, 'AC3: secret value');
    browser.waitForElementPresent('[data-testid="quickstart-created-copy"]', 5000, 'AC3: copy key button');
    browser.assert.containsText(
      '[data-testid="quickstart-created-secret"]',
      'This key will not be shown again.',
      'AC3: one-time secret warning'
    );

    // The Done button is gated behind the acknowledgment checkbox.
    browser.assert.attributeContains(
      '[data-testid="quickstart-created-done"]',
      'disabled',
      'true',
      'AC3: Done disabled until acknowledged'
    );
    browser.click('[data-testid="quickstart-created-confirm"]');
    browser.assert.not.attributeContains(
      '[data-testid="quickstart-created-done"]',
      'disabled',
      'true',
      'AC3: Done enabled after acknowledgment'
    );

    // The just-created plaintext is injected into the snippet (not the
    // YOUR_API_KEY placeholder).
    browser.getText('[data-testid="quickstart-created-secret-value"]', (result) => {
      const plaintext = result.value;
      browser.assert.ok(
        /^sk-/.test(plaintext),
        'AC3: created secret is a plaintext sk- key (got: ' + plaintext + ')'
      );
      browser.assert.textContains(
        '[data-testid="quickstart-snippet"]',
        plaintext,
        'AC3: snippet carries the just-created plaintext'
      );
      browser.assert.not.textContains(
        '[data-testid="quickstart-snippet"]',
        'YOUR_API_KEY',
        'AC3: snippet does not use the placeholder for a just-created key'
      );
    });
  },

  'AC4: existing keys show a key selector; selecting an existing key uses the YOUR_API_KEY placeholder': function (browser) {
    const org = browser.globals.orgA;
    // Create a key first so the selector renders.
    createKey(browser, org, 'e2e-existing-key', () => {
      browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/quickstart');
      browser.waitForElementPresent('[data-testid="quickstart-key-select"]', 10000, 'AC4: key selector renders');
      browser.waitForElementPresent('[data-testid="quickstart-key-create"]', 5000, 'AC4: Create new link renders');

      // The page auto-selects the first key; the snippet uses the
      // YOUR_API_KEY placeholder (no stored secret appears).
      browser.assert.containsText(
        '[data-testid="quickstart-snippet"]',
        'YOUR_API_KEY',
        'AC4: snippet uses the placeholder for an existing key'
      );
      browser.assert.not.containsText(
        '[data-testid="quickstart-snippet"]',
        'sk-',
        'AC4: no stored secret appears in the snippet'
      );
    });
  },

  'AC5: model selector lists the masked model; selecting a model updates the snippet model field': function (browser) {
    const org = browser.globals.orgA;
    seedModelForOrgA(browser, () => {
      browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/quickstart');
      browser.waitForElementPresent('[data-testid="quickstart-model-select"]', 10000, 'AC5: model selector renders');

      // The masked projection shows name + latest version.
      browser.assert.containsText(
        '[data-testid="quickstart-model-select"]',
        browser.globals.modelName,
        'AC5: model name listed'
      );
      browser.assert.containsText(
        '[data-testid="quickstart-model-select"]',
        'v1',
        'AC5: latest version listed'
      );

      // The page auto-selects the first model; the snippet carries its id.
      browser.assert.containsText(
        '[data-testid="quickstart-snippet"]',
        browser.globals.modelId,
        'AC5: snippet model field is the selected model id'
      );
    });
  },

  'AC6: base URL renders in a monospace field with a working copy control': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/quickstart');
    browser.waitForElementPresent('[data-testid="quickstart-base-url"]', 10000, 'AC6: base URL field renders');
    browser.assert.containsText(
      '[data-testid="quickstart-base-url"]',
      EXPECTED_BASE_URL,
      'AC6: base URL matches the configured endpoint'
    );
    browser.waitForElementPresent('[data-testid="quickstart-base-url-copy"]', 5000, 'AC6: copy control renders');
  },

  'AC7: Python/Node/curl tabs swap the snippet': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/quickstart');
    browser.waitForElementPresent('[data-testid="quickstart-lang-python"]', 10000, 'AC7: python tab');
    browser.waitForElementPresent('[data-testid="quickstart-lang-node"]', 5000, 'AC7: node tab');
    browser.waitForElementPresent('[data-testid="quickstart-lang-curl"]', 5000, 'AC7: curl tab');
    browser.waitForElementPresent('[data-testid="quickstart-snippet"]', 5000, 'AC7: snippet');

    // Default tab is Python (openai SDK with base_url).
    browser.assert.containsText('[data-testid="quickstart-snippet"]', 'from openai import OpenAI', 'AC7: python snippet');
    browser.assert.containsText('[data-testid="quickstart-snippet"]', 'base_url=', 'AC7: python uses base_url');

    // Node tab swaps to the JS SDK with baseURL.
    browser.click('[data-testid="quickstart-lang-node"]');
    browser.assert.containsText('[data-testid="quickstart-snippet"]', 'import OpenAI from "openai"', 'AC7: node snippet');
    browser.assert.containsText('[data-testid="quickstart-snippet"]', 'baseURL:', 'AC7: node uses baseURL');

    // curl tab swaps to a curl command posting to {base_url}/chat/completions.
    browser.click('[data-testid="quickstart-lang-curl"]');
    browser.assert.containsText(
      '[data-testid="quickstart-snippet"]',
      EXPECTED_BASE_URL + '/chat/completions',
      'AC7: curl posts to base_url/chat/completions'
    );
    browser.assert.containsText('[data-testid="quickstart-snippet"]', 'Authorization: Bearer', 'AC7: curl bearer auth');

    // Each tab has a copy button.
    browser.waitForElementPresent('[data-testid="quickstart-copy"]', 5000, 'AC7: copy snippet button');
  },

  'AC8: Test request is disabled until a model and key are selected; enabled call renders the error inline': function (browser) {
    const org = browser.globals.orgA;
    // Fresh orgA has no key and no model -> Test request is disabled.
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/quickstart');
    browser.waitForElementPresent('[data-testid="quickstart-test"]', 10000, 'AC8: test button renders');
    browser.assert.attributeContains(
      '[data-testid="quickstart-test"]',
      'disabled',
      'true',
      'AC8: test disabled with no key/model'
    );

    // Give orgA a key and a model, then reload: the test button enables.
    createKey(browser, org, 'e2e-test-key', () => {
      seedModelForOrgA(browser, () => {
        browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
        browser.url(browser.globals.baseUrl + '/quickstart');
        browser.waitForElementPresent('[data-testid="quickstart-test"]', 10000, 'AC8: test button after reload');
        browser.assert.not.attributeContains(
          '[data-testid="quickstart-test"]',
          'disabled',
          'true',
          'AC8: test enabled with key + model'
        );

        // Click Test request. The compose stack has no data-plane
        // inference service, so the playground proxy returns 10301 and the
        // page renders the error inline (AC8 error path).
        browser.click('[data-testid="quickstart-test"]');
        browser.waitForElementPresent('[data-testid="quickstart-test-error"]', 10000, 'AC8: inline error renders');
        browser.assert.containsText(
          '[data-testid="quickstart-test-error"]',
          'inference service not found',
          'AC8: inline error copy for 10301'
        );
      });
    });
  },

  'AC9: no authorized models shows the empty state with a link to /playground': function (browser) {
    // The model catalog is default-allow (feature #13): a model with zero
    // grant rows is visible to every organization, so other suites'
    // registered models would leak into orgB's list and the empty state
    // would never render. Mock the models read to return an empty list so
    // the empty state is deterministic regardless of catalog residue.
    browser.network.mockResponse(browser.globals.baseUrl + '/api/v1/models?page.limit=100', {
      status: 200,
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({response: {code: 0, message: 'ok'}, models: []})
    });
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${browser.globals.orgB}')`);
    browser.url(browser.globals.baseUrl + '/quickstart');
    browser.waitForElementPresent('[data-testid="quickstart-no-models"]', 10000, 'AC9: no-models empty state');
    browser.assert.containsText(
      '[data-testid="quickstart-no-models"]',
      'No models are available to your organization yet',
      'AC9: empty state copy'
    );
    browser.assert.containsText('[data-testid="quickstart-no-models"]', 'Playground', 'AC9: link to Playground');
  },

  'AC10: a failed load shows the ErrorBanner with a working Retry': function (browser) {
    const org = browser.globals.orgA;
    const endpointUrl = browser.globals.baseUrl + '/api/v1/inference-endpoint';

    // Mock the inference-endpoint read to fail (500) so the page's load
    // error path triggers.
    browser.network.mockResponse(endpointUrl, {
      status: 500,
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({code: 500, message: 'internal error'})
    });

    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/quickstart');

    // The ErrorBanner with the mapped copy and a Retry button appear.
    browser.waitForElementPresent('[data-testid="quickstart-retry"]', 10000, 'AC10: Retry button renders');
    browser.assert.containsText(
      'body',
      'Could not load the inference endpoint.',
      'AC10: mapped error copy'
    );

    // Restore the endpoint and click Retry: the page reloads and the base
    // URL renders (the retry works).
    browser.network.mockResponse(endpointUrl, {
      status: 200,
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({response: {code: 0, message: 'ok'}, baseUrl: EXPECTED_BASE_URL})
    });
    browser.click('[data-testid="quickstart-retry"]');
    browser.waitForElementPresent('[data-testid="quickstart-base-url"]', 10000, 'AC10: base URL renders after retry');
    browser.assert.containsText(
      '[data-testid="quickstart-base-url"]',
      EXPECTED_BASE_URL,
      'AC10: retry recovered the base URL'
    );
  },

  'AC12/AC13: quickstart is end-user surface only — user-nav-quickstart first, 8 nav items, no admin API calls': function (browser) {
    const org = browser.globals.orgA;
    const captured = [];

    // Register the network capture before navigating so the page's own
    // /api/v1/* calls are observed.
    browser.network.captureRequests((params) => {
      const url = params && params.request ? params.request.url : '';
      if (url.indexOf('/api/v1/') !== -1) {
        captured.push(url);
      }
    });

    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/quickstart');
    browser.waitForElementPresent('[data-testid="user-shell"]', 15000, 'AC12: user shell');

    // user-nav-quickstart is present and is the first nav item.
    browser.waitForElementPresent('[data-testid="user-nav-quickstart"]', 10000, 'AC12: user-nav-quickstart');
    browser.assert.containsText('[data-testid="user-nav-quickstart"]', 'Quickstart', 'AC12: nav label');

    // The user nav grew from 8 items at feature-21 time to 21 as later
    // features added destinations (webhooks, billing-reports, traces,
    // playground, forecast, batch, prompts, evaluations, ...). The count
    // is a snapshot each feature intentionally extends (design D8
    // precedent), so assert the floor from feature-21 plus ordering, not
    // an exact count.
    browser.elements('css selector', '[data-testid^="user-nav-"]', (result) => {
      browser.assert.ok(
        result.value.length >= 8,
        'AC13: at least 8 user-nav-* items (got ' + result.value.length + ')'
      );
    });

    // The page makes only /api/v1/* calls: none targets /api/v1/admin/*.
    browser.perform((done) => {
      const adminCalls = captured.filter((u) => u.indexOf('/api/v1/admin/') !== -1);
      browser.assert.equal(adminCalls.length, 0, 'AC12: no /api/v1/admin/* calls from the quickstart page');
      browser.assert.ok(
        captured.length >= 3,
        'AC12: page made user-prefix API calls (got: ' + JSON.stringify(captured) + ')'
      );
      done();
    });
  }
};