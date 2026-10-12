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

// Feature-48 (external model providers) e2e suite, run against the compose
// stack gateway + admin console. Covers the acceptance criteria of
// docs/design/external-model-providers.md reachable from the outside:
//
//   - AC1  the admin page /admin/providers renders from the admin nav
//          (nav-providers) with its search/type/enabled filters and the
//          provider detail page
//   - AC2  the create dialog validates name/type/base URL/key/timeout and a
//          valid submission adds a masked row with status Not checked
//   - AC3  the API key is write-only: no read RPC or page returns the key
//          (plaintext or ciphertext), only the masked hint; an empty key on
//          edit keeps the credential, a non-empty one rotates it
//   - AC4  the five probe states map exactly (ok / unauthorized / error /
//          unreachable / unknown); unauthorized != unreachable
//   - AC5  upstream-model curation add/duplicate/edit/remove with the
//          documented 13009/13010 codes
//   - AC6  publish creates an External catalog model; a live name conflict
//          is 13014; publishing is idempotent
//   - AC7  unpublish hides the model from the tenant catalog, fails closed
//          with 13015, and a published row cannot be removed (13016)
//   - AC8  enabling a provider whose last probe is unauthorized is rejected
//          (13012); Delete is disabled while models are published
//   - AC9  a published external model is consumed through the playground and
//          metered (a voucher with non-zero tokens attributed to the model
//          and the API key)
//   - AC10 the fail-closed codes 13011/13012/13013/13015 are distinct
//   - AC11 the tenant catalog/detail show the External marker and no
//          provider internals; the playground lists and calls the model
//   - AC12 realm separation: a user-realm session on
//          /api/v1/admin/providers is 10038; the providers API is not on the
//          bare /api/v1/providers prefix; /providers is not served by the
//          user console
//   - AC13 a non-member admin session receives 10036 and the page shows the
//          permission-denied state
//   - AC15 empty, guarded and permission-denied states render
//
// Deterministic probe coverage uses the mock-infer service (the real
// external OpenAI-compatible fixture on the compose network):
//   http://mock-infer:8000/v1           -> ok (200)
//   http://mock-infer:8000/unauthorized -> unauthorized (401)
//   http://mock-infer:8000/unavailable  -> error (503)
//   http://does-not-exist.invalid       -> unreachable (DNS failure)
//
// The live DeepSeek endpoint is intentionally NOT asserted here: it is only
// reachable when the host has outbound internet, so the deterministic 401
// coverage comes from mock-infer's /unauthorized variant.

const api = require('../page-objects/api.js');

const ADMIN = 'admin';
const USER = 'user';
// Every fixture this suite creates is named with this prefix so the sweep
// can never touch a developer fixture.
const FIXTURE_PREFIX = 'e2e-emp-';
const OK_BASE = 'http://mock-infer:8000/v1';
const UNAUTH_BASE = 'http://mock-infer:8000/unauthorized';
const DOWN_BASE = 'http://mock-infer:8000/unavailable';
const DEAD_BASE = 'http://does-not-exist.invalid';

// maskedHint mirrors the service's mask rule (last 4 chars, fixed mask).
function maskedHint(key) {
  return `••••${key.slice(-4)}`;
}

// selectOption sets a React-controlled <select> to `value` using the native
// value setter + a bubbling change event (WebDriver option clicks are flaky on
// a controlled select).
function selectOption(browser, testId, value) {
  browser.execute(function (sel, val) {
    const el = document.querySelector(sel);
    if (!el) {
      return false;
    }
    const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value').set;
    setter.call(el, val);
    el.dispatchEvent(new Event('change', {bubbles: true}));
    return el.value === val;
  }, [`[data-testid="${testId}"]`, value], (r) => {
    browser.assert.ok(r.value, `select ${testId} = ${value}`);
  });
  return browser;
}

// setInputValue sets a React-controlled text input (or textarea) to `value`,
// including the empty string — WebDriver setValue('') does not reliably fire
// the input event a controlled component listens to.
function setInputValue(browser, testId, value) {
  browser.execute(function (sel, val) {
    const el = document.querySelector(sel);
    if (!el) {
      return false;
    }
    const proto =
      el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
    setter.call(el, val);
    el.dispatchEvent(new Event('input', {bubbles: true}));
    el.dispatchEvent(new Event('change', {bubbles: true}));
    return el.value === val;
  }, [`[data-testid="${testId}"]`, value], (r) => {
    browser.assert.ok(r.value, `input ${testId} = ${value}`);
  });
  return browser;
}

// setOrgKeys points both surfaces at the suite's organization before a page
// navigation (the console resolves the org from its realm-scoped key).
function setOrgKeys(browser, org) {
  browser.execute(function (o) {
    localStorage.setItem('go-taas.admin.org-id', o);
    localStorage.setItem('go-taas.user.org-id', o);
  }, [org]);
  return browser;
}

// apiGet runs an admin GET and hands the parsed body to cb.
function apiGet(browser, org, path, cb) {
  api.request(browser, {method: 'GET', path, org, sessionRealm: ADMIN}, (res) => {
    cb(api.assertOk(browser, res, `GET ${path}`));
  });
}

// createProvider creates a provider via the admin API and returns its object.
function createProvider(browser, org, body, cb) {
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/admin/providers',
    org,
    sessionRealm: ADMIN,
    body
  }, (res) => {
    cb(api.assertOk(browser, res, `create provider ${body.name}`).provider);
  });
}

// addProviderModel curates an upstream model and returns its row.
function addProviderModel(browser, org, providerId, body, cb) {
  api.request(browser, {
    method: 'POST',
    path: `/api/v1/admin/providers/${providerId}/models`,
    org,
    sessionRealm: ADMIN,
    body
  }, (res) => {
    cb(api.assertOk(browser, res, `add model ${body.upstreamModelId}`).model);
  });
}

// publishProviderModel publishes a curated row and returns the response body.
function publishProviderModel(browser, org, providerId, modelId, tenantName, cb) {
  api.request(browser, {
    method: 'POST',
    path: `/api/v1/admin/providers/${providerId}/models/${modelId}:publish`,
    org,
    sessionRealm: ADMIN,
    body: {tenantFacingName: tenantName}
  }, (res) => {
    cb(api.assertOk(browser, res, `publish ${tenantName}`));
  });
}

// deleteProviderNow removes a provider (best-effort cleanup).
function deleteProviderNow(browser, org, providerId, done) {
  api.request(browser, {
    method: 'DELETE',
    path: `/api/v1/admin/providers/${providerId}`,
    org,
    sessionRealm: ADMIN
  }, () => done());
}

