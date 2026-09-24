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

// Feature-01 (api-key-management) e2e suite, run against the compose stack
// gateway. Covers the API-level acceptance criteria of
// docs/design/api-key-management.md (AC1-AC7, AC10); console-dialog criteria
// (AC8/AC9) are manual and out of scope here.

const api = require('../page-objects/api.js');

// sk- prefix + 43 base62 chars (design D1).
const PLAINTEXT_RE = /^sk-[A-Za-z0-9]{43}$/;

module.exports = {
  '@tags': ['auth', 'api-key', 'feature-01'],

  before(browser) {
    // Nothing: each test navigates in beforeEach because afterEach ends
    // the session and a fresh page starts at about:blank, where a
    // cross-origin fetch to the gateway would fail (null origin).
  },

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    // Fresh organizations per run: the compose DB persists between runs, so
    // unique org ids keep every case isolated and repeatable.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.orgA = `org-e2e-${browser.globals.runId}`;
    browser.globals.orgB = `org-other-${browser.globals.runId}`;
    // Feature #6: the org-scoped APIs validate the org header against the
    // organizations table, so the org ids must exist first.
    api.ensureOrg(browser, browser.globals.orgA);
    api.ensureOrg(browser, browser.globals.orgB);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC1: create returns a one-time sk- plaintext key and a key id': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org,
      body: {name: `e2e-key-${browser.globals.runId}`}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create key');
      browser.assert.ok(
        PLAINTEXT_RE.test(body.apiKey),
        `AC1: apiKey matches ^sk-[A-Za-z0-9]{43}$ (got ${String(body.apiKey).slice(0, 8)}...)`
      );
      browser.assert.ok(Boolean(body.keyId), 'AC1: keyId returned');
    });

    // A second create must never return the same plaintext (AC1).
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org,
      body: {name: `e2e-key2-${browser.globals.runId}`}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create second key');
      browser.assert.ok(
        PLAINTEXT_RE.test(body.apiKey),
        'AC1: second apiKey also matches the sk- format'
      );
    });
  },

  'AC3: list shows masked prefix, never the plaintext, with correct page meta': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org,
      body: {name: `e2e-list-${browser.globals.runId}`}
    }, (res) => {
      const created = api.assertOk(browser, res, 'create key for list');
      const plaintext = created.apiKey;
      const keyId = created.keyId;

      api.request(browser, {
        path: '/api/v1/admin/auth/api-keys?page.offset=0&page.limit=20',
        org
      }, (res2) => {
        const body = api.assertOk(browser, res2, 'list keys');
        const keys = body.keys || [];
        // The org is fresh for this run, so exactly the keys created by
        // this test case are visible (one create here; AC1's org is the
        // same id, so account for its two creates as well).
        const expected = 3;
        browser.assert.equal(
          body.pageMeta.total,
          String(expected),
          'AC3: pageMeta.total counts the org keys'
        );
        browser.assert.equal(body.pageMeta.offset, '0', 'AC3: pageMeta.offset echoed');
        browser.assert.equal(body.pageMeta.limit, 20, 'AC3: pageMeta.limit echoed');

        const row = keys.find((k) => k.keyId === keyId);
        browser.assert.ok(row, 'AC3: created key present in list');
        browser.assert.ok(
          row && typeof row.prefix === 'string' && row.prefix.length > 0 && row.prefix.length <= 8,
          `AC3: prefix masked (<= 8 chars, got "${row && row.prefix}")`
        );
        browser.assert.equal(row && row.revoked, false, 'AC3: fresh key not revoked');
        // AC10: no management API other than create returns the plaintext.
        const serialized = JSON.stringify(body);
        browser.assert.ok(
          serialized.indexOf(plaintext) === -1,
          'AC10: list response never contains the plaintext key'
        );
      });
    });
  },

  'AC6: revoke is idempotent and revoked keys stay auditable; activeOnly hides them': function (browser) {
    const org = browser.globals.orgA;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org,
      body: {name: `e2e-revoke-${browser.globals.runId}`}
    }, (res) => {
      const created = api.assertOk(browser, res, 'create key to revoke');
      const keyId = created.keyId;

      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/auth/api-keys/${keyId}:revoke`,
        org,
        body: {}
      }, (res2) => {
        api.assertOk(browser, res2, 'first revoke');

        // Idempotent second revoke (AC6).
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/auth/api-keys/${keyId}:revoke`,
          org,
          body: {}
        }, (res3) => {
          api.assertOk(browser, res3, 'second revoke is idempotent');
        });

        // Default list keeps the revoked row for audit (FR2.3) with
        // revoked=true and revoked_at set.
        api.request(browser, {
          path: '/api/v1/admin/auth/api-keys?page.offset=0&page.limit=20',
          org
        }, (res4) => {
          const body = api.assertOk(browser, res4, 'list after revoke');
          const row = (body.keys || []).find((k) => k.keyId === keyId);
          browser.assert.ok(row, 'FR2.3: revoked key remains visible for audit');
          browser.assert.equal(row && row.revoked, true, 'revoked flag set');
          browser.assert.ok(
            row && Number(row.revokedAt) > 0,
            'revokedAt recorded (unix seconds)'
          );
        });

        // activeOnly filters the revoked key out server-side.
        api.request(browser, {
          path: '/api/v1/admin/auth/api-keys?page.offset=0&page.limit=20&activeOnly=true',
          org
        }, (res5) => {
          const body = api.assertOk(browser, res5, 'list activeOnly');
          const stillThere = (body.keys || []).some((k) => k.keyId === keyId);
          browser.assert.equal(stillThere, false, 'activeOnly excludes revoked keys');
        });
      });
    });
  },

  'AC6: revoking a key of another organization returns not-found (no cross-org leak)': function (browser) {
    const orgA = browser.globals.orgA;
    const orgB = browser.globals.orgB;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org: orgA,
      body: {name: `e2e-xorg-${browser.globals.runId}`}
    }, (res) => {
      const created = api.assertOk(browser, res, 'create key in org A');
      const keyId = created.keyId;

      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/auth/api-keys/${keyId}:revoke`,
        org: orgB,
        body: {}
      }, (res2) => {
        // 10007 API_KEY_NOT_FOUND — ownership-checked, no existence leak.
        api.assertBusinessError(browser, res2, 10007, 'cross-org revoke');
      });

      // The key is still intact in its owning org.
      api.request(browser, {
        path: '/api/v1/admin/auth/api-keys?page.offset=0&page.limit=20',
        org: orgA
      }, (res3) => {
        const body = api.assertOk(browser, res3, 'list org A after cross-org revoke attempt');
        const row = (body.keys || []).find((k) => k.keyId === keyId);
        browser.assert.ok(row, 'key still listed in owning org');
        browser.assert.equal(row && row.revoked, false, 'key not revoked by foreign org');
      });

      // Org B's list never shows org A's keys.
      api.request(browser, {
        path: '/api/v1/admin/auth/api-keys?page.offset=0&page.limit=20',
        org: orgB
      }, (res4) => {
        const body = api.assertOk(browser, res4, 'list org B');
        const leaked = (body.keys || []).some((k) => k.keyId === keyId);
        browser.assert.equal(leaked, false, 'org B list does not leak org A keys');
      });
    });
  },

  'FR1.4: duplicate key names are allowed (keys identified by key_id)': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-dup-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org,
      body: {name}
    }, (res) => {
      const first = api.assertOk(browser, res, 'first create with name');

      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/auth/api-keys',
        org,
        body: {name}
      }, (res2) => {
        const second = api.assertOk(browser, res2, 'second create with same name');
        browser.assert.notEqual(second.keyId, first.keyId, 'duplicate name yields a distinct keyId');
      });
    });
  },

  'FR1.5: expires_at in the past is rejected; future expiry is accepted': function (browser) {
    const org = browser.globals.orgA;
    const past = Math.floor(Date.now() / 1000) - 3600;
    const future = Math.floor(Date.now() / 1000) + 86400;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org,
      body: {name: `e2e-past-${browser.globals.runId}`, expiresAt: String(past)}
    }, (res) => {
      // 10008 API_KEY_INVALID with a clear message.
      api.assertBusinessError(browser, res, 10008, 'past expires_at rejected');
    });

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org,
      body: {name: `e2e-future-${browser.globals.runId}`, expiresAt: String(future)}
    }, (res) => {
      const body = api.assertOk(browser, res, 'future expires_at accepted');
      browser.assert.ok(PLAINTEXT_RE.test(body.apiKey), 'future-expiry key created');
    });
  },

  'validation: empty name is rejected; missing org header is unauthorized': function (browser) {
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org: browser.globals.orgA,
      body: {name: ''}
    }, (res) => {
      api.assertBusinessError(browser, res, 10008, 'empty name rejected');
    });

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      body: {name: 'no-org-header'}
    }, (res) => {
      // 10001 UNAUTHORIZED: the transitional org header is required.
      api.assertBusinessError(browser, res, 10001, 'missing X-Organization-Id');
    });
  }
};
