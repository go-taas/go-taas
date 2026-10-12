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

// Feature-47 (distributed inference runtime / AIBrix integration) e2e
// suite, run against the compose stack gateway + cluster. Covers the
// acceptance criteria of docs/design/distributed-inference-runtime.md
// reachable from the outside:
//
//   - AC1  the admin page /admin/runtime renders from the admin nav
//   - AC2  the component table lists the AIBrix control plane + Envoy AI
//          Gateway when the scheme is aibrix
//   - AC3  the scheme switcher round-trips plain <-> aibrix with a
//          confirmation dialog and is idempotent
//   - AC4  the registered-model table renders and its search/engine/status
//          filters narrow the list, with pagination
//   - AC5  the register-model dialog + API validate the fields (12902,
//          12903, 12904, 12905, 12906, 12907)
//   - AC6  deregister asks for confirmation and removes the ModelClaim
//   - AC8  realm separation: user session on /api/v1/admin/runtime/* is
//          10038; the runtime API is not on the bare /api/v1 prefix; the
//          /runtime page is not served by the user console
//   - AC9  a session without the admin role receives 10036 and the page
//          shows the permission-denied state
//   - AC11 the plain scheme shows the fallback components, disables the
//          write actions and rejects a registration with 12901
//   - AC12 the healthy state renders without an unavailable notice and
//          survives a Refresh
//
// The compose controller observes the real cluster `sd4090`, so the
// AIBrix control plane, the Envoy AI Gateway and the ModelClaim projection
// are the real integration surface. Runtime availability is a projection
// of the cluster state published on a ~30s interval, so the suite polls
// for `availability=available` before asserting scheme/registration
// behavior instead of treating the warm-up as a failure. AC7 (the
// runtime-unavailable degradation) needs a cluster outage that cannot be
// forced against the live stack; it is covered by the controller/service
// unit tests and only the healthy slice is asserted here. AC10 (audit
// events) is FVT scope.

const { execSync } = require('child_process');
const api = require('../page-objects/api.js');

const ADMIN_REALM = 'admin';
const WARMUP_MS = 120000;

