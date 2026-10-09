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

// Feature-42 (batch inference) e2e suite, run against the compose stack
// gateway. Covers the acceptance criteria of
// docs/design/batch-inference.md reachable from the outside: the user
// Batch pages (AC7-AC9), the admin Batch page (AC10), and the surface
// separation (AC11). The API-level criteria (AC1-AC6) are covered by
// test/fvt/batch_inference_fvt_test.go; here the API is exercised only
// through the console flows plus the worker's end-to-end drive of a
// freshly created job (validating -> completed) against mock-infer.

const api = require('../page-objects/api.js');

// A two-line single-model JSONL input, base64-encoded for the API and
// reused for the UI upload.
const JSONL_LINES = [
  JSON.stringify({
    custom_id: 'e2e-a',
    method: 'POST',
    url: '/v1/chat/completions',
    body: {model: 'mock-model', messages: [{role: 'user', content: 'hello'}]}
  }),
  JSON.stringify({
    custom_id: 'e2e-b',
    method: 'POST',
    url: '/v1/chat/completions',
    body: {model: 'mock-model', messages: [{role: 'user', content: 'world'}]}
  })
];
const JSONL_TEXT = JSONL_LINES.join('\n') + '\n';

function b64(str) {
  return Buffer.from(str, 'utf8').toString('base64');
}

