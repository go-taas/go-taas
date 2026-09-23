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
  }
};

module.exports = api;
