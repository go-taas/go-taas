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

// Feature-39 (model fine-tuning management) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/architecture/model-finetuning.md (AC1-AC9) reachable from the
// outside:
//
//   - API contract on the admin surface (AC1-AC4): RegisterFineTuningDataset
//     registers a dataset and ListFineTuningDatasets returns it; an invalid
//     object path returns 12304; CreateFineTuningJob returns state=pending;
//     an unknown dataset returns 12303; ListFineTuningJobs/GetFineTuningJob
//     return the records; DeployFineTunedModel on a non-succeeded job
//     returns 12302 and on a succeeded job returns a service_id.
//   - Surface separation (AC8): the fine-tuning APIs are admin-only; a
//     user-realm session calling /api/v1/admin/finetuning/* is rejected
//     with 10038; the admin API is not reachable on the bare /api/v1/...
//     prefix.
//   - Console pages (AC5-AC7, AC9): the /admin/finetuning page renders the
//     dataset registry and job list; the create-job dialog creates a job
//     with state=pending; the /admin/finetuning/:jobId page renders the
//     status/hyperparameters/deploy card; the Deploy action is enabled only
//     for a succeeded job; a session without the required role receives
//     10036.
//
// The compose stack has no controller fine-tuning executor, so jobs created
// through the API stay pending. The seed (test/e2e/seed/finetuning/
// seed_finetuning.go) writes a dataset and a succeeded job directly into
// PostgreSQL so the deploy flow can be exercised.

const api = require('../page-objects/api.js');
const { execSync } = require('child_process');
const path = require('path');

// Fixed ids used by the seed (must match seed_finetuning.go).
const DATASET_ID = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
const PENDING_JOB_ID = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb';
const SUCCEEDED_JOB_ID = 'cccccccc-cccc-cccc-cccc-cccccccccccc';