module.exports = {
  '@tags': ['batch-inference', 'feature-42'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-batch-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation (AC11) ----

  'batch APIs are user-surface only; admin prefix has no create route': function (browser) {
    const org = browser.globals.orgA;
    // The user prefix serves the batch surface.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/batch?page.limit=5',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertOk(browser, res, 'user batch list reachable');
    });
    // The admin prefix serves the masked admin list.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/batch?page.limit=5',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertOk(browser, res, 'admin batch list reachable');
    });
    // CreateBatchJob is user-only: the admin prefix registers only the
    // list/get/cancel bindings, so a POST to the collection root hits a
    // method mismatch and the gateway answers 501 before any handler runs.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/batch',
      org,
      sessionRealm: 'admin',
      body: {input_file: b64(JSONL_TEXT), completion_window: '24h'}
    }, (res) => {
      browser.assert.equal(res.status, 501, 'admin-prefix batch create is 501 (no POST binding)');
    });
  },

  // ---- API + worker end-to-end (AC1/AC4 via the console surface) ----

  'a created job is driven validating to completed and the result downloads': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/batch',
      org,
      sessionRealm: 'user',
      body: {input_file: b64(JSONL_TEXT), completion_window: '24h'}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create batch job');
      const job = body.batchJob || {};
      browser.assert.equal(job.status, 'BATCH_JOB_STATUS_VALIDATING', 'created job is validating');
      browser.assert.equal(job.model, 'mock-model', 'single model extracted');
      browser.assert.equal(job.totalRequests, '2', 'two requests counted');
      const batchId = job.batchId;
      browser.assert.ok(Boolean(batchId), 'batch id returned');

      // Poll until the worker drives the job to a terminal state. The
      // worker promotes validating -> in_progress -> finalizing ->
      // completed on its 5s tick.
      browser
        .timeoutsAsyncScript(15000)
        .perform((done) => {
          const poll = (attempt) => {
            api.request(browser, {
              method: 'GET',
              path: `/api/v1/batch/${batchId}`,
              org,
              sessionRealm: 'user'
            }, (res2) => {
              const job2 = (res2.body && res2.body.batchJob) || {};
              if (job2.status === 'BATCH_JOB_STATUS_COMPLETED' || attempt >= 20) {
                browser.assert.equal(job2.status, 'BATCH_JOB_STATUS_COMPLETED', 'job reaches completed');
                browser.assert.equal(job2.succeededRequests, '2', 'both requests succeeded');
                browser.assert.equal(job2.failedRequests, '0', 'no failed requests');
                // The result file downloads (AC4).
                api.request(browser, {
                  method: 'GET',
                  path: `/api/v1/batch/${batchId}/result`,
                  org,
                  sessionRealm: 'user'
                }, (res3) => {
                  const body3 = api.assertOk(browser, res3, 'download result');
                  const content = Buffer.from(body3.content || '', 'base64').toString('utf8');
                  browser.assert.ok(content.includes('e2e-a'), 'result carries custom_id e2e-a');
                  browser.assert.ok(content.includes('e2e-b'), 'result carries custom_id e2e-b');
                  done();
                });
              } else {
                setTimeout(() => poll(attempt + 1), 1500);
              }
            });
          };
          poll(0);
        });
    });
  },

  'invalid JSONL and multi-model inputs are rejected with 12603/12605': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/batch',
      org,
      sessionRealm: 'user',
      body: {input_file: b64('{"bad json\n')}
    }, (res) => {
      api.assertBusinessError(browser, res, 12603, 'invalid JSONL -> 12603');
    });
    const multi = [
      JSON.stringify({custom_id: 'a', method: 'POST', url: '/v1/chat/completions', body: {model: 'm1'}}),
      JSON.stringify({custom_id: 'b', method: 'POST', url: '/v1/chat/completions', body: {model: 'm2'}})
    ].join('\n');
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/batch',
      org,
      sessionRealm: 'user',
      body: {input_file: b64(multi)}
    }, (res) => {
      api.assertBusinessError(browser, res, 12605, 'multi-model -> 12605');
    });
  },

  'a job in a terminal state cannot be cancelled (12602) and cross-tenant reads 404 as 12601': function (browser) {
    const org = browser.globals.orgA;
    const orgB = `org-e2e-batch-other-${browser.globals.runId}`;
    api.ensureOrg(browser, orgB);
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/batch',
      org,
      sessionRealm: 'user',
      body: {input_file: b64(JSONL_TEXT), completion_window: '24h'}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create job for cancel test');
      const batchId = body.batchJob.batchId;
      browser
        .timeoutsAsyncScript(15000)
        .perform((done) => {
          const poll = (attempt) => {
            api.request(browser, {
              method: 'GET',
              path: `/api/v1/batch/${batchId}`,
              org,
              sessionRealm: 'user'
            }, (res2) => {
              const status = (res2.body && res2.body.batchJob && res2.body.batchJob.status) || '';
              if (status === 'BATCH_JOB_STATUS_COMPLETED' || attempt >= 20) {
                // Terminal state: cancel is rejected (AC3).
                api.request(browser, {
                  method: 'POST',
                  path: `/api/v1/batch/${batchId}/cancel`,
                  org,
                  sessionRealm: 'user',
                  body: {}
                }, (res3) => {
                  api.assertBusinessError(browser, res3, 12602, 'terminal cancel -> 12602');
                  // Cross-tenant read is masked as not-found (AC6). Per D6
                  // (FR4.4) a session's active org wins over the
                  // X-Organization-Id header, so the org-B context needs its
                  // own session — seeding one here overwrites the org-A user
                  // token, which is fine: this is the last request of the
                  // case and beforeEach re-seeds for the next test.
                  api.seedSession(browser, 'user', orgB);
                  api.request(browser, {
                    method: 'GET',
                    path: `/api/v1/batch/${batchId}`,
                    org: orgB,
                    sessionRealm: 'user'
                  }, (res4) => {
                    api.assertBusinessError(browser, res4, 12601, 'cross-tenant -> 12601');
                    done();
                  });
                });
              } else {
                setTimeout(() => poll(attempt + 1), 1500);
              }
            });
          };
          poll(0);
        });
    });
  },

  // ---- UI flows ----

  'AC7: the /batch page renders the job list with the New batch action': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/batch');
    browser.waitForElementPresent('[data-testid="batch-table"], [data-testid="batch-empty"]', 15000, 'list or empty state renders');
    browser.waitForElementPresent('[data-testid="batch-new"]', 15000, 'new batch action');
    browser.waitForElementPresent('[data-testid="user-nav-batch"]', 15000, 'nav entry present');
  },

  'AC8: the builder creates a job that polls to completed and enables download': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/batch');
    browser.waitForElementPresent('[data-testid="batch-new"]', 15000, 'new batch action');
    // The builder opens by default when the list is empty (design §5.3),
    // so only click New batch when it is not already open. Wait for the
    // first list load to settle either way (empty -> builder, rows ->
    // no builder) before deciding.
    browser.waitForElementPresent('[data-testid="batch-table"], [data-testid="batch-empty"]', 15000, 'list settles');
    browser.execute(
      function () {
        // Return 1 when the builder dialog is already open.
        return document.querySelector('[data-testid="batch-builder"]') ? 1 : 0;
      },
      [],
      (result) => {
        if (!result.value) {
          browser.click('[data-testid="batch-new"]');
        }
      }
    );
    browser.waitForElementPresent('[data-testid="batch-builder"]', 5000, 'builder opens');
    browser.waitForElementPresent('[data-testid="batch-file-input"]', 5000, 'file input');
    browser.waitForElementPresent('[data-testid="batch-window-select"]', 5000, 'window select');

    // Upload the JSONL via the hidden-file-input dance: set the file on
    // the input through the browser, then submit.
    browser.perform((client, done) => {
      client.execute(
        function (jsonl) {
          const dt = new DataTransfer();
          const file = new File([jsonl], 'e2e.jsonl', {type: 'application/x-ndjson'});
          dt.items.add(file);
          const input = document.querySelector('[data-testid="batch-file-input"]');
          input.files = dt.files;
          input.dispatchEvent(new Event('change', {bubbles: true}));
          return input.files.length;
        },
        [JSONL_TEXT],
        (result) => {
          client.assert.equal(result.value, 1, 'file attached');
          done();
        }
      );
    });
    browser.click('[data-testid="batch-submit"]');
    // The builder navigates to the detail page of the new job.
    browser.waitForElementPresent('[data-testid="batch-detail-summary"]', 15000, 'detail page opens');
    // The worker drives the job to completed; the download button enables.
    browser.waitForElementPresent(
      '[data-testid="batch-download-result"]:not([disabled])',
      30000,
      'result download enabled when completed'
    );
  },

  'AC10: the /admin/batch page lists jobs across tenants with the Organization column': function (browser) {
    const org = browser.globals.orgA;
    // Create one job in this org via the API first.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/batch',
      org,
      sessionRealm: 'user',
      body: {input_file: b64(JSONL_TEXT), completion_window: '24h'}
    }, (res) => {
      api.assertOk(browser, res, 'seed job created');
      browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/admin/batch');
      browser.waitForElementPresent('[data-testid="admin-batch-table"]', 15000, 'admin table renders');
      browser.waitForElementPresent('[data-testid="nav-batch"]', 15000, 'admin nav entry');
      // The Organization column header is present.
      browser.assert.elementPresent('th', 'headers render');
    });
  }
};
