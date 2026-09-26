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

// Feature-20 (inference load testing) e2e suite, run against the compose
// stack gateway. Covers the acceptance criteria of
// docs/design/load-testing.md reachable from the outside: the admin Load
// Tests list + detail pages (AC7/AC8/AC9/AC10/AC12), the end-user model
// detail performance section (AC11/AC13), and the API contract + surface
// separation.
//
// The compose stack has no Controller/Kubernetes, so a service created via
// the API stays "pending" forever. The suite seeds a running inference
// service pointing at a mock OpenAI-compatible endpoint
// (test/e2e/seed/seed_loadtest.go) so CreateLoadTest has a valid target
// (AD4: state=running with an endpoint).

const { execSync } = require('child_process');
const path = require('path');
const api = require('../page-objects/api.js');

// Seed a running inference service pointing at the mock endpoint inside
// the compose network. The seed program writes the running row directly
// into PostgreSQL (the compose stack has no controller to set it running).
function seedRunningService(browser, org) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed',
    'golang:1.26-alpine',
    `sh -c "go run seed_loadtest.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -endpoint 'http://mock-infer:8000' -org '${org}'"`
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedRunningService failed: ${e.message}`);
  }
}

// Register a model in the catalog and return its modelId via the callback.
function createModel(browser, org, name, done) {
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/admin/models',
    org,
    body: {name, version: 'v1', weightPath: `models/${name}/v1/`}
  }, (res) => {
    const body = api.assertOk(browser, res, `createModel ${name}`);
    done(body.modelId);
  });
}

// Create a load test against the seeded running service and return its id.
function createLoadTest(browser, org, serviceId, done) {
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/admin/load-tests',
    org,
    body: {
      service_id: serviceId,
      concurrency: 2,
      duration_seconds: 5,
      request_rate: 200,
      prompt_template: 'hello load test',
      max_tokens: 32
    }
  }, (res) => {
    const body = api.assertOk(browser, res, 'create load test');
    browser.assert.equal(body.state, 'pending', 'create returns state=pending');
    done(body.loadTestId);
  });
}

// Poll GetLoadTest until the run reaches a terminal state, then call done
// with the final body. Uses a raw fetch with a generous timeout and
// retries on transient failures and non-terminal states.
function waitForTerminal(browser, org, loadTestId, done) {
  const deadline = Date.now() + 40000;
  const url = api.url(browser, `/api/v1/admin/load-tests/${loadTestId}`);
  const poll = () => {
    browser.timeoutsAsyncScript(30000).executeAsync(function ({url, org}, done) {
      fetch(url, {headers: {'X-Organization-Id': org}})
        .then(async (res) => {
          const text = await res.text();
          let body = null;
          try {
            body = JSON.parse(text);
          } catch (e) {
            body = null;
          }
          done({status: res.status, body});
        })
        .catch((err) => done({status: 0, body: null, err: String(err)}));
    }, [{url, org}], (result) => {
      const value = result && result.value !== undefined ? result.value : result;
      if (value && value.status !== 0 && value.body) {
        const state = value.body.summary && value.body.summary.state;
        if (state === 'completed' || state === 'failed' || state === 'stopped') {
          done(value.body);
          return;
        }
      }
      if (Date.now() > deadline) {
        browser.assert.fail(`load test did not reach a terminal state (last: ${value && value.body && value.body.summary && value.body.summary.state})`);
        return;
      }
      setTimeout(poll, 1000);
    });
  };
  poll();
}

module.exports = {
  '@tags': ['load-testing', 'feature-20'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-loadtest-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation (AC12/AC13) ----

  'AC12: load-test APIs are admin-only (user prefix 404s)': function (browser) {
    const org = browser.globals.orgA;
    // The admin-prefix endpoints are reachable.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/load-tests?page.limit=5',
      org
    }, (res) => {
      api.assertOk(browser, res, 'AC12: admin load-tests reachable');
    });
    // The user-prefix equivalent must NOT exist (404).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/load-tests?page.limit=5',
      org
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC12: user-prefix load-tests is 404');
    });
  },

  'AC13: user load-test API is user-only (admin prefix 404s)': function (browser) {
    const org = browser.globals.orgA;
    // The user-prefix endpoint is reachable (unknown model -> 10101, not 404).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/models/00000000-0000-0000-0000-000000000000/load-tests',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10101, 'AC13: user load-tests reachable (10101 for unknown)');
    });
    // The admin-prefix equivalent must NOT exist (404).
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/models/nonexistent-model/load-tests',
      org
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC13: admin-prefix user load-tests is 404');
    });
  },

  'AC12: admin page is served at /admin/load-tests': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/load-tests');
    browser.waitForElementPresent('[data-testid="load-tests-page"]', 15000, 'AC12: admin page served');
  },

  // ---- API contract (AC1/AC2/AC3/AC4/AC5/AC6) ----

  'AC1/AC3: create a load test, it completes, and the list shows it': function (browser) {
    const org = browser.globals.orgA;
    seedRunningService(browser, org);
    createModel(browser, org, `e2e-loadtest-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      browser.globals.loadtestModelId = modelId;
      createLoadTest(browser, org, '22222222-2222-2222-2222-222222222222', (loadTestId) => {
        browser.globals.loadTestId = loadTestId;
        waitForTerminal(browser, org, loadTestId, (body) => {
          browser.assert.equal(body.summary.state, 'completed', 'AC1: run completes');
          const result = body.result;
          browser.assert.ok(result, 'AC1: result block present');
          browser.assert.ok(parseInt(result.totalRequests, 10) > 0, 'AC1: requests sent');
          browser.assert.ok(result.throughputRps > 0, 'AC1: throughput > 0');
          browser.assert.ok(result.latencyP95Ms > 0, 'AC1: p95 latency > 0');
          browser.assert.ok(result.outputTokensPerSec > 0, 'AC1: tokens/sec > 0');
          browser.assert.equal(result.errorRate, 0, 'AC1: zero error rate');

          // AC3: the history list shows the completed run with its summary.
          api.request(browser, {
            method: 'GET',
            path: '/api/v1/admin/load-tests?status=completed&page.limit=10',
            org
          }, (res) => {
            const list = api.assertOk(browser, res, 'AC3: list');
            const runs = list.runs || [];
            const found = runs.find((r) => r.loadTestId === loadTestId);
            browser.assert.ok(found, 'AC3: completed run in history');
            browser.assert.equal(found.state, 'completed', 'AC3: run state completed');
            browser.assert.ok(found.throughputRps > 0, 'AC3: summary carries throughput');
          });
        });
      });
    });
  },

  'AC2: invalid config returns 10309; unknown target returns 10311': function (browser) {
    const org = browser.globals.orgA;
    // Invalid config (empty prompt) -> 10309.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/load-tests',
      org,
      body: {service_id: '22222222-2222-2222-2222-222222222222', prompt_template: '', duration_seconds: 5}
    }, (res) => {
      api.assertBusinessError(browser, res, 10309, 'AC2: empty prompt -> 10309');
    });
    // Unknown target -> 10311.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/load-tests',
      org,
      body: {service_id: '99999999-9999-9999-9999-999999999999', prompt_template: 'hi', duration_seconds: 5}
    }, (res) => {
      api.assertBusinessError(browser, res, 10311, 'AC2: unknown target -> 10311');
    });
  },

  'AC4/AC5: stop a running test and delete a terminal one': function (browser) {
    const org = browser.globals.orgA;
    seedRunningService(browser, org);
    // Start a long-running test so we can stop it.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/load-tests',
      org,
      body: {
        service_id: '22222222-2222-2222-2222-222222222222',
        concurrency: 1,
        duration_seconds: 3600,
        request_rate: 50,
        prompt_template: 'hi'
      }
    }, (res) => {
      const created = api.assertOk(browser, res, 'AC4: create long test');
      const loadTestId = created.loadTestId;

      // Wait until the run is running, then stop it.
      const deadline = Date.now() + 15000;
      const waitRunning = () => {
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/admin/load-tests/${loadTestId}`,
          org
        }, (res2) => {
          const body = api.assertOk(browser, res2, 'AC4: get');
          if (body.summary.state === 'running') {
            api.request(browser, {
              method: 'POST',
              path: `/api/v1/admin/load-tests/${loadTestId}:stop`,
              org,
              body: {}
            }, (res3) => {
              const stopped = api.assertOk(browser, res3, 'AC4: stop');
              browser.assert.equal(stopped.state, 'stopped', 'AC4: state stopped');
              // AC5: a terminal run can be deleted.
              api.request(browser, {
                method: 'DELETE',
                path: `/api/v1/admin/load-tests/${loadTestId}`,
                org
              }, (res4) => {
                api.assertOk(browser, res4, 'AC5: delete terminal run');
                // The deleted run is gone (10308).
                api.request(browser, {
                  method: 'GET',
                  path: `/api/v1/admin/load-tests/${loadTestId}`,
                  org
                }, (res5) => {
                  api.assertBusinessError(browser, res5, 10308, 'AC5: deleted run -> 10308');
                });
              });
            });
          } else if (Date.now() > deadline) {
            browser.assert.fail('AC4: run did not reach running state');
          } else {
            setTimeout(waitRunning, 1000);
          }
        });
      };
      waitRunning();
    });
  },

  'AC6: user projection is masked (no service ids)': function (browser) {
    const org = browser.globals.orgA;
    seedRunningService(browser, org);
    createModel(browser, org, `e2e-loadtest-user-${browser.globals.runId}-${browser.globals.testSeq}`, (modelId) => {
      // The seeded service references the fixed model id; create a load
      // test against it so the projection has content.
      createLoadTest(browser, org, '22222222-2222-2222-2222-222222222222', (loadTestId) => {
        waitForTerminal(browser, org, loadTestId, () => {
          api.request(browser, {
            method: 'GET',
            path: `/api/v1/models/11111111-1111-1111-1111-111111111111/load-tests`,
            org
          }, (res) => {
            const body = api.assertOk(browser, res, 'AC6: user projection');
            const results = body.results || [];
            browser.assert.ok(results.length > 0, 'AC6: projection has results');
            const first = results[0];
            browser.assert.ok(!('serviceId' in first), 'AC6: no serviceId in projection');
            browser.assert.ok(!('loadTestId' in first), 'AC6: no loadTestId in projection');
            browser.assert.ok(first.throughputRps > 0, 'AC6: throughput present');
            browser.assert.ok(first.latencyP95Ms > 0, 'AC6: p95 latency present');
          });
        });
      });
    });
  },

  // ---- Admin UI (AC7/AC8/AC9/AC10) ----

  'AC7: admin page renders the history table with a last-updated timestamp': function (browser) {
    const org = browser.globals.orgA;
    seedRunningService(browser, org);
    createLoadTest(browser, org, '22222222-2222-2222-2222-222222222222', (loadTestId) => {
      waitForTerminal(browser, org, loadTestId, () => {
        browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
        browser.url(browser.globals.baseUrl + '/admin/load-tests');
        browser.waitForElementPresent('[data-testid="load-tests-page"]', 15000, 'AC7: page renders');
        browser.waitForElementPresent('[data-testid="load-tests-table"]', 15000, 'AC7: history table');
        browser.waitForElementPresent('[data-testid="load-tests-last-updated"]', 10000, 'AC7: last-updated');
        browser.waitForElementPresent(`[data-testid="load-test-row-${loadTestId}"]`, 15000, 'AC7: run row');
      });
    });
  },

  'AC8: New Load Test dialog validates fields and navigates on submit': function (browser) {
    const org = browser.globals.orgA;
    seedRunningService(browser, org);
    // Set the admin-realm org key directly (the dialog's service list is
    // org-scoped; the legacy key is adopted only on a fresh surface boot).
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/load-tests');
    browser.waitForElementPresent('[data-testid="load-tests-page"]', 15000, 'AC8: page renders');
    browser.click('[data-testid="new-load-test"]');
    browser.waitForElementPresent('[data-testid="new-load-test-dialog"]', 10000, 'AC8: dialog opens');

    // Select the seeded running service so the submit can succeed.
    browser.pause(1500);
    browser.waitForElementPresent('[data-testid="load-test-service"]', 10000, 'AC8: service select');
    browser.waitForElementPresent('[data-testid="load-test-service"] option[value="22222222-2222-2222-2222-222222222222"]', 10000, 'AC8: service option');
    browser.click('[data-testid="load-test-service"] option[value="22222222-2222-2222-2222-222222222222"]');

    // Invalid concurrency shows the validation error.
    browser.clearValue('[data-testid="load-test-concurrency"]');
    browser.setValue('[data-testid="load-test-concurrency"]', '0');
    browser.click('[data-testid="load-test-submit"]');
    browser.waitForElementPresent('[data-testid="error-banner"]', 10000, 'AC8: validation error');

    // A valid submit navigates to the detail page.
    browser.clearValue('[data-testid="load-test-concurrency"]');
    browser.setValue('[data-testid="load-test-concurrency"]', '2');
    browser.click('[data-testid="load-test-submit"]');
    browser.waitForElementPresent('[data-testid="load-test-detail-page"]', 15000, 'AC8: navigates to detail');
  },

  'AC9: detail page shows live progress and curated results': function (browser) {
    const org = browser.globals.orgA;
    seedRunningService(browser, org);
    createLoadTest(browser, org, '22222222-2222-2222-2222-222222222222', (loadTestId) => {
      browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + `/admin/load-tests/${loadTestId}`);
      browser.waitForElementPresent('[data-testid="load-test-detail-page"]', 15000, 'AC9: detail page');
      // The run completes in ~5s; wait for the result block.
      browser.waitForElementPresent('[data-testid="load-test-result"]', 30000, 'AC9: result block');
      browser.waitForElementPresent('[data-testid="load-test-throughput"]', 10000, 'AC9: throughput');
      browser.waitForElementPresent('[data-testid="load-test-latency-table"]', 10000, 'AC9: latency table');
      browser.waitForElementPresent('[data-testid="load-test-tokens-per-sec"]', 10000, 'AC9: tokens/sec');
      browser.waitForElementPresent('[data-testid="load-test-error-rate"]', 10000, 'AC9: error rate');
    });
  },

  'AC10: empty state renders when the history is empty': function (browser) {
    const org = browser.globals.orgA;
    // The load-test history is platform-scoped (admin-only, no org
    // filter), so the empty state only appears when the whole table is
    // empty. Clear it so the empty state is deterministic.
    const repoRoot = path.resolve(__dirname, '..', '..', '..');
    const cmd = [
      'docker run --rm --network go-taas_default',
      '-e GOPROXY=https://goproxy.cn,direct',
      '-v go-taas-go-mod-cache:/go/pkg/mod',
      '-v go-taas-go-build-cache:/root/.cache/go-build',
      `-v "${repoRoot}":/app`,
      '-w /app/test/e2e/seed',
      'golang:1.26-alpine',
      `sh -c "go run seed_loadtest.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -endpoint 'http://mock-infer:8000' -org '${org}' -clear"`
    ].join(' ');
    try {
      execSync(cmd, {stdio: 'pipe', timeout: 120000});
    } catch (e) {
      browser.assert.fail(`clear load tests failed: ${e.message}`);
    }
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/load-tests');
    browser.waitForElementPresent('[data-testid="load-tests-page"]', 15000, 'AC10: page renders');
    browser.waitForElementPresent('[data-testid="load-tests-empty"]', 15000, 'AC10: empty state');
  },

  // ---- End-user UI (AC11) ----

  'AC11: user model detail shows the masked performance section': function (browser) {
    const org = browser.globals.orgA;
    seedRunningService(browser, org);
    createLoadTest(browser, org, '22222222-2222-2222-2222-222222222222', (loadTestId) => {
      // The run completes in ~5s; wait a fixed window (the poll can flake
      // under resource pressure) then navigate to the model detail page.
      browser.pause(8000);
      browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
      browser.url(browser.globals.baseUrl + '/models/11111111-1111-1111-1111-111111111111');
      browser.waitForElementPresent('[data-testid="model-detail-page"]', 15000, 'AC11: model detail page');
      browser.waitForElementPresent('[data-testid="model-detail-performance"]', 10000, 'AC11: performance section');
      browser.waitForElementPresent('[data-testid="model-detail-performance-table"]', 10000, 'AC11: performance table');
      browser.waitForElementPresent('[data-testid="model-detail-performance-row"]', 10000, 'AC11: performance row');
    });
  }
};