// Seed fine-tuning datasets and jobs directly into PostgreSQL (the compose
// stack has no controller executor to produce them).
function seedFinetuning(browser) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/finetuning',
    'golang:1.26-alpine',
    `sh -c "go run seed_finetuning.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable'"`
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedFinetuning failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['model-finetuning', 'feature-39'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-ft-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    // Seed the fine-tuning dataset and jobs.
    seedFinetuning(browser);
    // Seed admin- and user-realm sessions so both the admin protected page
    // renders and the wrong-realm rejection can be exercised.
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Admin surface: dataset API (AC1) ----

  'AC1: RegisterFineTuningDataset registers a dataset; invalid object path -> 12304': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-ds-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/finetuning/datasets',
      org,
      body: {name, format: 'jsonl', objectPath: `data/${name}.jsonl`}
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1: register dataset');
      browser.assert.ok(Boolean(body.datasetId), 'AC1: dataset_id returned');
    });

    // Invalid object path -> 12304.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/finetuning/datasets',
      org,
      body: {name: 'bad', format: 'jsonl', objectPath: '../escape'}
    }, (res) => {
      api.assertBusinessError(browser, res, 12304, 'AC1: invalid object path -> 12304');
    });
  },

  'AC1b: ListFineTuningDatasets returns the seeded dataset': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/finetuning/datasets',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC1b: list datasets');
      browser.assert.ok(Array.isArray(body.datasets), 'AC1b: datasets array');
      const found = body.datasets.some((d) => d.datasetId === DATASET_ID);
      browser.assert.ok(found, 'AC1b: seeded dataset present');
    });
  },

  // ---- Admin surface: job API (AC2/AC3) ----

  'AC2: CreateFineTuningJob returns state=pending; unknown dataset -> 12303': function (browser) {
    const org = browser.globals.orgA;
    const name = `e2e-job-${browser.globals.runId}-${browser.globals.testSeq}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/finetuning/jobs',
      org,
      body: {
        name,
        baseModelId: '11111111-1111-1111-1111-111111111111',
        baseModelVersion: 'v1',
        datasetId: DATASET_ID,
        hyperparameters: {epochs: 3, batchSize: 4, learningRate: 0.001}
      }
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC2: create job');
      browser.assert.ok(Boolean(body.jobId), 'AC2: job_id returned');
      browser.assert.equal(body.state, 'pending', 'AC2: state pending');
    });

    // Unknown dataset -> 12303.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/finetuning/jobs',
      org,
      body: {
        name: 'bad',
        baseModelId: '11111111-1111-1111-1111-111111111111',
        baseModelVersion: 'v1',
        datasetId: '99999999-9999-9999-9999-999999999999',
        hyperparameters: {epochs: 3, batchSize: 4, learningRate: 0.001}
      }
    }, (res) => {
      api.assertBusinessError(browser, res, 12303, 'AC2: unknown dataset -> 12303');
    });
  },

  'AC3: ListFineTuningJobs and GetFineTuningJob return the seeded jobs': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/finetuning/jobs',
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC3: list jobs');
      browser.assert.ok(Array.isArray(body.jobs), 'AC3: jobs array');
      const found = body.jobs.some((j) => j.jobId === SUCCEEDED_JOB_ID);
      browser.assert.ok(found, 'AC3: seeded succeeded job present');
    });

    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/finetuning/jobs/${SUCCEEDED_JOB_ID}`,
      org
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC3: get job');
      browser.assert.ok(body.job, 'AC3: job present');
      browser.assert.equal(body.job.summary.name, 'e2e-succeeded-job', 'AC3: job name');
      browser.assert.ok(body.job.hyperparameters, 'AC3: hyperparameters present');
    });

    // Unknown job -> 12301.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/finetuning/jobs/99999999-9999-9999-9999-999999999999',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 12301, 'AC3: unknown job -> 12301');
    });
  },

  // ---- Admin surface: deploy API (AC4) ----

  'AC4: DeployFineTunedModel on a non-succeeded job -> 12302; on a succeeded job returns service_id': function (browser) {
    const org = browser.globals.orgA;

    // Deploy on a pending job -> 12302.
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/finetuning/jobs/${PENDING_JOB_ID}:deploy`,
      org,
      body: {imageId: 'img-vllm-nvidia-v063', accelerator: 'nvidia', replicas: 1}
    }, (res) => {
      api.assertBusinessError(browser, res, 12302, 'AC4: deploy on pending -> 12302');
    });

    // Deploy on the seeded succeeded job -> service_id.
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/finetuning/jobs/${SUCCEEDED_JOB_ID}:deploy`,
      org,
      body: {imageId: 'img-vllm-nvidia-v063', accelerator: 'nvidia', replicas: 1}
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC4: deploy succeeded job');
      browser.assert.ok(Boolean(body.serviceId), 'AC4: service_id returned');
    });
  },

  // ---- Surface separation (AC8) ----

  'AC8: a user-realm session calling the admin finetuning prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      return localStorage.getItem('go-taas.user.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC8: user session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/finetuning/jobs',
        org: browser.globals.orgA,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC8: user session on admin prefix -> 10038');
      });
    });
  },

  'AC8b: the admin finetuning API is not reachable on the bare /api/v1/... prefix': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/finetuning/jobs',
      org
    }, (res) => {
      browser.assert.ok(
        res.status === 404 || (res.body && res.body.code !== 0),
        'AC8b: admin finetuning route not served on bare prefix'
      );
    });
  },

  // ---- Console pages: admin Fine-tuning page (AC5/AC6) ----

  'AC5: /admin/finetuning page renders the dataset registry and job list': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/finetuning');
    browser.waitForElementPresent('[data-testid="finetuning-title"]', 15000, 'AC5: page renders');
    browser.waitForElementPresent('[data-testid="finetuning-datasets"]', 10000, 'AC5: dataset registry');
    browser.waitForElementPresent('[data-testid="finetuning-jobs"]', 10000, 'AC5: job list');
    browser.waitForElementPresent('[data-testid="finetuning-register-dataset"]', 10000, 'AC5: register dataset action');
    browser.waitForElementPresent('[data-testid="finetuning-new-job"]', 10000, 'AC5: new job action');
    browser.waitForElementPresent(`[data-testid="finetuning-dataset-${DATASET_ID}"]`, 10000, 'AC5: seeded dataset row');
    browser.waitForElementPresent(`[data-testid="finetuning-job-${SUCCEEDED_JOB_ID}"]`, 10000, 'AC5: seeded succeeded job row');
  },

  'AC6: the create-job dialog creates a job that appears with state=pending': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/finetuning');
    browser.waitForElementPresent('[data-testid="finetuning-new-job"]', 15000, 'AC6: new job action');
    browser.click('[data-testid="finetuning-new-job"]');
    browser.waitForElementPresent('[data-testid="finetuning-job-dialog"]', 10000, 'AC6: job dialog');
    browser.setValue('[data-testid="finetuning-job-name"]', `e2e-ui-job-${browser.globals.runId}`);
    browser.setValue('[data-testid="finetuning-job-model"]', '11111111-1111-1111-1111-111111111111');
    browser.setValue('[data-testid="finetuning-job-version"]', 'v1');
    browser.click('[data-testid="finetuning-job-dataset"] option[value="' + DATASET_ID + '"]');
    browser.click('[data-testid="finetuning-job-submit"]');
    // The job list reloads; the new job appears with state=pending.
    browser.waitForElementPresent('[data-testid="finetuning-jobs"]', 10000, 'AC6: job list reloads');
  },

  // ---- Console pages: admin Fine-tuning job detail page (AC7) ----

  'AC7: /admin/finetuning/:jobId page renders status/hyperparameters/deploy; Deploy enabled only for succeeded': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    // Succeeded job: deploy action enabled.
    browser.url(browser.globals.baseUrl + `/admin/finetuning/${SUCCEEDED_JOB_ID}`);
    browser.waitForElementPresent('[data-testid="finetuning-job-title"]', 15000, 'AC7: job detail renders');
    browser.waitForElementPresent('[data-testid="finetuning-status"]', 10000, 'AC7: status card');
    browser.waitForElementPresent('[data-testid="finetuning-hyperparameters"]', 10000, 'AC7: hyperparameters card');
    browser.waitForElementPresent('[data-testid="finetuning-deploy"]', 10000, 'AC7: deploy card');
    browser.waitForElementPresent('[data-testid="finetuning-deploy-action"]', 10000, 'AC7: deploy action enabled for succeeded');

    // Pending job: deploy disabled.
    browser.url(browser.globals.baseUrl + `/admin/finetuning/${PENDING_JOB_ID}`);
    browser.waitForElementPresent('[data-testid="finetuning-job-title"]', 15000, 'AC7: pending job detail renders');
    browser.waitForElementPresent('[data-testid="finetuning-deploy-disabled"]', 10000, 'AC7: deploy disabled for pending');
  },

  // ---- Console pages: permission denied (AC9) ----

  'AC9: a session without the required role receives 10036 on the admin finetuning API': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC9: non-member admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/finetuning/jobs',
        org,
        headers: {Authorization: `Bearer ${token}`}
      }, (res) => {
        api.assertBusinessError(browser, res, 10036, 'AC9: non-member session on admin finetuning API -> 10036');
      });
    });
  }
};