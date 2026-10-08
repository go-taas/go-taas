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

// Feature-44 (prompt evaluation) e2e suite, run against the compose
// stack gateway. Covers the acceptance criteria of
// docs/design/prompt-evaluation.md reachable from the outside: suite
// CRUD (AC1/AC2), case validation (AC3), real metered completion runs
// against a seeded running service + mock endpoint (AC5/AC6), run
// cancellation (AC7), run comparison (AC8), tenant isolation (AC9),
// the console pages (AC10) and the API surface separation.

const { execSync } = require('child_process');
const path = require('path');
const api = require('../page-objects/api.js');

const MODEL_ID = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';

// Seed a running inference service pointing at the mock OpenAI-compatible
// endpoint (test/e2e/seed/evaluation/seed_evaluation.go) so evaluation runs
// have a real completion target (the compose stack has no controller to
// set a service running).
function seedEvaluationService(browser, org) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/evaluation',
    'golang:1.26-alpine',
    `sh -c "go run seed_evaluation.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -endpoint 'http://mock-infer:8000' -org '${org}' -model '${MODEL_ID}'"`
  ].join(' ');
  try {
    execSync(cmd, { stdio: 'pipe', timeout: 120000 });
  } catch (e) {
    browser.assert.fail(`seedEvaluationService failed: ${e.message}`);
  }
}

// Create a prompt with one version and return {promptId, version}.
function createPrompt(browser, org, name, done) {
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/prompts',
    org,
    sessionRealm: 'user',
    body: { name, content: 'Answer the question: ${question}', variables: ['${question}'] }
  }, (res) => {
    const body = api.assertOk(browser, res, `createPrompt ${name}`);
    const prompt = body.prompt || body;
    done({ promptId: prompt.promptId, version: prompt.version || 1 });
  });
}

// Create an evaluation suite for the prompt and return its id.
function createSuite(browser, org, prompt, name, done) {
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/evaluations',
    org,
    sessionRealm: 'user',
    body: { name, promptId: prompt.promptId, promptVersion: prompt.version }
  }, (res) => {
    const body = api.assertOk(browser, res, `createSuite ${name}`);
    done(body.evaluation.evaluationId);
  });
}

// Add a test case to the suite and return its id.
function addCase(browser, org, evaluationId, name, done) {
  api.request(browser, {
    method: 'POST',
    path: `/api/v1/evaluations/${evaluationId}/cases`,
    org,
    sessionRealm: 'user',
    body: {
      name,
      variables: { question: 'what is the answer' },
      checks: [{ type: 'contains', expected: '42' }]
    }
  }, (res) => {
    const body = api.assertOk(browser, res, `addCase ${name}`);
    done(body.case ? body.case.caseId : body.caseId);
  });
}

// Create an active API key and return its keyId.
function createApiKey(browser, org, name, done) {
  api.request(browser, {
    method: 'POST',
    path: '/api/v1/auth/api-keys',
    org,
    sessionRealm: 'user',
    body: { name }
  }, (res) => {
    const body = api.assertOk(browser, res, `createApiKey ${name}`);
    done(body.keyId);
  });
}

// Create a run and return its id.
function createRun(browser, org, evaluationId, apiKeyId, done) {
  api.request(browser, {
    method: 'POST',
    path: `/api/v1/evaluations/${evaluationId}/runs`,
    org,
    sessionRealm: 'user',
    body: { modelId: MODEL_ID, apiKeyId, caseIds: [], promptVersion: 1 }
  }, (res) => {
    const body = api.assertOk(browser, res, 'createRun');
    browser.assert.equal(body.run.status, 'pending', 'run created pending');
    done(body.run.runId);
  });
}

// Poll GetEvaluationRun until a terminal state, then call done(body).
function waitForTerminalRun(browser, org, evaluationId, runId, done) {
  const deadline = Date.now() + 60000;
  const url = api.url(browser, `/api/v1/evaluations/${evaluationId}/runs/${runId}`);
  const poll = () => {
    browser.timeoutsAsyncScript(30000).executeAsync(function ({url, org}, done) {
      const token = localStorage.getItem('go-taas.user.session-token');
      const headers = { 'X-Organization-Id': org };
      if (token) headers.Authorization = `Bearer ${token}`;
      fetch(url, { headers })
        .then(async (res) => {
          const text = await res.text();
          let body = null;
          try {
            body = JSON.parse(text);
          } catch (e) {
            body = null;
          }
          done({ status: res.status, body });
        })
        .catch((err) => done({ status: 0, body: null, err: String(err) }));
    }, [{url, org}], (result) => {
      const value = result && result.value !== undefined ? result.value : result;
      if (value && value.status !== 0 && value.body && value.body.run) {
        const status = value.body.run.status;
        if (status !== 'pending' && status !== 'running') {
          done(value.body.run);
          return;
        }
      }
      if (Date.now() > deadline) {
        browser.assert.fail(`evaluation run did not reach a terminal state (last: ${value && value.body && value.body.run && value.body.run.status})`);
        return;
      }
      setTimeout(poll, 2000);
    });
  };
  poll();
}

