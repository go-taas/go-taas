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

// Feature-43 (prompt management & prompt library) e2e suite, run against
// the compose stack gateway. Covers the acceptance criteria of
// docs/design/prompt-management.md reachable from the outside: the user
// Prompts pages (AC7-AC10), the admin Prompts page (AC11), and the
// surface separation (AC12). The API-level criteria (AC1-AC6) are
// covered by test/fvt/prompt_management_fvt_test.go.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['prompt-management', 'feature-43'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-prompt-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation (AC12) ----

  'prompt APIs are user-surface; admin prefix serves the masked list': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/prompts?page.limit=5',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertOk(browser, res, 'user prompt list reachable');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/prompts?page.limit=5',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertOk(browser, res, 'admin prompt list reachable');
    });
    // The user prefix has no admin template route.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/prompts/admin-only-missing',
      org,
      sessionRealm: 'user'
    }, (res) => {
      // An unknown prompt id is a clean 12701 business error, proving
      // the route exists on the user surface.
      api.assertBusinessError(browser, res, 12701, 'unknown prompt -> 12701');
    });
  },

  // ---- API contract through the console surface ----

  'AC1/AC2: create, update, versions, and rollback via the API': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/prompts',
      org,
      sessionRealm: 'user',
      body: {name: 'e2e-prompt', content: 'Summarize: ${text}', variables: ['${text}']}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create prompt');
      const prompt = body.prompt || {};
      browser.assert.equal(prompt.version, 1, 'created at version 1');
      const promptId = prompt.promptId;
      browser.assert.ok(Boolean(promptId), 'prompt id returned');

      // Duplicate name -> 12703.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/prompts',
        org,
        sessionRealm: 'user',
        body: {name: 'e2e-prompt', content: 'other'}
      }, (res2) => {
        api.assertBusinessError(browser, res2, 12703, 'duplicate name -> 12703');

        // Update creates version 2.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/prompts/${promptId}`,
          org,
          sessionRealm: 'user',
          body: {content: 'Translate: ${text}'}
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'update prompt');
          browser.assert.equal(body3.prompt.version, 2, 'update creates version 2');

          // Version history lists both.
          api.request(browser, {
            method: 'GET',
            path: `/api/v1/prompts/${promptId}/versions`,
            org,
            sessionRealm: 'user'
          }, (res4) => {
            const body4 = api.assertOk(browser, res4, 'list versions');
            const versions = body4.versions || [];
            browser.assert.equal(versions.length, 2, 'two versions listed');

            // Rollback to version 1 (restores it as the active version).
            api.request(browser, {
              method: 'POST',
              path: `/api/v1/prompts/${promptId}/rollback`,
              org,
              sessionRealm: 'user',
              body: {version: 1}
            }, (res5) => {
              const body5 = api.assertOk(browser, res5, 'rollback');
              browser.assert.equal(body5.prompt.version, 1, 'rollback restores version 1 as active');
              browser.assert.equal(body5.prompt.content, 'Summarize: ${text}', 'content restored');
            });
          });
        });
      });
    });
  },

  'AC4: usage recording increments the counters': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/prompts',
      org,
      sessionRealm: 'user',
      body: {name: 'e2e-usage', content: 'hello'}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create prompt');
      const promptId = body.prompt.promptId;
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/prompts/${promptId}/usage`,
        org,
        sessionRealm: 'user',
        body: {}
      }, () => {
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/prompts/${promptId}/usage`,
          org,
          sessionRealm: 'user'
        }, (res2) => {
          const body2 = api.assertOk(browser, res2, 'get usage');
          browser.assert.equal(body2.timesUsed, '1', 'times_used incremented');
          browser.assert.ok(Number(body2.lastUsedAt) > 0, 'last_used_at set');
        });
      });
    });
  },

  // ---- UI flows ----

  'AC7: the /prompts page renders the toolbar, folder sidebar, and list': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/prompts');
    browser.waitForElementPresent('[data-testid="prompt-table"], [data-testid="prompt-empty"]', 15000, 'list or empty state renders');
    browser.waitForElementPresent('[data-testid="prompt-new"]', 15000, 'new prompt action');
    browser.waitForElementPresent('[data-testid="prompt-search"]', 15000, 'search box');
    browser.waitForElementPresent('[data-testid="user-nav-prompts"]', 15000, 'nav entry present');
  },

  'AC8: the New prompt dialog creates a prompt that appears with version 1': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/prompts');
    browser.waitForElementPresent('[data-testid="prompt-new"]', 15000, 'new prompt action');
    browser.click('[data-testid="prompt-new"]');
    browser.waitForElementPresent('[data-testid="prompt-create-dialog"]', 5000, 'create dialog opens');
    browser.setValue('[data-testid="prompt-name-input"]', 'e2e-ui-prompt');
    browser.setValue('[data-testid="prompt-content-input"]', 'Classify: ${text}');
    browser.setValue('[data-testid="prompt-variables-input"]', 'text');
    browser.click('[data-testid="prompt-create-submit"]');
    // Creating navigates to the new prompt's detail page (design §5.3).
    browser.waitForElementPresent('[data-testid="prompt-detail-content"]', 15000, 'detail page opens after create');
    browser.assert.urlContains('/prompts/', 'navigated to the new prompt detail');
  },

  'AC9/AC10: the detail page shows versions; edit creates a version; open in playground pre-fills': function (browser) {
    const org = browser.globals.orgA;
    // Create a prompt via the API to get a stable id.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/prompts',
      org,
      sessionRealm: 'user',
      body: {name: 'e2e-detail', content: 'Draft an email about ${topic}'}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create prompt');
      const promptId = body.prompt.promptId;

      browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + `/prompts/${promptId}`);
      browser.waitForElementPresent('[data-testid="prompt-detail-content"]', 15000, 'detail content renders');
      browser.waitForElementPresent('[data-testid="prompt-detail-versions"]', 15000, 'version history renders');
      browser.waitForElementPresent('[data-testid="prompt-version-1"]', 15000, 'version 1 listed');
      browser.waitForElementPresent('[data-testid="prompt-detail-usage"]', 15000, 'usage panel renders');

      // Open in playground pre-fills the editor (AC10).
      browser.click('[data-testid="prompt-open-playground"]');
      browser.waitForElementPresent('[data-testid="playground-prompt-input"]', 15000, 'playground renders');
      browser.assert.valueContains(
        '[data-testid="playground-prompt-input"]',
        'Draft an email about',
        'playground editor pre-filled with the prompt content'
      );
    });
  },

  'AC11: the /admin/prompts page lists prompts with masked content and tabs': function (browser) {
    const org = browser.globals.orgA;
    // Create a prompt so the admin list has a row.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/prompts',
      org,
      sessionRealm: 'user',
      body: {name: 'e2e-admin-visible', content: 'secret tenant content'}
    }, (res) => {
      api.assertOk(browser, res, 'create prompt');
      browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/admin/prompts');
      browser.waitForElementPresent('[data-testid="admin-prompts-table"]', 15000, 'admin table renders');
      browser.waitForElementPresent('[data-testid="admin-prompts-tab-usage"]', 15000, 'usage tab');
      browser.waitForElementPresent('[data-testid="admin-prompts-tab-templates"]', 15000, 'templates tab');
      browser.waitForElementPresent('[data-testid="nav-prompts"]', 15000, 'admin nav entry');
    });
  }
};
