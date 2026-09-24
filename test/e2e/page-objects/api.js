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

/**
 * Control-gateway API helper.
 *
 * Every request is issued from inside the browser page via `fetch`, so the
 * suites exercise the same browser HTTP stack (CORS preflight, header
 * handling, JSON parsing) a console SPA would use.
 *
 * Business-error contract (see docs/design/*.md): grpc-gateway renders
 * business errors as `{code, message}` in the body; the HTTP status may be
 * 500 for out-of-range codes, so assertions must branch on the body `code`,
 * never on the HTTP status.
 */
const api = {
  /**
   * Absolute URL for a gateway path. `browser` is the Nightwatch client
   * (its `globals.baseUrl` carries the configured gateway base URL).
   */
  url(browser, path) {
    const nightwatch = browser && browser.globals ? browser : browser.nightwatch;
    return nightwatch.globals.baseUrl + path;
  },

  /**
   * Perform a fetch from the browser and hand the parsed envelope to `verify`.
   * `verify` receives (response) where response = {status, body, text} and
   * must return true on success; assertion failures are reported by the
   * callback itself via browser.assert.
   */
  request(browser, {method = 'GET', path, body, org, headers = {}}, verify) {
    const url = this.url(browser, path);
    const payload = {
      url,
      options: {
        method,
        headers: Object.assign(
          org ? {'X-Organization-Id': org} : {},
          body !== undefined ? {'Content-Type': 'application/json'} : {},
          headers
        ),
        body: body !== undefined ? JSON.stringify(body) : undefined
      }
    };

    browser
      .timeoutsAsyncScript(15000)
      .executeAsync(function ({url, options}, done) {
        fetch(url, options)
          .then(async (res) => {
            const text = await res.text();
            let body = null;
            try {
              body = JSON.parse(text);
            } catch (e) {
              body = null;
            }
            done({status: res.status, body, text});
          })
          .catch((err) => done({status: 0, body: null, text: String(err)}));
      }, [payload], (result) => {
        const value = result && result.value !== undefined ? result.value : result;
        if (value && value.status !== 0 && value.body !== null) {
          verify(value);
        } else {
          browser.assert.fail(`request to ${url} failed: ${value && value.text}`);
        }
      });

    return this;
  },

  /** Assert the unified success envelope: response.code === 0. */
  assertOk(browser, res, label) {
    browser.assert.equal(res.status, 200, `${label}: HTTP 200`);
    browser.assert.equal(
      res.body && res.body.response ? res.body.response.code : undefined,
      0,
      `${label}: envelope response.code === 0`
    );
    return res.body;
  },

  /** Assert a business error body code (HTTP status is not meaningful). */
  assertBusinessError(browser, res, expectedCode, label) {
    browser.assert.ok(
      res.body && typeof res.body.code === 'number',
      `${label}: body carries numeric business code (got: ${res.text})`
    );
    browser.assert.equal(res.body.code, expectedCode, `${label}: business code ${expectedCode}`);
    return res.body;
  },

  /**
   * Ensure an organization exists (feature #6). The org-scoped APIs
   * validate the X-Organization-Id header against the organizations
   * table, so every suite must create its org ids before use. Creating
   * an existing org returns 10015, which is tolerated (idempotent).
   */
  ensureOrg(browser, orgId) {
    this.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/tenancy/organizations',
      org: orgId,
      body: {organizationId: orgId, displayName: `e2e org ${orgId}`}
    }, (res) => {
      if (res.body && res.body.code === 10015) {
        // Already exists: fine.
        return;
      }
      this.assertOk(browser, res, `ensureOrg ${orgId}`);
    });
    return this;
  },

  /**
   * Create an SSO provider (feature #7). Tolerates 10020 (already
   * exists) for idempotency across runs.
   */
  ensureSSOProvider(browser, providerId, provider) {
    this.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/sso/providers',
      org: 'org-default',
      body: {provider: Object.assign({providerId}, provider)}
    }, (res) => {
      if (res.body && res.body.code === 10020) {
        // Already exists: fine.
        return;
      }
      this.assertOk(browser, res, `ensureSSOProvider ${providerId}`);
    });
    return this;
  },

  /**
   * Enable an SSO provider (feature #7).
   */
  enableSSOProvider(browser, providerId) {
    this.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/auth/sso/providers/${providerId}:enable`,
      org: 'org-default'
    }, (res) => {
      this.assertOk(browser, res, `enableSSOProvider ${providerId}`);
    });
    return this;
  },

  /**
   * Perform an SSO login via the callback and store the session token
   * in localStorage (feature #7). The fake IdP is an in-process HTTP
   * server that returns a fixed ID token.
   */
  ssoLogin(browser, providerId) {
    // The callback exchanges the code at the fake IdP's token endpoint.
    // The fake IdP is configured in the compose stack; here we call the
    // callback with a valid state and code.
    this.request(browser, {
      method: 'GET',
      path: `/api/v1/auth/sso/${providerId}/callback?code=e2e-code&state=e2e-state`,
      org: ''
    }, (res) => {
      const body = this.assertOk(browser, res, `ssoLogin ${providerId}`);
      browser.assert.ok(Boolean(body.sessionToken), 'ssoLogin: session token returned');
      // Store the session token in localStorage.
      browser.execute(function (token) {
        localStorage.setItem('go-taas.session-token', token);
      }, [body.sessionToken]);
    });
    return this;
  }
};

module.exports = api;