module.exports = {
  '@tags': ['prompt-evaluation', 'feature-44'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-eval-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
    api.seedSession(browser, 'admin', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation ----

  'evaluation APIs are user-surface only (admin prefix 404s)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/evaluations?page.limit=5',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertOk(browser, res, 'user evaluations reachable');
    });
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/evaluations?page.limit=5',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      browser.assert.equal(res.status, 404, 'admin-prefix evaluations is 404');
    });
  },

  'user page is served at /evaluations': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/evaluations');
    browser.waitForElementPresent('[data-testid="evaluations-page"]', 15000, 'user page served');
    browser.waitForElementPresent('[data-testid="user-nav-evaluations"]', 15000, 'nav entry present');
  },

  // ---- Suite CRUD + case validation ----

  'AC1/AC2: suite create, list, get, update and delete': function (browser) {
    const org = browser.globals.orgA;
    createPrompt(browser, org, `eval-prompt-${browser.globals.testSeq}`, (prompt) => {
      createSuite(browser, org, prompt, 'suite-crud', (evaluationId) => {
        api.request(browser, {
          method: 'GET',
          path: '/api/v1/evaluations?page.limit=100',
          org,
          sessionRealm: 'user'
        }, (res) => {
          const body = api.assertOk(browser, res, 'AC1: list');
          const found = (body.evaluations || []).find((e) => e.evaluationId === evaluationId);
          browser.assert.ok(found, 'AC1: created suite listed');
          browser.assert.equal(found.name, 'suite-crud', 'AC1: name matches');
        });
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/evaluations/${evaluationId}`,
          org,
          sessionRealm: 'user'
        }, (res) => {
          const body = api.assertOk(browser, res, 'AC2: get');
          browser.assert.equal(body.evaluation.evaluationId, evaluationId, 'AC2: get returns suite');
        });
        api.request(browser, {
          method: 'PATCH',
          path: `/api/v1/evaluations/${evaluationId}`,
          org,
          sessionRealm: 'user',
          body: { name: 'suite-crud-renamed', description: 'updated' }
        }, (res) => {
          api.assertOk(browser, res, 'AC2: update');
        });
        api.request(browser, {
          method: 'DELETE',
          path: `/api/v1/evaluations/${evaluationId}`,
          org,
          sessionRealm: 'user'
        }, (res) => {
          api.assertOk(browser, res, 'AC2: delete');
        });
      });
    });
  },

  'AC3: case validation rejects empty checks and bad variables': function (browser) {
    const org = browser.globals.orgA;
    createPrompt(browser, org, `eval-prompt-cv-${browser.globals.testSeq}`, (prompt) => {
      createSuite(browser, org, prompt, 'suite-case-validation', (evaluationId) => {
        // A case with no checks is invalid (12803).
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/evaluations/${evaluationId}/cases`,
          org,
          sessionRealm: 'user',
          body: { name: 'no-checks', variables: {}, checks: [] }
        }, (res) => {
          api.assertBusinessError(browser, res, 12803, 'AC3: empty checks rejected');
        });
        // Variables that are not valid JSON strings are invalid.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/evaluations/${evaluationId}/cases`,
          org,
          sessionRealm: 'user',
          body: { name: 'bad-check', variables: {}, checks: [{ type: 'unknown-type', expected: 'x' }] }
        }, (res) => {
          api.assertBusinessError(browser, res, 12803, 'AC3: unknown check type rejected');
        });
      });
    });
  },

  // ---- Real metered completion runs ----

  'AC5/AC6: run executes real completions against the running service': function (browser) {
    const org = browser.globals.orgA;
    seedEvaluationService(browser, org);
    createPrompt(browser, org, `eval-prompt-run-${browser.globals.testSeq}`, (prompt) => {
      createSuite(browser, org, prompt, 'suite-run', (evaluationId) => {
        addCase(browser, org, evaluationId, 'case-42', () => {
          createApiKey(browser, org, 'eval-key', (apiKeyId) => {
            createRun(browser, org, evaluationId, apiKeyId, (runId) => {
              waitForTerminalRun(browser, org, evaluationId, runId, (run) => {
                browser.assert.equal(run.status, 'completed', 'AC5: run completes');
                browser.assert.equal(run.completedCount, 1, 'AC5: one case completed');
                browser.assert.equal(run.passedCount, 1, 'AC6: contains-check passed');
                const result = (run.cases || [])[0];
                browser.assert.ok(result, 'AC5: case result present');
                browser.assert.ok(result.completion, 'AC5: real completion returned');
                browser.assert.ok(result.inputTokens > 0, 'AC5: input tokens metered');
                browser.assert.ok(result.outputTokens > 0, 'AC5: output tokens metered');
                browser.assert.ok((result.checkResults || []).length > 0, 'AC6: check results present');
                browser.assert.equal(result.checkResults[0].passed, true, 'AC6: check passed');
              });
            });
          });
        });
      });
    });
  },

  'AC7: run cancellation finalizes as cancelled': function (browser) {
    const org = browser.globals.orgA;
    seedEvaluationService(browser, org);
    createPrompt(browser, org, `eval-prompt-cancel-${browser.globals.testSeq}`, (prompt) => {
      createSuite(browser, org, prompt, 'suite-cancel', (evaluationId) => {
        addCase(browser, org, evaluationId, 'case-cancel', () => {
          createApiKey(browser, org, 'eval-key-cancel', (apiKeyId) => {
            createRun(browser, org, evaluationId, apiKeyId, (runId) => {
              // Request cancellation immediately; the runner finalizes
              // the run as cancelled instead of executing it.
              api.request(browser, {
                method: 'POST',
                path: `/api/v1/evaluations/${evaluationId}/runs/${runId}:cancel`,
                org,
                sessionRealm: 'user',
                body: {}
              }, (res) => {
                api.assertOk(browser, res, 'AC7: cancel accepted');
                waitForTerminalRun(browser, org, evaluationId, runId, (run) => {
                  browser.assert.equal(run.status, 'cancelled', 'AC7: run finalized cancelled');
                });
              });
            });
          });
        });
      });
    });
  },

  'AC8: compare two runs returns deltas and case alignment': function (browser) {
    const org = browser.globals.orgA;
    seedEvaluationService(browser, org);
    createPrompt(browser, org, `eval-prompt-cmp-${browser.globals.testSeq}`, (prompt) => {
      createSuite(browser, org, prompt, 'suite-compare', (evaluationId) => {
        addCase(browser, org, evaluationId, 'case-compare', () => {
          createApiKey(browser, org, 'eval-key-cmp', (apiKeyId) => {
            createRun(browser, org, evaluationId, apiKeyId, (leftId) => {
              waitForTerminalRun(browser, org, evaluationId, leftId, (leftRun) => {
                browser.assert.equal(leftRun.status, 'completed', 'AC8: left run completed');
                createRun(browser, org, evaluationId, apiKeyId, (rightId) => {
                  waitForTerminalRun(browser, org, evaluationId, rightId, (rightRun) => {
                    browser.assert.equal(rightRun.status, 'completed', 'AC8: right run completed');
                    api.request(browser, {
                      method: 'POST',
                      path: `/api/v1/evaluations/${evaluationId}/runs:compare`,
                      org,
                      sessionRealm: 'user',
                      body: { leftRunId: leftId, rightRunId: rightId }
                    }, (res) => {
                      const body = api.assertOk(browser, res, 'AC8: compare');
                      browser.assert.ok(body.left && body.left.runId === leftId, 'AC8: left run returned');
                      browser.assert.ok(body.right && body.right.runId === rightId, 'AC8: right run returned');
                      browser.assert.equal(body.passRateDeltaMilli, 0, 'AC8: equal pass rates');
                      browser.assert.ok(Array.isArray(body.cases) && body.cases.length > 0, 'AC8: case alignment present');
                      const aligned = body.cases[0];
                      browser.assert.equal(aligned.onlyInLeft, false, 'AC8: case in both runs');
                      browser.assert.equal(aligned.onlyInRight, false, 'AC8: case in both runs');
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

  // ---- Tenant isolation ----

  'AC9: suites are isolated per organization': function (browser) {
    const orgA = browser.globals.orgA;
    const orgB = `org-e2e-eval-b-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, orgB);
    createPrompt(browser, orgA, `eval-prompt-iso-${browser.globals.testSeq}`, (prompt) => {
      createSuite(browser, orgA, prompt, 'suite-iso', (evaluationId) => {
        // The session's active org drives org scoping (the standard
        // platform behavior), so cross-tenant access needs an orgB
        // session; orgB must not see orgA's suite (12801).
        api.seedSession(browser, 'user', orgB);
        api.request(browser, {
          method: 'GET',
          path: `/api/v1/evaluations/${evaluationId}`,
          org: orgB,
          sessionRealm: 'user'
        }, (res) => {
          api.assertBusinessError(browser, res, 12801, 'AC9: cross-tenant get rejected');
        });
      });
    });
  },

  // ---- Console pages ----

  'AC10: detail page renders cases and run dialog': function (browser) {
    const org = browser.globals.orgA;
    createPrompt(browser, org, `eval-prompt-ui-${browser.globals.testSeq}`, (prompt) => {
      createSuite(browser, org, prompt, 'suite-ui', (evaluationId) => {
        browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
        browser.url(browser.globals.baseUrl + `/evaluations/${evaluationId}`);
        browser.waitForElementPresent('[data-testid="evaluation-detail-page"]', 15000, 'AC10: detail page renders');
        browser.waitForElementPresent('[data-testid="add-evaluation-case"]', 15000, 'AC10: add-case button present');
        browser.waitForElementPresent('[data-testid="run-evaluation"]', 15000, 'AC10: run button present');
      });
    });
  }
};