// sleepSync blocks without spawning a process (test queue is synchronous).
function sleepSync(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

// listModelClaimNames returns the ModelClaim names visible to the local
// kubectl (the observable AIBrix integration), or [] when kubectl is
// unavailable.
function listModelClaimNames() {
  try {
    const out = execSync('kubectl -n taas get modelclaims -o name', {
      stdio: ['ignore', 'pipe', 'ignore'],
      timeout: 30000
    }).toString();
    return out
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
      .map((l) => l.replace(/^.*\//, ''));
  } catch (e) {
    return [];
  }
}

// waitForModelClaim polls kubectl until `mc-<name>` is present (or absent)
// or the deadline passes.
function waitForModelClaim(name, present, timeoutMs) {
  const claim = `mc-${name}`;
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const has = listModelClaimNames().includes(claim);
    if (has === present) {
      return true;
    }
    if (Date.now() > deadline) {
      return false;
    }
    sleepSync(2000);
  }
}

// deleteModelClaim force-removes a fixture ModelClaim (best-effort) so the
// cluster is left tidy even if a test aborts before its UI cleanup.
function deleteModelClaim(name) {
  try {
    execSync(`kubectl -n taas delete modelclaim mc-${name} --ignore-not-found`, {
      stdio: ['ignore', 'ignore', 'ignore'],
      timeout: 30000
    });
  } catch (e) {
    // Best-effort cleanup.
  }
}

// waitForRuntime polls GET /api/v1/admin/runtime from inside the browser
// until availability is `available` (and optionally the persisted scheme
// matches and the snapshot contains a component with id/name
// `wantComponent`), tolerating the ~30s snapshot warm-up of a cold stack
// (AD4/D8). The scheme is persisted independently of the controller
// snapshot, so the component check is what proves the scheme's component
// set has actually landed.
function waitForRuntime(browser, org, wantScheme, wantComponent, timeoutMs) {
  const path = '/api/v1/admin/runtime';
  browser
    .timeoutsAsyncScript(timeoutMs + 10000)
    .executeAsync(function ({url, wantScheme, wantComponent, deadline}, done) {
      const token = localStorage.getItem('go-taas.admin.session-token');
      const start = Date.now();
      const tick = () => {
        const headers = {};
        if (token) headers.Authorization = `Bearer ${token}`;
        fetch(url, {headers})
          .then((r) => r.json())
          .then((body) => {
            const rt = body && body.runtime;
            const comps = (rt && rt.components) || [];
            const hasComponent =
              !wantComponent ||
              comps.some((c) => c.componentId === wantComponent || c.name === wantComponent);
            const ok =
              rt &&
              rt.availability === 'available' &&
              (!wantScheme || rt.scheme === wantScheme) &&
              hasComponent;
            if (ok) {
              return done({
                ok: true,
                scheme: rt.scheme,
                availability: rt.availability,
                components: comps.length
              });
            }
            if (Date.now() - start > deadline) {
              return done({
                ok: false,
                scheme: rt && rt.scheme,
                availability: rt && rt.availability,
                components: comps.map((c) => c.componentId)
              });
            }
            setTimeout(tick, 3000);
          })
          .catch((err) => {
            if (Date.now() - start > deadline) {
              return done({ok: false, error: String(err)});
            }
            setTimeout(tick, 3000);
          });
      };
      tick();
    }, [{
      url: api.url(browser, path),
      wantScheme: wantScheme || null,
      wantComponent: wantComponent || null,
      deadline: timeoutMs
    }], (result) => {
      const value = result && result.value !== undefined ? result.value : result;
      browser.assert.ok(
        value && value.ok,
        `runtime warm-up (availability=available${wantScheme ? `, scheme=${wantScheme}` : ''}${wantComponent ? `, component=${wantComponent}` : ''}) — ${JSON.stringify(value)}`
      );
    });
  return browser;
}

// ensureAibrix warms up the runtime then guarantees the persisted scheme is
// aibrix and a full aibrix snapshot (with the control-plane components) is
// served.
function ensureAibrix(browser, org) {
  waitForRuntime(browser, org, null, null, WARMUP_MS);
  api.request(browser, {
    method: 'PUT',
    path: '/api/v1/admin/runtime/scheme',
    org,
    sessionRealm: ADMIN_REALM,
    body: {scheme: 'aibrix'}
  }, (res) => {
    if (res.body && res.body.code === 12901) {
      browser.assert.fail('ensureAibrix: runtime reported unavailable after warm-up');
      return;
    }
    api.assertOk(browser, res, 'ensureAibrix: PUT scheme aibrix');
  });
  waitForRuntime(browser, org, 'aibrix', 'aibrix-controller-manager', WARMUP_MS);
  return browser;
}

module.exports = {
  '@tags': ['distributed-inference-runtime', 'feature-47'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-rt-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, ADMIN_REALM, browser.globals.orgA);
  },

  afterEach(browser) {
    // Best-effort: keep the platform on the AIBrix scheme between tests so
    // a partially completed suite never leaves the plain scheme behind.
    // (AIBrix is the scheme the runtime is verified against; switching to
    // it is a no-op when already active and is rejected only while the
    // runtime is unavailable, which the next test's warm-up absorbs.)
    browser.timeoutsAsyncScript(15000).executeAsync(function (url, done) {
      const token = localStorage.getItem('go-taas.admin.session-token');
      if (!token) return done(null);
      fetch(url, {
        method: 'PUT',
        headers: {'Content-Type': 'application/json', Authorization: `Bearer ${token}`},
        body: JSON.stringify({scheme: 'aibrix'})
      })
        .then(() => done(null))
        .catch(() => done(null));
    }, [api.url(browser, '/api/v1/admin/runtime/scheme')], () => {});
    browser.end();
  },

  after(browser, done) {
    // Safety net: remove any fixture ModelClaims the suite created.
    for (const claim of listModelClaimNames()) {
      if (claim.startsWith('mc-rt-e2e-')) {
        deleteModelClaim(claim.slice(3));
      }
    }
    done();
  },

  // ---- AC1: admin page + navigation ----

  'AC1: /admin/runtime renders the overview, components and models regions from the admin nav': function (browser) {
    const org = browser.globals.orgA;
    waitForRuntime(browser, org, null, null, WARMUP_MS);
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/runtime');
    browser.waitForElementPresent('[data-testid="runtime-page"]', 20000, 'AC1: runtime page renders');
    browser.waitForElementPresent('[data-testid="nav-runtime"]', 10000, 'AC1: nav-runtime entry');
    browser.waitForElementPresent('[data-testid="runtime-overview"]', 10000, 'AC1: overview card');
    browser.waitForElementPresent('[data-testid="runtime-scheme-badge"]', 10000, 'AC1: scheme badge');
    browser.waitForElementPresent('[data-testid="runtime-availability-badge"]', 10000, 'AC1: availability badge');
    browser.waitForElementPresent('[data-testid="runtime-last-checked"]', 10000, 'AC1: last checked');
    browser.waitForElementPresent('[data-testid="runtime-switch-scheme"]', 10000, 'AC1: switch scheme action');
    browser.waitForElementPresent('[data-testid="runtime-refresh"]', 10000, 'AC1: refresh action');
    browser.waitForElementPresent('[data-testid="runtime-components-table"]', 15000, 'AC1: components table');
    // The registered-model region renders either the table (when the
    // cluster already has ModelClaims) or the explanatory empty state.
    browser.execute(function () {
      return Boolean(
        document.querySelector('[data-testid="runtime-models-table"]') ||
          document.querySelector('[data-testid="runtime-models-empty"]')
      );
    }, [], (result) => {
      browser.assert.ok(result.value, 'AC1: models region renders (table or empty state)');
    });
  },

  // ---- AC2: AIBrix control plane + Envoy AI Gateway ----

  'AC2: the component table lists the AIBrix control plane and the Envoy AI Gateway': function (browser) {
    const org = browser.globals.orgA;
    ensureAibrix(browser, org);
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/runtime');
    browser.waitForElementPresent('[data-testid="runtime-components-table"]', 20000, 'AC2: components table');
    const controlPlane = [
      'aibrix-controller-manager',
      'aibrix-gateway-plugins',
      'aibrix-gpu-optimizer',
      'aibrix-metadata-service',
      'aibrix-redis-master'
    ];
    for (const name of controlPlane) {
      browser.waitForElementPresent(
        `[data-testid="runtime-component-${name}"]`,
        15000,
        `AC2: ${name} row`
      );
      browser.assert.containsText(
        `[data-testid="runtime-component-${name}"]`,
        'control_plane',
        `AC2: ${name} kind`
      );
      browser.assert.containsText(
        `[data-testid="runtime-component-${name}"]`,
        'ready',
        `AC2: ${name} status`
      );
    }
    browser.waitForElementPresent(
      '[data-testid="runtime-component-envoy-aibrix-system-aibrix-eg"]',
      15000,
      'AC2: Envoy AI Gateway row'
    );
    browser.assert.containsText(
      '[data-testid="runtime-component-envoy-aibrix-system-aibrix-eg"]',
      'gateway',
      'AC2: Envoy AI Gateway kind'
    );
    // Column headers required by the design (§5.3).
    browser.execute(function () {
      const table = document.querySelector('[data-testid="runtime-components-table"]');
      return table ? table.textContent || '' : '';
    }, [], (result) => {
      const text = result.value || '';
      browser.assert.ok(text.includes('Namespace'), 'AC2: Namespace column');
      browser.assert.ok(text.includes('Ready'), 'AC2: Ready column');
    });
  },

  // ---- AC3: serving-scheme switcher ----

  'AC3: the scheme switcher round-trips AIBrix to Plain and back with confirmation': function (browser) {
    const org = browser.globals.orgA;
    ensureAibrix(browser, org);
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/runtime');
    browser.waitForElementPresent('[data-testid="runtime-scheme-badge"]', 20000, 'AC3: scheme badge');
    browser.assert.containsText('[data-testid="runtime-scheme-badge"]', 'AIBrix', 'AC3: current scheme AIBrix');

    // Open the switcher; the current scheme is preselected so Apply is a
    // no-op (idempotent, AD7).
    browser.click('[data-testid="runtime-switch-scheme"]');
    browser.waitForElementPresent('[data-testid="runtime-scheme-dialog"]', 10000, 'AC3: scheme dialog');
    browser.waitForElementPresent('[data-testid="runtime-scheme-option-plain"]', 10000, 'AC3: plain option');
    browser.waitForElementPresent('[data-testid="runtime-scheme-option-aibrix"]', 10000, 'AC3: aibrix option');
    browser.assert.attributeEquals(
      '[data-testid="runtime-scheme-apply"]',
      'disabled',
      'true',
      'AC3: Apply disabled while the same scheme is selected'
    );
    // Switch to plain and confirm.
    browser.click('[data-testid="runtime-scheme-option-plain"] input');
    browser.pause(500);
    browser.click('[data-testid="runtime-scheme-apply"]');
    browser.waitForElementPresent('[data-testid="runtime-scheme-confirm"]', 10000, 'AC3: confirm dialog');
    browser.assert.containsText(
      '[data-testid="runtime-scheme-confirm"]',
      'Switch serving scheme',
      'AC3: confirm copy'
    );
    browser.click('[data-testid="runtime-scheme-confirm-switch"]');
    browser.waitForElementPresent('[data-testid="runtime-toast"]', 10000, 'AC3: scheme updated toast');
    browser.pause(2000);
    browser.assert.containsText(
      '[data-testid="runtime-scheme-badge"]',
      'Plain Deployment',
      'AC3: scheme badge now Plain'
    );

    // Switch back to AIBrix and confirm the badge updates.
    browser.click('[data-testid="runtime-switch-scheme"]');
    browser.waitForElementPresent('[data-testid="runtime-scheme-dialog"]', 10000, 'AC3: scheme dialog reopened');
    browser.click('[data-testid="runtime-scheme-option-aibrix"] input');
    browser.pause(500);
    browser.click('[data-testid="runtime-scheme-apply"]');
    browser.waitForElementPresent('[data-testid="runtime-scheme-confirm"]', 10000, 'AC3: confirm dialog reopened');
    browser.click('[data-testid="runtime-scheme-confirm-switch"]');
    browser.pause(2000);
    browser.assert.containsText(
      '[data-testid="runtime-scheme-badge"]',
      'AIBrix',
      'AC3: scheme badge back to AIBrix'
    );
  },

  // ---- AC4/AC5/AC6: register, filters, duplicate, deregister ----

  'AC4/AC5/AC6: register a model, observe the ModelClaim, then deregister': function (browser) {
    const org = browser.globals.orgA;
    const name = `rt-e2e-${browser.globals.runId}`;
    const artifact = '/data/weights/rt-e2e';
    ensureAibrix(browser, org);
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/runtime');
    browser.waitForElementPresent('[data-testid="runtime-register-model"]', 20000, 'AC5: register action');

    // Register through the dialog (AC5).
    browser.click('[data-testid="runtime-register-model"]');
    browser.waitForElementPresent('[data-testid="runtime-register-dialog"]', 10000, 'AC5: register dialog');
    browser.setValue('[data-testid="runtime-register-field-name"]', name);
    // Engine defaults to vllm; replicas to 1.
    browser.setValue('[data-testid="runtime-register-field-artifact"]', artifact);
    browser.click('[data-testid="runtime-register-submit"]');
    browser.waitForElementPresent('[data-testid="runtime-toast"]', 15000, 'AC5: registered toast');
    browser.assert.containsText('[data-testid="runtime-toast"]', 'Model registered', 'AC5: toast copy');

    // The registration drives a ModelClaim reconcile; the projection (and
    // therefore the row) lands on the next snapshot (~30s).
    browser.waitForElementPresent(
      `[data-testid="runtime-model-${name}"]`,
      90000,
      'AC5: registered model row'
    );
    browser.assert.containsText(`[data-testid="runtime-model-${name}"]`, 'vllm', 'AC5: engine badge');
    browser.waitForElementPresent('[data-testid="runtime-models-pagination"]', 10000, 'AC4: pagination');

    // AC4: the name search and the engine filter narrow the table.
    browser.setValue('[data-testid="runtime-model-search"]', 'zzz-no-such-model');
    browser.waitForElementNotPresent(
      `[data-testid="runtime-model-${name}"]`,
      15000,
      'AC4: non-matching search filters the row out'
    );
    browser.setValue('[data-testid="runtime-model-search"]', name);
    browser.waitForElementPresent(
      `[data-testid="runtime-model-${name}"]`,
      15000,
      'AC4: name search keeps the row'
    );
    browser.clearValue('[data-testid="runtime-model-search"]');
    browser.click('[data-testid="runtime-engine-filter"] option[value="sglang"]');
    browser.waitForElementNotPresent(
      `[data-testid="runtime-model-${name}"]`,
      15000,
      'AC4: sglang engine filter excludes the vllm model'
    );
    browser.click('[data-testid="runtime-engine-filter"] option[value="vllm"]');
    browser.waitForElementPresent(
      `[data-testid="runtime-model-${name}"]`,
      15000,
      'AC4: vllm engine filter keeps the row'
    );
    browser.click('[data-testid="runtime-engine-filter"] option[value=""]');

    // The cluster is the source of truth: the ModelClaim must exist (AD5).
    browser.perform((done) => {
      browser.assert.ok(
        waitForModelClaim(name, true, 60000),
        'AC5: ModelClaim mc-<name> exists on the cluster'
      );
      done();
    });

    // AC5 (12904): once the ModelClaim is observed, a duplicate
    // registration is rejected.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/runtime/models',
      org,
      sessionRealm: ADMIN_REALM,
      body: {model_name: name, engine: 'vllm', replicas: 1, artifact_url: artifact}
    }, (res) => {
      api.assertBusinessError(browser, res, 12904, 'AC5: duplicate registration -> 12904');
    });

    // Deregister through the confirmation dialog (AC6).
    browser.click(`[data-testid="runtime-deregister-${name}"]`);
    browser.waitForElementPresent('[data-testid="runtime-deregister-confirm"]', 10000, 'AC6: deregister dialog');
    browser.assert.containsText(
      '[data-testid="runtime-deregister-confirm"]',
      `Deregister ${name}`,
      'AC6: deregister confirmation copy'
    );
    browser.click('[data-testid="runtime-deregister-submit"]');
    browser.waitForElementNotPresent(
      `[data-testid="runtime-model-${name}"]`,
      120000,
      'AC6: row removed after deregister'
    );
    browser.perform((done) => {
      browser.assert.ok(
        waitForModelClaim(name, false, 90000),
        'AC6: ModelClaim removed from the cluster'
      );
      // Belt-and-braces: never leave the fixture behind.
      deleteModelClaim(name);
      done();
    });
  },

  // ---- AC5: registration validation (admin API) ----

  'AC5: registration validation errors are returned on the admin API': function (browser) {
    const org = browser.globals.orgA;
    ensureAibrix(browser, org);
    const base = {model_name: 'rt-e2e-validate', engine: 'vllm', replicas: 1, artifact_url: '/data/x'};

    // Invalid serving scheme -> 12902.
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/runtime/scheme',
      org,
      sessionRealm: ADMIN_REALM,
      body: {scheme: 'bogus'}
    }, (res) => {
      api.assertBusinessError(browser, res, 12902, 'AC5: invalid scheme -> 12902');
    });

    // Unsupported engine -> 12905.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/runtime/models',
      org,
      sessionRealm: ADMIN_REALM,
      body: Object.assign({}, base, {engine: 'trtllm'})
    }, (res) => {
      api.assertBusinessError(browser, res, 12905, 'AC5: unsupported engine -> 12905');
    });

    // Missing artifact URL -> 12907.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/runtime/models',
      org,
      sessionRealm: ADMIN_REALM,
      body: {model_name: 'rt-e2e-noartifact', engine: 'vllm', replicas: 1}
    }, (res) => {
      api.assertBusinessError(browser, res, 12907, 'AC5: missing artifact_url -> 12907');
    });

    // Invalid model name -> 12907.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/runtime/models',
      org,
      sessionRealm: ADMIN_REALM,
      body: Object.assign({}, base, {model_name: 'Bad Name'})
    }, (res) => {
      api.assertBusinessError(browser, res, 12907, 'AC5: invalid model name -> 12907');
    });

    // Negative replicas -> 12906.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/runtime/models',
      org,
      sessionRealm: ADMIN_REALM,
      body: Object.assign({}, base, {model_name: 'rt-e2e-neg', replicas: -1})
    }, (res) => {
      api.assertBusinessError(browser, res, 12906, 'AC5: negative replicas -> 12906');
    });

    // Unknown model on deregister -> 12903.
    api.request(browser, {
      method: 'DELETE',
      path: '/api/v1/admin/runtime/models/rt-e2e-does-not-exist',
      org,
      sessionRealm: ADMIN_REALM
    }, (res) => {
      api.assertBusinessError(browser, res, 12903, 'AC5: unknown deregister -> 12903');
    });
  },

  // ---- AC11: plain scheme fallback + disabled writes + 12901 ----

  'AC11: the plain scheme shows the fallback components, disables writes and rejects registration': function (browser) {
    const org = browser.globals.orgA;
    waitForRuntime(browser, org, null, null, WARMUP_MS);

    // Switch to plain (allowed regardless of availability).
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/runtime/scheme',
      org,
      sessionRealm: ADMIN_REALM,
      body: {scheme: 'plain'}
    }, (res) => {
      api.assertOk(browser, res, 'AC11: PUT scheme plain');
    });
    // The controller publishes the plain (fallback) snapshot on its next
    // collection; the page reflects it on the following poll.
    waitForRuntime(browser, org, 'plain', 'go-taas-controller', WARMUP_MS);

    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/runtime');
    browser.waitForElementPresent('[data-testid="runtime-scheme-badge"]', 20000, 'AC11: scheme badge');
    browser.assert.containsText(
      '[data-testid="runtime-scheme-badge"]',
      'Plain Deployment',
      'AC11: scheme badge Plain'
    );
    browser.waitForElementPresent(
      '[data-testid="runtime-component-go-taas-controller"]',
      20000,
      'AC11: fallback controller component'
    );
    browser.waitForElementPresent(
      '[data-testid="runtime-component-kubernetes-api"]',
      20000,
      'AC11: fallback Kubernetes API component'
    );
    // Writes are gated off under the plain scheme (AD12).
    browser.assert.attributeEquals(
      '[data-testid="runtime-register-model"]',
      'disabled',
      'true',
      'AC11: Register model disabled under plain'
    );
    browser.assert.attributeContains(
      '[data-testid="runtime-register-model"]',
      'title',
      'AIBrix scheme',
      'AC11: disabled Register tooltip points at the AIBrix scheme'
    );
    browser.waitForElementPresent('[data-testid="runtime-models-empty"]', 10000, 'AC11: explanatory empty state');
    browser.assert.containsText(
      '[data-testid="runtime-models-empty"]',
      'Plain Deployment',
      'AC11: empty-state copy names Plain Deployment'
    );

    // A direct registration under plain is rejected with 12901.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/runtime/models',
      org,
      sessionRealm: ADMIN_REALM,
      body: {model_name: 'rt-e2e-plain', engine: 'vllm', replicas: 1, artifact_url: '/data/x'}
    }, (res) => {
      api.assertBusinessError(browser, res, 12901, 'AC11: register under plain -> 12901');
    });

    // Restore the AIBrix scheme so the stack is left as the suite found it.
    api.request(browser, {
      method: 'PUT',
      path: '/api/v1/admin/runtime/scheme',
      org,
      sessionRealm: ADMIN_REALM,
      body: {scheme: 'aibrix'}
    }, (res) => {
      api.assertOk(browser, res, 'AC11: restore scheme aibrix');
    });
    waitForRuntime(browser, org, 'aibrix', 'aibrix-controller-manager', WARMUP_MS);
  },

  // ---- AC8: realm separation ----

  'AC8: a user-realm session is rejected on the admin runtime API with 10038': function (browser) {
    const org = browser.globals.orgA;
    api.seedSession(browser, 'user', org);
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/runtime',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC8: user session on admin runtime -> 10038');
    });
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/runtime/models',
      org,
      sessionRealm: 'user',
      body: {model_name: 'rt-e2e-realm', engine: 'vllm', replicas: 1, artifact_url: '/data/x'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC8: user session on admin register -> 10038');
    });
  },

  'AC8: the runtime API is not on the bare user prefix and the page is not on the user console': function (browser) {
    const org = browser.globals.orgA;
    api.seedSession(browser, 'user', org);
    // A user-realm session passes the user-surface realm guard and hits
    // the gateway 404 — the admin runtime routes are not bound there.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/runtime',
      org,
      sessionRealm: 'user'
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC8: bare /api/v1/runtime is 404');
      browser.assert.equal(res.body && res.body.code, 5, 'AC8: bare /api/v1/runtime code 5');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/runtime/models',
      org,
      sessionRealm: 'user'
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC8: bare /api/v1/runtime/models is 404');
    });
    // The user console has no /runtime route: it renders the not-found page.
    browser.execute(`localStorage.setItem('go-taas.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/runtime');
    browser.waitForElementPresent('[data-testid="not-found-home"]', 15000, 'AC8: user console not-found page');
    browser.assert.not.elementPresent('[data-testid="runtime-page"]', 'AC8: runtime page not served on the user console');
    browser.assert.not.elementPresent('[data-testid="nav-runtime"]', 'AC8: no runtime nav in the user console');
  },

  // ---- AC9: permission denied ----

  'AC9: a session without the admin role receives 10036 and shows the permission-denied page': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId
      .toString()
      .padStart(12, '0')
      .slice(-12)}`;
    api.seedSession(browser, ADMIN_REALM, org, 'admin', nonMemberUser, {noMember: true});
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/runtime',
      org,
      sessionRealm: ADMIN_REALM
    }, (res) => {
      api.assertBusinessError(browser, res, 10036, 'AC9: non-member session -> 10036');
    });
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/runtime');
    browser.waitForElementPresent('[data-testid="runtime-permission-denied"]', 20000, 'AC9: permission-denied state');
    browser.waitForElementPresent('[data-testid="runtime-back-admin"]', 10000, 'AC9: back-to-admin link');
  },

  // ---- AC12: healthy state + refresh ----

  'AC12: the healthy runtime page shows no unavailable notice and survives a refresh': function (browser) {
    const org = browser.globals.orgA;
    ensureAibrix(browser, org);
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/runtime');
    browser.waitForElementPresent('[data-testid="runtime-page"]', 20000, 'AC12: page renders');
    browser.waitForElementPresent('[data-testid="runtime-availability-badge"]', 10000, 'AC12: availability badge');
    browser.assert.containsText(
      '[data-testid="runtime-availability-badge"]',
      'Available',
      'AC12: availability Available'
    );
    browser.assert.not.elementPresent(
      '[data-testid="runtime-unavailable-notice"]',
      'AC12: no unavailable notice while available'
    );
    browser.assert.not.elementPresent('[data-testid="runtime-error"]', 'AC12: no error banner');
    browser.click('[data-testid="runtime-refresh"]');
    browser.waitForElementPresent('[data-testid="runtime-overview"]', 10000, 'AC12: overview after refresh');
    browser.waitForElementPresent('[data-testid="runtime-components-table"]', 15000, 'AC12: components after refresh');
  }
};