// deleteProviderDeep unpublishes every published row, removes every curated
// row and then deletes the provider — the exact order the service requires
// (a published model blocks removal, a published provider blocks deletion).
function deleteProviderDeep(browser, org, providerId, done) {
  api.request(browser, {
    method: 'GET',
    path: `/api/v1/admin/providers/${providerId}/models?page.limit=100`,
    org,
    sessionRealm: ADMIN
  }, (res) => {
    const models = (res.body && res.body.models) || [];
    const unpublishNext = (list) => {
      if (!list.length) {
        removeNext(models);
        return;
      }
      const m = list[0];
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/providers/${providerId}/models/${m.modelId}:unpublish`,
        org,
        sessionRealm: ADMIN,
        body: {}
      }, () => unpublishNext(list.slice(1)));
    };
    const removeNext = (list) => {
      if (!list.length) {
        deleteProviderNow(browser, org, providerId, done);
        return;
      }
      const m = list[0];
      api.request(browser, {
        method: 'DELETE',
        path: `/api/v1/admin/providers/${providerId}/models/${m.modelId}`,
        org,
        sessionRealm: ADMIN
      }, () => removeNext(list.slice(1)));
    };
    unpublishNext(models.filter((m) => m.published));
  });
}

// deleteProviders runs deleteProviderDeep sequentially over a list of ids.
function deleteProviders(browser, org, ids, done) {
  const list = ids.slice();
  const next = () => {
    if (!list.length) {
      done();
      return;
    }
    deleteProviderDeep(browser, org, list.shift(), next);
  };
  next();
}

// sweepFixtures removes every leftover fixture provider from an aborted run
// (names are namespaced with FIXTURE_PREFIX, so nothing else is touched).
function sweepFixtures(browser, org, done) {
  api.request(browser, {
    method: 'GET',
    path: '/api/v1/admin/providers?page.limit=100',
    org,
    sessionRealm: ADMIN
  }, (res) => {
    const list = ((res.body && res.body.providers) || []).filter((p) =>
      String(p.name || '').startsWith(FIXTURE_PREFIX)
    );
    deleteProviders(browser, org, list.map((p) => p.providerId), done);
  });
}

// waitForVoucher polls the metering voucher list until a voucher for the
// catalog model + API key with non-zero tokens appears (the metering event
// is consumed asynchronously).
function waitForVoucher(browser, org, catalogId, keyId, timeoutMs) {
  browser
    .timeoutsAsyncScript(timeoutMs + 5000)
    .executeAsync(function ({url, org, catalogId, keyId, deadline}, done) {
      const token = localStorage.getItem('go-taas.admin.session-token');
      const start = Date.now();
      const tick = () => {
        fetch(url, {headers: {Authorization: `Bearer ${token}`, 'X-Organization-Id': org}})
          .then((r) => r.json())
          .then((body) => {
            const vs = (body && body.vouchers) || [];
            const hit = vs.find(
              (v) =>
                v.modelId === catalogId &&
                v.apiKeyId === keyId &&
                Number((v.usage && v.usage.promptTokens) || 0) > 0 &&
                Number((v.usage && v.usage.completionTokens) || 0) > 0
            );
            if (hit) {
              return done({ok: true, voucher: hit});
            }
            if (Date.now() - start > deadline) {
              return done({ok: false, count: vs.length});
            }
            setTimeout(tick, 1500);
          })
          .catch(() => {
            if (Date.now() - start > deadline) {
              return done({ok: false, error: 'fetch failed'});
            }
            setTimeout(tick, 1500);
          });
      };
      tick();
    }, [{
      url: api.url(browser, '/api/v1/admin/metering/vouchers?page.limit=100'),
      org,
      catalogId,
      keyId,
      deadline: timeoutMs
    }], (result) => {
      const v = result && result.value !== undefined ? result.value : result;
      browser.assert.ok(
        v && v.ok,
        `AC9: metering voucher recorded for the external model — ${JSON.stringify(v)}`
      );
      if (v && v.ok) {
        browser.assert.equal(v.voucher.modelId, catalogId, 'AC9: voucher attributed to the catalog model');
        browser.assert.equal(v.voucher.apiKeyId, keyId, 'AC9: voucher attributed to the API key');
        browser.assert.ok(
          Number(v.voucher.usage.promptTokens) > 0,
          'AC9: voucher prompt tokens > 0'
        );
        browser.assert.ok(
          Number(v.voucher.usage.completionTokens) > 0,
          'AC9: voucher completion tokens > 0'
        );
      }
    });
  return browser;
}

// assertPageExcludes reads the rendered body text and asserts it does not
// contain `needle` (used for the secret-hygiene reads).
function assertPageExcludes(browser, needle, label) {
  browser.execute(function () {
    return document.body ? document.body.innerText : '';
  }, [], (r) => {
    const text = String(r.value || '');
    browser.assert.ok(
      text.indexOf(needle) === -1,
      `${label}: rendered page never contains the key material`
    );
  });
}

module.exports = {
  '@tags': ['external-model-providers', 'feature-48'],

  before(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    const org = `org-e2e-emp-${browser.globals.runId}`;
    browser.globals.orgA = org;
    api.ensureOrg(browser, org);
    api.seedSession(browser, ADMIN, org);
    // Aborted runs may leave fixture providers behind; sweep them before the
    // suite so the list page assertions stay deterministic.
    sweepFixtures(browser, org, () => {});
  },

  beforeEach(browser) {
    // afterEach ends the session, so start a fresh one on the gateway origin.
    browser.url(browser.globals.baseUrl + '/');
    api.seedSession(browser, ADMIN, browser.globals.orgA);
    setOrgKeys(browser, browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- AC1: admin page + navigation ----

  'AC1: /admin/providers renders from the admin nav with its filters': function (browser) {
    const org = browser.globals.orgA;
    const name = `${FIXTURE_PREFIX}list-${browser.globals.runId}`;
    createProvider(browser, org, {
      name, type: 'openai_compatible', baseUrl: OK_BASE, apiKey: 'sk-e2e-list-123456', enabled: false
    }, (p) => {
      setOrgKeys(browser, org);
      browser.url(browser.globals.baseUrl + '/admin/providers');
      browser.waitForElementPresent('[data-testid="nav-providers"]', 20000, 'AC1: nav-providers entry');
      browser.waitForElementPresent('[data-testid="provider-list"]', 20000, 'AC1: providers page renders');
      browser.waitForElementPresent('[data-testid="provider-add"]', 10000, 'AC1: Add provider action');
      browser.waitForElementPresent('[data-testid="provider-refresh"]', 10000, 'AC1: Refresh action');
      browser.waitForElementPresent('[data-testid="provider-search"]', 10000, 'AC1: search box');
      browser.waitForElementPresent('[data-testid="provider-filter-type"]', 10000, 'AC1: type filter');
      browser.waitForElementPresent('[data-testid="provider-filter-status"]', 10000, 'AC1: status filter');
      browser.waitForElementPresent('[data-testid="provider-filter-enabled"]', 10000, 'AC1: enabled filter');
      browser.waitForElementPresent(`[data-testid="provider-row-${p.providerId}"]`, 15000, 'AC1: fixture row');
      // A never-probed provider shows Not checked and the non-HTTPS chip.
      browser.assert.containsText(
        `[data-testid="provider-status-${p.providerId}"]`,
        'Not checked',
        'AC1: never-probed status is Not checked'
      );
      browser.waitForElementPresent(
        `[data-testid="provider-nonhttps-chip-${p.providerId}"]`,
        10000,
        'AC1: non-HTTPS chip for an http base URL'
      );

      // Search narrows to a no-match -> empty state.
      setInputValue(browser, 'provider-search', `no-such-${browser.globals.runId}`);
      browser.waitForElementPresent('[data-testid="provider-empty"]', 15000, 'AC1: empty state on no match');
      setInputValue(browser, 'provider-search', '');
      browser.waitForElementPresent(`[data-testid="provider-row-${p.providerId}"]`, 15000, 'AC1: search cleared');

      // Type filter: openai_compatible keeps the row, openai hides it.
      selectOption(browser, 'provider-filter-type', 'openai');
      browser.pause(1200);
      browser.assert.not.elementPresent(
        `[data-testid="provider-row-${p.providerId}"]`,
        'AC1: type filter hides a non-matching provider'
      );
      selectOption(browser, 'provider-filter-type', 'openai_compatible');
      browser.waitForElementPresent(
        `[data-testid="provider-row-${p.providerId}"]`,
        15000,
        'AC1: type filter keeps the matching provider'
      );

      // Enabled filter: true hides the disabled row, false keeps it.
      selectOption(browser, 'provider-filter-enabled', 'true');
      browser.pause(1200);
      browser.assert.not.elementPresent(
        `[data-testid="provider-row-${p.providerId}"]`,
        'AC1: enabled filter hides a disabled provider'
      );
      selectOption(browser, 'provider-filter-enabled', 'false');
      browser.waitForElementPresent(
        `[data-testid="provider-row-${p.providerId}"]`,
        15000,
        'AC1: enabled filter keeps the disabled provider'
      );

      deleteProviders(browser, org, [p.providerId], () => {});
    });
  },

  'AC1: the provider detail page renders the overview, probe panel and model table': function (browser) {
    const org = browser.globals.orgA;
    const name = `${FIXTURE_PREFIX}detail-${browser.globals.runId}`;
    createProvider(browser, org, {
      name, type: 'openai_compatible', baseUrl: OK_BASE, apiKey: 'sk-e2e-detail-123456', enabled: false
    }, (p) => {
      setOrgKeys(browser, org);
      browser.url(browser.globals.baseUrl + `/admin/providers/${p.providerId}`);
      browser.waitForElementPresent('[data-testid="provider-detail"]', 20000, 'AC1: detail page renders');
      browser.waitForElementPresent('[data-testid="provider-detail-name"]', 10000, 'AC1: detail name');
      browser.assert.containsText('[data-testid="provider-detail-name"]', name, 'AC1: detail name value');
      browser.waitForElementPresent('[data-testid="provider-detail-overview"]', 10000, 'AC1: overview card');
      browser.waitForElementPresent('[data-testid="provider-detail-base-url"]', 10000, 'AC1: base URL');
      browser.waitForElementPresent('[data-testid="provider-detail-key-hint"]', 10000, 'AC1: masked key hint');
      browser.waitForElementPresent('[data-testid="provider-test-panel"]', 10000, 'AC1: connectivity panel');
      browser.waitForElementPresent('[data-testid="provider-test-status"]', 10000, 'AC1: probe status badge');
      browser.waitForElementPresent('[data-testid="provider-test-now"]', 10000, 'AC1: Test connection action');
      browser.waitForElementPresent('[data-testid="provider-model-table"]', 10000, 'AC1: upstream-model card');
      browser.waitForElementPresent('[data-testid="provider-model-empty"]', 10000, 'AC1: empty upstream-model state');
      browser.assert.containsText(
        '[data-testid="provider-test-status"]',
        'Not checked',
        'AC1: detail shows Not checked before the first probe'
      );

      deleteProviders(browser, org, [p.providerId], () => {});
    });
  },

  // ---- AC2 / AC3: create dialog validation + masked, write-only key ----

  'AC2/AC3: the create dialog validates fields and creates a masked provider': function (browser) {
    const org = browser.globals.orgA;
    const name = `${FIXTURE_PREFIX}ui-${browser.globals.runId}`;
    const key = `sk-e2e-secret-${browser.globals.runId}-WXYZ`;
    setOrgKeys(browser, org);
    browser.url(browser.globals.baseUrl + '/admin/providers');
    browser.waitForElementPresent('[data-testid="provider-add"]', 20000, 'AC2: Add provider action');

    browser.click('[data-testid="provider-add"]');
    browser.waitForElementPresent('[data-testid="provider-dialog"]', 10000, 'AC2: create dialog');

    // Empty submit -> name validation.
    browser.click('[data-testid="provider-form-submit"]');
    browser.assert.containsText('[data-testid="provider-dialog"]', 'Enter a lowercase name', 'AC2: name required/invalid');

    // Invalid name pattern.
    browser.setValue('[data-testid="provider-form-name"]', 'E2E_BAD');
    browser.click('[data-testid="provider-form-submit"]');
    browser.assert.containsText('[data-testid="provider-dialog"]', 'Enter a lowercase name', 'AC2: name pattern');

    // Valid name, invalid base URL.
    browser.setValue('[data-testid="provider-form-name"]', name);
    browser.setValue('[data-testid="provider-form-base-url"]', 'not-a-url');
    browser.click('[data-testid="provider-form-submit"]');
    browser.assert.containsText('[data-testid="provider-dialog"]', 'Enter a valid absolute URL', 'AC2: base URL invalid');

    // Valid base URL, missing key on create.
    browser.setValue('[data-testid="provider-form-base-url"]', OK_BASE);
    browser.click('[data-testid="provider-form-submit"]');
    browser.assert.containsText('[data-testid="provider-dialog"]', 'Enter the provider API key', 'AC2: key required on create');

    // Key present, timeout out of range.
    browser.setValue('[data-testid="provider-form-api-key"]', key);
    browser.setValue('[data-testid="provider-form-timeout"]', '999');
    browser.click('[data-testid="provider-form-submit"]');
    browser.assert.containsText(
      '[data-testid="provider-dialog"]',
      'Enter a timeout between 1 and 120 seconds',
      'AC2: timeout range'
    );

    // Valid submission.
    browser.setValue('[data-testid="provider-form-timeout"]', '10');
    browser.click('[data-testid="provider-form-submit"]');
    browser.waitForElementPresent('[data-testid="provider-toast"]', 15000, 'AC2: created toast');
    browser.assert.containsText('[data-testid="provider-toast"]', 'Provider created.', 'AC2: created toast copy');

    // The API returns the created row with a masked hint and Not checked.
    apiGet(browser, org, '/api/v1/admin/providers?page.limit=100', (body) => {
      const p = (body.providers || []).find((x) => x.name === name);
      browser.assert.ok(p, 'AC2: created provider is listed');
      if (!p) {
        return;
      }
      browser.assert.equal(p.apiKeyHint, maskedHint(key), 'AC3: only the masked hint is returned');
      browser.assert.equal(
        p.lastProbe && p.lastProbe.status,
        'unknown',
        'AC2: a fresh provider is Not checked'
      );

      // Duplicate name is rejected with the 13002 copy.
      browser.click('[data-testid="provider-add"]');
      browser.waitForElementPresent('[data-testid="provider-dialog"]', 10000, 'AC2: dialog reopened');
      browser.setValue('[data-testid="provider-form-name"]', name);
      browser.setValue('[data-testid="provider-form-base-url"]', OK_BASE);
      browser.setValue('[data-testid="provider-form-api-key"]', 'sk-e2e-dup-123456');
      browser.click('[data-testid="provider-form-submit"]');
      browser.assert.containsText(
        '[data-testid="provider-dialog"]',
        'A provider with this name already exists.',
        'AC2: duplicate name rejected (13002)'
      );
      browser.click('[data-testid="provider-form-cancel"]');

      // Edit with an empty key keeps the credential and the hint (AC3).
      browser.url(browser.globals.baseUrl + '/admin/providers');
      browser.waitForElementPresent(`[data-testid="provider-edit-${p.providerId}"]`, 15000, 'AC3: edit action');
      browser.click(`[data-testid="provider-edit-${p.providerId}"]`);
      browser.waitForElementPresent('[data-testid="provider-dialog"]', 10000, 'AC3: edit dialog');
      browser.click('[data-testid="provider-form-submit"]');
      browser.waitForElementPresent('[data-testid="provider-toast"]', 15000, 'AC3: updated toast');
      browser.assert.containsText('[data-testid="provider-toast"]', 'Provider updated.', 'AC3: updated toast copy');

      apiGet(browser, org, `/api/v1/admin/providers/${p.providerId}`, (d) => {
        browser.assert.equal(d.provider.apiKeyHint, maskedHint(key), 'AC3: empty key keeps the credential');

        // A non-empty key rotates it and changes the hint.
        const rotated = 'sk-e2e-rotated-ABCD';
        api.request(browser, {
          method: 'PATCH',
          path: `/api/v1/admin/providers/${p.providerId}`,
          org,
          sessionRealm: ADMIN,
          body: {name, type: 'openai_compatible', baseUrl: OK_BASE, apiKey: rotated}
        }, (res) => {
          const body2 = api.assertOk(browser, res, 'AC3: rotate key');
          browser.assert.equal(
            body2.provider.apiKeyHint,
            maskedHint(rotated),
            'AC3: rotation changes the masked hint'
          );
          deleteProviders(browser, org, [p.providerId], () => {});
        });
      });
    });
  },

  'AC3: no read RPC or page ever returns the API key (plaintext or ciphertext)': function (browser) {
    const org = browser.globals.orgA;
    const name = `${FIXTURE_PREFIX}secret-${browser.globals.runId}`;
    const key = `sk-e2e-secret-${browser.globals.runId}-WXYZ`;
    createProvider(browser, org, {
      name, type: 'openai_compatible', baseUrl: OK_BASE, apiKey: key, enabled: false
    }, (p) => {
      // The list read never carries the plaintext key nor a ciphertext field.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/providers?page.limit=100',
        org,
        sessionRealm: ADMIN
      }, (res) => {
        api.assertOk(browser, res, 'AC3: list read');
        browser.assert.ok(res.text.indexOf(key) === -1, 'AC3: list JSON excludes the plaintext key');
        browser.assert.ok(res.text.indexOf('ciphertext') === -1, 'AC3: list JSON carries no ciphertext field');
        browser.assert.ok(res.text.indexOf(maskedHint(key)) >= 0, 'AC3: list JSON carries the masked hint');
      });
      // The detail read is equally masked.
      api.request(browser, {
        method: 'GET',
        path: `/api/v1/admin/providers/${p.providerId}`,
        org,
        sessionRealm: ADMIN
      }, (res) => {
        api.assertOk(browser, res, 'AC3: detail read');
        browser.assert.ok(res.text.indexOf(key) === -1, 'AC3: detail JSON excludes the plaintext key');
        browser.assert.ok(res.text.indexOf('ciphertext') === -1, 'AC3: detail JSON carries no ciphertext field');
      });

      // The rendered detail page never shows the key either.
      setOrgKeys(browser, org);
      browser.url(browser.globals.baseUrl + `/admin/providers/${p.providerId}`);
      browser.waitForElementPresent('[data-testid="provider-detail-key-hint"]', 20000, 'AC3: detail key hint');
      browser.assert.containsText(
        '[data-testid="provider-detail-key-hint"]',
        maskedHint(key),
        'AC3: the page shows the masked hint'
      );
      assertPageExcludes(browser, key, 'AC3');

      deleteProviders(browser, org, [p.providerId], () => {});
    });
  },

  // ---- AC4: the five probe states ----

  'AC4: the probe states map to ok/unauthorized/error/unreachable and stay distinct': function (browser) {
    const org = browser.globals.orgA;
    const suffix = browser.globals.runId;
    createProvider(browser, org, {
      name: `${FIXTURE_PREFIX}ok-${suffix}`, type: 'openai_compatible', baseUrl: OK_BASE,
      apiKey: 'sk-e2e-ok-123456', enabled: false
    }, (okP) => {
      createProvider(browser, org, {
        name: `${FIXTURE_PREFIX}unauth-${suffix}`, type: 'openai_compatible', baseUrl: UNAUTH_BASE,
        apiKey: 'sk-e2e-unauth-123456', enabled: false
      }, (unauthP) => {
        createProvider(browser, org, {
          name: `${FIXTURE_PREFIX}down-${suffix}`, type: 'openai_compatible', baseUrl: DOWN_BASE,
          apiKey: 'sk-e2e-down-123456', enabled: false
        }, (downP) => {
          createProvider(browser, org, {
            name: `${FIXTURE_PREFIX}dead-${suffix}`, type: 'openai_compatible', baseUrl: DEAD_BASE,
            apiKey: 'sk-e2e-dead-123456', enabled: false
          }, (deadP) => {
            const ids = [okP.providerId, unauthP.providerId, downP.providerId, deadP.providerId];
            // A never-probed provider stores `unknown`.
            browser.assert.equal(
              unauthP.lastProbe && unauthP.lastProbe.status,
              'unknown',
              'AC4: a fresh provider stores unknown'
            );

            const testProvider = (id, expected, httpStatus, label, next) => {
              api.request(browser, {
                method: 'POST',
                path: `/api/v1/admin/providers/${id}:test`,
                org,
                sessionRealm: ADMIN,
                body: {}
              }, (res) => {
                const body = api.assertOk(browser, res, label);
                const probe = body.probe || {};
                browser.assert.equal(probe.status, expected, `${label}: status`);
                browser.assert.equal(Number(probe.httpStatus), httpStatus, `${label}: http status`);
                browser.assert.ok(
                  String(probe.message || '').indexOf('Bearer') === -1,
                  `${label}: sanitized message excludes the credential`
                );
                next(probe.status);
              });
            };

            testProvider(okP.providerId, 'ok', 200, 'AC4 ok', () => {
              testProvider(unauthP.providerId, 'unauthorized', 401, 'AC4 unauthorized', (unauthStatus) => {
                testProvider(downP.providerId, 'error', 503, 'AC4 error', () => {
                  testProvider(deadP.providerId, 'unreachable', 0, 'AC4 unreachable', (unreachStatus) => {
                    browser.assert.notEqual(
                      unauthStatus,
                      unreachStatus,
                      'AC4: unauthorized is distinct from unreachable'
                    );
                    browser.assert.equal(unauthStatus, 'unauthorized', 'AC4: 401 maps to unauthorized');
                    browser.assert.equal(unreachStatus, 'unreachable', 'AC4: DNS failure maps to unreachable');
                    deleteProviders(browser, org, ids, () => {});
                  });
                });
              });
            });
          });
        });
      });
    });
  },

  // ---- AC4 / AC8: the UI probe panel and the enable guard ----

  'AC4/AC8: the detail panel shows Credentials rejected vs Unreachable and blocks enabling': function (browser) {
    const org = browser.globals.orgA;
    const suffix = browser.globals.runId;
    createProvider(browser, org, {
      name: `${FIXTURE_PREFIX}uiunauth-${suffix}`, type: 'openai_compatible', baseUrl: UNAUTH_BASE,
      apiKey: 'sk-e2e-uiunauth-123456', enabled: false
    }, (unauthP) => {
      createProvider(browser, org, {
        name: `${FIXTURE_PREFIX}uidead-${suffix}`, type: 'openai_compatible', baseUrl: DEAD_BASE,
        apiKey: 'sk-e2e-uidead-123456', enabled: false
      }, (deadP) => {
        setOrgKeys(browser, org);
        // Credentials rejected (401) renders unauthorized, never unreachable.
        browser.url(browser.globals.baseUrl + `/admin/providers/${unauthP.providerId}`);
        browser.waitForElementPresent('[data-testid="provider-test-now"]', 20000, 'AC4: Test connection action');
        browser.click('[data-testid="provider-test-now"]');
        // The unauthorized notice only renders when the probe returns
        // unauthorized, so waiting on it is the deterministic signal.
        browser.waitForElementPresent(
          '[data-testid="provider-test-unauthorized-notice"]',
          20000,
          'AC4: unauthorized notice'
        );
        browser.assert.containsText(
          '[data-testid="provider-test-status"]',
          'Credentials rejected',
          'AC4: unauthorized badge label'
        );
        browser.assert.containsText('[data-testid="provider-test-message"]', 'HTTP 401', 'AC4: 401 in the message');

        // Unreachable renders its own badge and notice (distinct from unauthorized).
        browser.url(browser.globals.baseUrl + `/admin/providers/${deadP.providerId}`);
        browser.waitForElementPresent('[data-testid="provider-test-now"]', 20000, 'AC4: Test connection action (dead)');
        browser.click('[data-testid="provider-test-now"]');
        browser.waitForElementPresent(
          '[data-testid="provider-test-unreachable-notice"]',
          20000,
          'AC4: unreachable notice'
        );
        browser.assert.containsText('[data-testid="provider-test-status"]', 'Unreachable', 'AC4: unreachable badge label');

        // AC8: enabling a provider whose last probe is unauthorized is blocked.
        browser.url(browser.globals.baseUrl + '/admin/providers');
        browser.waitForElementPresent(`[data-testid="provider-enabled-${unauthP.providerId}"]`, 20000, 'AC8: enabled toggle');
        browser.click(`[data-testid="provider-enabled-${unauthP.providerId}"]`);
        browser.waitForElementPresent('[data-testid="provider-action-error"]', 15000, 'AC8: enable blocked banner');
        browser.assert.containsText(
          '[data-testid="provider-action-error"]',
          'Credentials were rejected',
          'AC8: enabling an unauthorized provider is rejected (13012)'
        );

        deleteProviders(browser, org, [unauthP.providerId, deadP.providerId], () => {});
      });
    });
  },

  // ---- AC5: upstream-model curation ----

  'AC5: upstream-model curation add/duplicate/edit/remove (API + UI)': function (browser) {
    const org = browser.globals.orgA;
    const name = `${FIXTURE_PREFIX}curate-${browser.globals.runId}`;
    createProvider(browser, org, {
      name, type: 'openai_compatible', baseUrl: OK_BASE, apiKey: 'sk-e2e-curate-123456', enabled: true
    }, (p) => {
      addProviderModel(browser, org, p.providerId, {
        upstreamModelId: 'gpt-4o', displayName: 'E2E GPT-4o', contextWindow: 128000, enabled: true
      }, (m) => {
        // Missing upstream id -> 13010.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/providers/${p.providerId}/models`,
          org,
          sessionRealm: ADMIN,
          body: {upstreamModelId: '', enabled: true}
        }, (res) => {
          api.assertBusinessError(browser, res, 13010, 'AC5: empty upstream id (13010)');
        });
        // Duplicate upstream id -> 13009.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/providers/${p.providerId}/models`,
          org,
          sessionRealm: ADMIN,
          body: {upstreamModelId: 'gpt-4o', enabled: true}
        }, (res) => {
          api.assertBusinessError(browser, res, 13009, 'AC5: duplicate upstream id (13009)');
        });
        // Unknown row -> 13008.
        api.request(browser, {
          method: 'PATCH',
          path: `/api/v1/admin/providers/${p.providerId}/models/00000000-0000-0000-0000-000000000001`,
          org,
          sessionRealm: ADMIN,
          body: {displayName: 'x', contextWindow: 0, enabled: true}
        }, (res) => {
          api.assertBusinessError(browser, res, 13008, 'AC5: unknown model row (13008)');
        });

        // UI: the row renders; add a second model through the dialog.
        setOrgKeys(browser, org);
        browser.url(browser.globals.baseUrl + `/admin/providers/${p.providerId}`);
        browser.waitForElementPresent('[data-testid="provider-model-table"]', 20000, 'AC5: model table');
        browser.waitForElementPresent(`[data-testid="provider-model-row-${m.modelId}"]`, 15000, 'AC5: curated row');
        browser.click('[data-testid="provider-model-add"]');
        browser.waitForElementPresent('[data-testid="provider-model-form-upstream"]', 10000, 'AC5: add dialog');
        browser.setValue('[data-testid="provider-model-form-upstream"]', 'gpt-4o-mini');
        browser.setValue('[data-testid="provider-model-form-display"]', 'E2E GPT-4o mini');
        browser.setValue('[data-testid="provider-model-form-window"]', '64000');
        browser.click('[data-testid="provider-model-form-submit"]');
        browser.waitForElementPresent('[data-testid="provider-toast"]', 15000, 'AC5: added toast');
        browser.assert.containsText('[data-testid="provider-toast"]', 'Model added.', 'AC5: added toast copy');

        // Duplicate through the dialog shows the specified copy.
        browser.click('[data-testid="provider-model-add"]');
        browser.waitForElementPresent('[data-testid="provider-model-form-upstream"]', 10000, 'AC5: add dialog reopened');
        browser.setValue('[data-testid="provider-model-form-upstream"]', 'gpt-4o-mini');
        browser.click('[data-testid="provider-model-form-submit"]');
        browser.assert.containsText(
          '[data-testid="provider-model-dialog"]',
          'This upstream model is already curated.',
          'AC5: duplicate upstream id copy (13009)'
        );
        browser.click('[data-testid="provider-model-form-cancel"]');

        // Edit the first row, then remove the second (non-published) row.
        browser.click(`[data-testid="provider-model-edit-${m.modelId}"]`);
        browser.waitForElementPresent('[data-testid="provider-model-form-display"]', 10000, 'AC5: edit dialog');
        browser.setValue('[data-testid="provider-model-form-display"]', 'E2E GPT-4o edited');
        browser.click('[data-testid="provider-model-form-submit"]');
        browser.waitForElementPresent('[data-testid="provider-toast"]', 15000, 'AC5: updated toast');
        browser.assert.containsText('[data-testid="provider-toast"]', 'Model updated.', 'AC5: updated toast copy');

        apiGet(browser, org, `/api/v1/admin/providers/${p.providerId}/models?page.limit=100`, (body) => {
          const second = (body.models || []).find((x) => x.upstreamModelId === 'gpt-4o-mini');
          browser.assert.ok(second, 'AC5: second curated row exists');
          browser.click(`[data-testid="provider-model-remove-${second.modelId}"]`);
          browser.waitForElementPresent('[data-testid="provider-toast"]', 15000, 'AC5: removed toast');
          browser.assert.containsText('[data-testid="provider-toast"]', 'Model removed.', 'AC5: removed toast copy');
          deleteProviders(browser, org, [p.providerId], () => {});
        });
      });
    });
  },

  // ---- AC6 / AC7: publish, conflict, unpublish ----

  'AC6/AC7: publish, name conflict, unpublish and the publish guards (API + UI)': function (browser) {
    const org = browser.globals.orgA;
    const name = `${FIXTURE_PREFIX}pub-${browser.globals.runId}`;
    const tenantName = `${FIXTURE_PREFIX}pubmodel-${browser.globals.runId}`;
    api.seedSession(browser, USER, org);
    createProvider(browser, org, {
      name, type: 'openai_compatible', baseUrl: OK_BASE, apiKey: 'sk-e2e-pub-123456', enabled: true
    }, (p) => {
      addProviderModel(browser, org, p.providerId, {upstreamModelId: 'gpt-4o', enabled: true}, (a) => {
        addProviderModel(browser, org, p.providerId, {upstreamModelId: 'gpt-4o-mini', enabled: true}, (b) => {
          publishProviderModel(browser, org, p.providerId, a.modelId, tenantName, (pub) => {
            browser.assert.ok(pub.catalogModelId, 'AC6: publish returns the catalog model id');
            // Idempotent re-publish.
            publishProviderModel(browser, org, p.providerId, a.modelId, tenantName, () => {
              browser.assert.ok(true, 'AC6: re-publishing the same name is an idempotent success');
            });
            // A live name conflict on another row is 13014.
            api.request(browser, {
              method: 'POST',
              path: `/api/v1/admin/providers/${p.providerId}/models/${b.modelId}:publish`,
              org,
              sessionRealm: ADMIN,
              body: {tenantFacingName: tenantName}
            }, (res) => {
              api.assertBusinessError(browser, res, 13014, 'AC6: live name conflict (13014)');
            });

            // UI reflects the published row and the guards.
            setOrgKeys(browser, org);
            browser.url(browser.globals.baseUrl + `/admin/providers/${p.providerId}`);
            browser.waitForElementPresent(`[data-testid="provider-model-catalog-${a.modelId}"]`, 20000, 'AC6: catalog link');
            browser.assert.containsText(
              `[data-testid="provider-model-catalog-${a.modelId}"]`,
              tenantName,
              'AC6: catalog link shows the tenant-facing name'
            );
            browser.execute(function (sel) {
              const el = document.querySelector(sel);
              return el ? el.disabled : null;
            }, [`[data-testid="provider-model-remove-${a.modelId}"]`], (r) => {
              browser.assert.equal(r.value, true, 'AC8: a published row cannot be removed (disabled)');
            });

            // Publishing row B with the conflicting name shows the copy.
            browser.click(`[data-testid="provider-model-publish-${b.modelId}"]`);
            browser.waitForElementPresent('[data-testid="provider-publish-dialog"]', 10000, 'AC6: publish dialog');
            browser.setValue('[data-testid="provider-publish-name"]', tenantName);
            browser.click('[data-testid="provider-publish-confirm"]');
            browser.assert.containsText(
              '[data-testid="provider-publish-dialog"]',
              'A model with this name already exists.',
              'AC6: publish conflict copy (13014)'
            );
            browser.click('[data-testid="provider-publish-dialog"] button.secondary');

            // Unpublish row A through the confirmation (AC7).
            browser.click(`[data-testid="provider-model-unpublish-${a.modelId}"]`);
            browser.waitForElementPresent('[data-testid="provider-unpublish-dialog"]', 10000, 'AC7: unpublish dialog');
            browser.click('[data-testid="provider-unpublish-confirm"]');
            browser.waitForElementPresent('[data-testid="provider-toast"]', 15000, 'AC7: unpublished toast');
            browser.assert.containsText('[data-testid="provider-toast"]', 'Model unpublished.', 'AC7: unpublished toast copy');

            // The tenant catalog hides the unpublished model.
            api.request(browser, {
              method: 'GET',
              path: '/api/v1/models?page.limit=100',
              org,
              sessionRealm: USER
            }, (res) => {
              const body = api.assertOk(browser, res, 'AC7: tenant catalog');
              const found = (body.models || []).some((m) => m.modelId === pub.catalogModelId);
              browser.assert.equal(found, false, 'AC7: an unpublished model is hidden from the tenant catalog');
            });
            // Requests fail closed with 13015.
            api.request(browser, {
              method: 'POST',
              path: `/api/v1/models/${pub.catalogModelId}:playground`,
              org,
              sessionRealm: USER,
              body: {prompt: 'hi'}
            }, (res) => {
              api.assertBusinessError(browser, res, 13015, 'AC7: unpublished model fails closed (13015)');
            });

            // The freed name can be published on row B through the dialog.
            browser.click(`[data-testid="provider-model-publish-${b.modelId}"]`);
            browser.waitForElementPresent('[data-testid="provider-publish-dialog"]', 10000, 'AC6: publish dialog (B)');
            browser.setValue('[data-testid="provider-publish-name"]', tenantName);
            browser.click('[data-testid="provider-publish-confirm"]');
            browser.waitForElementPresent('[data-testid="provider-toast"]', 15000, 'AC6: published toast (B)');
            browser.assert.containsText('[data-testid="provider-toast"]', 'Model published.', 'AC6: published toast copy');

            deleteProviders(browser, org, [p.providerId], () => {});
          });
        });
      });
    });
  },

  // ---- AC7 / AC11: tenant-facing marker ----

  'AC7/AC11: the tenant catalog and detail carry the External marker, never provider internals': function (browser) {
    const org = browser.globals.orgA;
    const name = `${FIXTURE_PREFIX}tenant-${browser.globals.runId}`;
    const tenantName = `${FIXTURE_PREFIX}tenantmodel-${browser.globals.runId}`;
    api.seedSession(browser, USER, org);
    createProvider(browser, org, {
      name, type: 'openai_compatible', baseUrl: OK_BASE, apiKey: 'sk-e2e-tenant-123456', enabled: true
    }, (p) => {
      addProviderModel(browser, org, p.providerId, {
        upstreamModelId: 'gpt-4o', displayName: 'E2E GPT-4o', enabled: true
      }, (m) => {
        publishProviderModel(browser, org, p.providerId, m.modelId, tenantName, (pub) => {
          // The masked user projection carries external + providerName only.
          api.request(browser, {
            method: 'GET',
            path: '/api/v1/models?page.limit=100',
            org,
            sessionRealm: USER
          }, (res) => {
            const body = api.assertOk(browser, res, 'AC11: user catalog');
            const row = (body.models || []).find((x) => x.modelId === pub.catalogModelId);
            browser.assert.ok(row, 'AC11: the published external model is in the tenant catalog');
            browser.assert.equal(row.external, true, 'AC11: external marker');
            browser.assert.equal(row.providerName, name, 'AC11: provider name');
            const raw = JSON.stringify(row);
            browser.assert.ok(raw.indexOf('providerId') === -1, 'AC11: no provider id in the user projection');
            browser.assert.ok(raw.indexOf('upstreamModelId') === -1, 'AC11: no upstream id in the user projection');
            browser.assert.ok(raw.indexOf(OK_BASE) === -1, 'AC11: no base URL in the user projection');
          });

          // The user catalog page shows the External badge.
          setOrgKeys(browser, org);
          browser.url(browser.globals.baseUrl + '/models');
          browser.waitForElementPresent('[data-testid="models-table"]', 20000, 'AC11: user models table');
          browser.waitForElementPresent(`[data-testid="model-row-${tenantName}"]`, 15000, 'AC11: external model row');
          browser.waitForElementPresent('[data-testid="models-external-badge"]', 10000, 'AC11: External badge');

          // The user detail page shows the marker + provider, never the internals.
          browser.url(browser.globals.baseUrl + `/models/${pub.catalogModelId}`);
          browser.waitForElementPresent('[data-testid="model-detail-external-marker"]', 20000, 'AC11: detail External marker');
          browser.waitForElementPresent('[data-testid="model-detail-provider-name"]', 10000, 'AC11: detail provider name');
          browser.assert.containsText(
            '[data-testid="model-detail-provider-name"]',
            name,
            'AC11: detail names the provider'
          );
          assertPageExcludes(browser, OK_BASE, 'AC11');
          assertPageExcludes(browser, 'gpt-4o', 'AC11');
          assertPageExcludes(browser, p.providerId, 'AC11');

          // The playground lists the external model and calls it. The select
          // is React-controlled, so use the native value setter + change event.
          browser.url(browser.globals.baseUrl + '/playground');
          browser.waitForElementPresent('[data-testid="playground-model-select"]', 20000, 'AC11: playground model select');
          browser.execute(function (modelId) {
            const sel = document.getElementById('playground-model-select');
            if (!sel) {
              return false;
            }
            const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value').set;
            setter.call(sel, modelId);
            sel.dispatchEvent(new Event('change', {bubbles: true}));
            return sel.value === modelId;
          }, [pub.catalogModelId], (r) => {
            browser.assert.ok(r.value, 'AC11: the playground lists and selects the external model');
          });
          browser.setValue('[data-testid="playground-prompt-input"]', 'what is the answer?');
          browser.click('[data-testid="playground-send"]');
          browser.waitForElementPresent('[data-testid="playground-response"]', 25000, 'AC11: playground response');
          browser.assert.containsText('[data-testid="playground-response"]', '42', 'AC11: external completion');

          deleteProviders(browser, org, [p.providerId], () => {});
        });
      });
    });
  },

  // ---- AC9: external consumption + metering ----

  'AC9: an external model is consumed through the playground and metered': function (browser) {
    const org = browser.globals.orgA;
    const name = `${FIXTURE_PREFIX}meter-${browser.globals.runId}`;
    const tenantName = `${FIXTURE_PREFIX}metermodel-${browser.globals.runId}`;
    api.seedSession(browser, USER, org);
    createProvider(browser, org, {
      name, type: 'openai_compatible', baseUrl: OK_BASE, apiKey: 'sk-e2e-meter-123456', enabled: true
    }, (p) => {
      addProviderModel(browser, org, p.providerId, {upstreamModelId: 'gpt-4o', enabled: true}, (m) => {
        publishProviderModel(browser, org, p.providerId, m.modelId, tenantName, (pub) => {
          // Create an org API key for attribution.
          api.request(browser, {
            method: 'POST',
            path: '/api/v1/admin/auth/api-keys',
            org,
            sessionRealm: ADMIN,
            body: {name: `${FIXTURE_PREFIX}key-${browser.globals.runId}`}
          }, (res) => {
            const keyId = api.assertOk(browser, res, 'AC9: create API key').keyId;
            browser.assert.ok(keyId, 'AC9: API key id returned');

            // Consume the published external model through the playground API.
            api.request(browser, {
              method: 'POST',
              path: `/api/v1/models/${pub.catalogModelId}:playground`,
              org,
              sessionRealm: USER,
              body: {prompt: 'what is the answer?', apiKeyId: keyId}
            }, (res2) => {
              const body = api.assertOk(browser, res2, 'AC9: external playground');
              browser.assert.ok(
                String(body.completion || '').indexOf('42') >= 0,
                'AC9: completion proxied from the external service'
              );
              browser.assert.ok(Number(body.promptTokens) > 0, 'AC9: prompt tokens reported');
              browser.assert.ok(Number(body.completionTokens) > 0, 'AC9: completion tokens reported');

              // The metered voucher is recorded asynchronously.
              waitForVoucher(browser, org, pub.catalogModelId, keyId, 30000);
              deleteProviders(browser, org, [p.providerId], () => {});
            });
          });
        });
      });
    });
  },

  // ---- AC10: fail-closed mappings ----

  'AC10: disabled/unpublished/upstream-401/upstream-503 fail closed with distinct codes': function (browser) {
    const org = browser.globals.orgA;
    const suffix = browser.globals.runId;
    api.seedSession(browser, USER, org);
    const publish = (providerBody, upstream, tenantName, cb) => {
      createProvider(browser, org, providerBody, (p) => {
        addProviderModel(browser, org, p.providerId, {upstreamModelId: upstream, enabled: true}, (m) => {
          publishProviderModel(browser, org, p.providerId, m.modelId, tenantName, (pub) => {
            cb(p, m, pub);
          });
        });
      });
    };
    const playground = (catalogId, expected, label, cb) => {
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/models/${catalogId}:playground`,
        org,
        sessionRealm: USER,
        body: {prompt: 'hi', apiKeyId: 'e2e-key'}
      }, (res) => {
        api.assertBusinessError(browser, res, expected, label);
        cb();
      });
    };

    const ids = [];
    // Disabled provider -> 13011.
    publish({
      name: `${FIXTURE_PREFIX}dis-${suffix}`, type: 'openai_compatible', baseUrl: OK_BASE,
      apiKey: 'sk-e2e-dis-123456', enabled: false
    }, 'gpt-4o', `${FIXTURE_PREFIX}dis-model-${suffix}`, (p1, _m1, pub1) => {
      ids.push(p1.providerId);
      playground(pub1.catalogModelId, 13011, 'AC10: disabled provider (13011)', () => {
        // Upstream 401 -> 13012.
        publish({
          name: `${FIXTURE_PREFIX}u401-${suffix}`, type: 'openai_compatible', baseUrl: UNAUTH_BASE,
          apiKey: 'sk-e2e-u401-123456', enabled: true
        }, 'gpt-4o', `${FIXTURE_PREFIX}u401-model-${suffix}`, (p2, _m2, pub2) => {
          ids.push(p2.providerId);
          playground(pub2.catalogModelId, 13012, 'AC10: upstream 401 (13012)', () => {
            // Upstream 503 -> 13013.
            publish({
              name: `${FIXTURE_PREFIX}u503-${suffix}`, type: 'openai_compatible', baseUrl: DOWN_BASE,
              apiKey: 'sk-e2e-u503-123456', enabled: true
            }, 'gpt-4o', `${FIXTURE_PREFIX}u503-model-${suffix}`, (p3, _m3, pub3) => {
              ids.push(p3.providerId);
              playground(pub3.catalogModelId, 13013, 'AC10: upstream 503 (13013)', () => {
                // Unpublished model -> 13015.
                publish({
                  name: `${FIXTURE_PREFIX}unpub-${suffix}`, type: 'openai_compatible', baseUrl: OK_BASE,
                  apiKey: 'sk-e2e-unpub-123456', enabled: true
                }, 'gpt-4o', `${FIXTURE_PREFIX}unpub-model-${suffix}`, (p4, m4, pub4) => {
                  ids.push(p4.providerId);
                  api.request(browser, {
                    method: 'POST',
                    path: `/api/v1/admin/providers/${p4.providerId}/models/${m4.modelId}:unpublish`,
                    org,
                    sessionRealm: ADMIN,
                    body: {}
                  }, (res) => {
                    api.assertOk(browser, res, 'AC10: unpublish');
                    playground(pub4.catalogModelId, 13015, 'AC10: unpublished model (13015)', () => {
                      deleteProviders(browser, org, ids, () => {});
                    });
                  });
                });
              });
            });
          });
        });
      });
    });
  },

  // ---- AC15 + AC8 guards: empty / guarded / permission-denied ----

  'AC15: the empty and guarded states render': function (browser) {
    const org = browser.globals.orgA;
    const name = `${FIXTURE_PREFIX}guard-${browser.globals.runId}`;
    const tenantName = `${FIXTURE_PREFIX}guardmodel-${browser.globals.runId}`;
    createProvider(browser, org, {
      name, type: 'openai_compatible', baseUrl: OK_BASE, apiKey: 'sk-e2e-guard-123456', enabled: true
    }, (p) => {
      addProviderModel(browser, org, p.providerId, {upstreamModelId: 'gpt-4o', enabled: true}, (m) => {
        publishProviderModel(browser, org, p.providerId, m.modelId, tenantName, () => {
          setOrgKeys(browser, org);
          browser.url(browser.globals.baseUrl + '/admin/providers');
          browser.waitForElementPresent('[data-testid="provider-list"]', 20000, 'AC15: providers page');

          // Empty state on a no-match search.
          setInputValue(browser, 'provider-search', `nope-${browser.globals.runId}`);
          browser.waitForElementPresent('[data-testid="provider-empty"]', 15000, 'AC15: empty state');
          browser.waitForElementPresent('[data-testid="provider-empty-add"]', 10000, 'AC15: empty-state add action');
          setInputValue(browser, 'provider-search', '');

          // Guarded Delete: disabled while any model is published.
          browser.waitForElementPresent(`[data-testid="provider-delete-${p.providerId}"]`, 15000, 'AC15: delete action');
          browser.execute(function (sel) {
            const el = document.querySelector(sel);
            return el ? el.disabled : null;
          }, [`[data-testid="provider-delete-${p.providerId}"]`], (r) => {
            browser.assert.equal(r.value, true, 'AC8: Delete disabled while a model is published');
          });
          // The API enforces the same guard (13016).
          api.request(browser, {
            method: 'DELETE',
            path: `/api/v1/admin/providers/${p.providerId}`,
            org,
            sessionRealm: ADMIN
          }, (res) => {
            api.assertBusinessError(browser, res, 13016, 'AC8: deleting an in-use provider (13016)');
          });

          deleteProviders(browser, org, [p.providerId], () => {});
        });
      });
    });
  },

  'AC13: a session without the required role receives 10036 and the page denies permission': function (browser) {
    const org = `org-e2e-emp-nomember-${browser.globals.runId}`;
    api.ensureOrg(browser, org);
    // Seed an admin-realm session that is NOT a member of the new org.
    api.seedSession(browser, ADMIN, org, null, null, {noMember: true});
    setOrgKeys(browser, org);

    // The API rejects the non-member with 10036.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/providers?page.limit=10',
      org,
      sessionRealm: ADMIN
    }, (res) => {
      api.assertBusinessError(browser, res, 10036, 'AC13: non-member admin session (10036)');
    });

    // The page shows the permission-denied state with a link back to /admin.
    browser.url(browser.globals.baseUrl + '/admin/providers');
    browser.waitForElementPresent('[data-testid="provider-permission-denied"]', 20000, 'AC13: permission-denied state');
    browser.waitForElementPresent('[data-testid="provider-back-admin"]', 10000, 'AC13: back-to-admin link');
  },

  // ---- AC12: realm separation ----

  'AC12: realm separation — user session 10038, bare prefix 404, /providers not served': function (browser) {
    const org = browser.globals.orgA;
    api.seedSession(browser, USER, org);

    // A valid user-realm session on the admin prefix is a realm mismatch.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/providers?page.limit=10',
      org,
      sessionRealm: USER
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC12: user session on the admin providers API (10038)');
    });

    // The providers API does not exist on the bare /api/v1 prefix.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/providers?page.limit=10',
      org,
      sessionRealm: USER
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC12: bare providers prefix is 404');
      browser.assert.equal(res.body && res.body.code, 5, 'AC12: bare providers prefix code 5');
    });

    // The admin console never calls a bare /api/v1 route: an admin session on
    // the user prefix is rejected as a realm mismatch.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/models?page.limit=1',
      org,
      sessionRealm: ADMIN
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC12: admin session on the user API prefix (10038)');
    });

    // /providers is not a user-console route: the wildcard NotFoundPage renders.
    setOrgKeys(browser, org);
    browser.url(browser.globals.baseUrl + '/providers');
    browser.waitForElementPresent('[data-testid="not-found-home"]', 20000, 'AC12: user console serves NotFound for /providers');
    browser.assert.not.elementPresent('[data-testid="provider-list"]', 'AC12: no providers page on the user console');

    // With no admin session, /admin/providers is not served: the admin shell
    // redirects to the admin login.
    browser.execute(function () {
      localStorage.removeItem('go-taas.admin.session-token');
    });
    browser.url(browser.globals.baseUrl + '/admin/providers');
    browser.pause(2000);
    browser.assert.not.elementPresent(
      '[data-testid="provider-list"]',
      'AC12: no providers page without an admin session'
    );
    browser.execute(function () {
      return window.location.pathname;
    }, [], (r) => {
      browser.assert.ok(
        String(r.value || '').indexOf('/admin/login') >= 0,
        `AC12: redirected to the admin login (got ${r.value})`
      );
    });
  }
};
