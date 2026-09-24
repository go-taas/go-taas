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

// Feature-06 (multi-tenancy) e2e suite, run against the compose stack
// gateway. Covers the API-level acceptance criteria of
// docs/design/multi-tenancy.md (AC1-AC11) plus the console pages
// (Organizations, Projects) and the upgraded org switcher.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['tenancy', 'feature-06'],

  before(browser) {
    // Nothing: each test navigates in beforeEach because afterEach ends
    // the session and a fresh page starts at about:blank, where a
    // cross-origin fetch to the gateway would fail (null origin).
  },

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.orgA = `org-e2e-${browser.globals.runId}`;
    browser.globals.orgB = `org-other-${browser.globals.runId}`;
    // The org-scoped APIs validate the org header against the
    // organizations table, so the org ids must exist first.
    api.ensureOrg(browser, browser.globals.orgA);
    api.ensureOrg(browser, browser.globals.orgB);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC1: create organization returns the summary with zero counts': function (browser) {
    const orgId = `org-create-${browser.globals.runId}`;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/tenancy/organizations',
      org: browser.globals.orgA,
      body: {organizationId: orgId, displayName: 'Create Org', description: 'desc'}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create org');
      browser.assert.equal(body.organization.organizationId, orgId, 'AC1: organizationId echoed');
      browser.assert.equal(body.organization.state, 'active', 'AC1: new org is active');
      browser.assert.equal(body.organization.apiKeyCount, '0', 'AC1: zero api keys');
      browser.assert.equal(body.organization.projectCount, '0', 'AC1: zero projects');
    });
  },

  'AC1: invalid and duplicate organization ids are rejected': function (browser) {
    // Invalid id → 10019.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/tenancy/organizations',
      org: browser.globals.orgA,
      body: {organizationId: 'BAD ID', displayName: 'Bad'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10019, 'AC1: invalid org id');
    });

    // Duplicate id → 10015.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/tenancy/organizations',
      org: browser.globals.orgA,
      body: {organizationId: browser.globals.orgA, displayName: 'Dup'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10015, 'AC1: duplicate org id');
    });
  },

  'AC2: list organizations returns the directory with counts': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/tenancy/organizations?page.limit=100',
      org: browser.globals.orgA
    }, (res) => {
      const body = api.assertOk(browser, res, 'list orgs');
      const orgs = body.organizations || [];
      const found = orgs.find((o) => o.organizationId === browser.globals.orgA);
      browser.assert.ok(Boolean(found), 'AC2: orgA present in directory');
      browser.assert.equal(found.state, 'active', 'AC2: orgA active');
      browser.assert.ok(Number(found.apiKeyCount) >= 0, 'AC2: apiKeyCount is a number-string');
    });
  },

  'AC3: get and update an organization': function (browser) {
    const orgId = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/tenancy/organizations/${orgId}`,
      org: orgId
    }, (res) => {
      const body = api.assertOk(browser, res, 'get org');
      browser.assert.equal(body.organization.organizationId, orgId, 'AC3: get returns the org');
    });

    api.request(browser, {
      method: 'PATCH',
      path: `/api/v1/admin/tenancy/organizations/${orgId}`,
      org: orgId,
      body: {displayName: 'Renamed Org', description: 'updated'}
    }, (res) => {
      const body = api.assertOk(browser, res, 'update org');
      browser.assert.equal(body.organization.displayName, 'Renamed Org', 'AC3: displayName updated');
    });

    // Unknown org → 10005.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/tenancy/organizations/org-missing',
      org: orgId
    }, (res) => {
      api.assertBusinessError(browser, res, 10005, 'AC3: unknown org');
    });
  },

  'AC4: disable and enable an organization are idempotent': function (browser) {
    const orgId = `org-toggle-${browser.globals.runId}`;
    api.ensureOrg(browser, orgId);

    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/tenancy/organizations/${orgId}:disable`,
      org: orgId
    }, (res) => {
      const body = api.assertOk(browser, res, 'disable org');
      browser.assert.equal(body.organization.state, 'disabled', 'AC4: org disabled');
    });

    // Idempotent.
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/tenancy/organizations/${orgId}:disable`,
      org: orgId
    }, (res) => {
      api.assertOk(browser, res, 'disable org again');
    });

    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/tenancy/organizations/${orgId}:enable`,
      org: orgId
    }, (res) => {
      const body = api.assertOk(browser, res, 'enable org');
      browser.assert.equal(body.organization.state, 'active', 'AC4: org re-enabled');
    });
  },

  'AC6: create and list projects under an organization': function (browser) {
    const orgId = browser.globals.orgA;
    const projectId = `proj-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/tenancy/projects',
      org: orgId,
      body: {organizationId: orgId, projectId, displayName: 'Core Platform'}
    }, (res) => {
      const body = api.assertOk(browser, res, 'create project');
      browser.assert.equal(body.project.projectId, projectId, 'AC6: projectId echoed');
      browser.assert.equal(body.project.organizationId, orgId, 'AC6: owning org echoed');
    });

    api.request(browser, {
      method: 'GET',
      path: `/api/v1/admin/tenancy/projects?organizationId=${orgId}`,
      org: orgId
    }, (res) => {
      const body = api.assertOk(browser, res, 'list projects');
      const projects = body.projects || [];
      const found = projects.find((p) => p.projectId === projectId);
      browser.assert.ok(Boolean(found), 'AC6: project present in directory');
    });
  },

  'AC6: duplicate project id is globally unique (10016)': function (browser) {
    const orgId = browser.globals.orgA;
    const projectId = `proj-dup-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/tenancy/projects',
      org: orgId,
      body: {organizationId: orgId, projectId, displayName: 'First'}
    }, (res) => {
      api.assertOk(browser, res, 'create first project');
    });

    // Same project id under a different org → 10016.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/tenancy/projects',
      org: browser.globals.orgB,
      body: {organizationId: browser.globals.orgB, projectId, displayName: 'Clash'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10016, 'AC6: duplicate project id');
    });
  },

  'AC8: a disabled organization cannot accrue new API keys (10017)': function (browser) {
    const orgId = `org-gated-${browser.globals.runId}`;
    api.ensureOrg(browser, orgId);

    // Disable the org.
    api.request(browser, {
      method: 'POST',
      path: `/api/v1/admin/tenancy/organizations/${orgId}:disable`,
      org: orgId
    }, (res) => {
      api.assertOk(browser, res, 'disable org');
    });

    // Creating an API key under the disabled org → 10017.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org: orgId,
      body: {name: 'blocked'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10017, 'AC8: disabled org blocks key creation');
    });

    // Reads still work under the disabled org.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/auth/api-keys',
      org: orgId
    }, (res) => {
      api.assertOk(browser, res, 'AC8: reads allowed under disabled org');
    });
  },

  'AC9: an unknown organization id is rejected on reads (10005)': function (browser) {
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/auth/api-keys',
      org: 'org-does-not-exist'
    }, (res) => {
      api.assertBusinessError(browser, res, 10005, 'AC9: unknown org on read');
    });
  },

  'AC10: cross-organization isolation is preserved': function (browser) {
    const orgA = browser.globals.orgA;
    const orgB = browser.globals.orgB;

    // Create a key under orgA.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/auth/api-keys',
      org: orgA,
      body: {name: `iso-key-${browser.globals.runId}`}
    }, (res) => {
      api.assertOk(browser, res, 'create key under orgA');
    });

    // orgB sees no orgA keys.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/auth/api-keys',
      org: orgB
    }, (res) => {
      const body = api.assertOk(browser, res, 'list keys under orgB');
      browser.assert.equal(body.pageMeta.total, '0', 'AC10: orgB sees no orgA keys');
    });
  },

  'AC14: organizations page renders the directory and create dialog': function (browser) {
    browser.url(browser.globals.baseUrl + '/admin/organizations');
    browser.waitForElementPresent('[data-testid="orgs-table"]', 10000, 'AC14: orgs table renders');
    browser.waitForElementPresent('[data-testid="create-org"]', 5000, 'AC14: create button renders');

    // The create dialog opens with the id/name/desc inputs.
    browser.click('[data-testid="create-org"]');
    browser.waitForElementPresent('[data-testid="create-org-dialog"]', 5000, 'AC14: create dialog opens');
    browser.waitForElementPresent('[data-testid="org-id-input"]', 5000, 'AC14: org id input');
    browser.waitForElementPresent('[data-testid="org-name-input"]', 5000, 'AC14: org name input');
    browser.waitForElementPresent('[data-testid="org-desc-input"]', 5000, 'AC14: org desc input');
  },

  'AC14: projects page renders the directory and org filter': function (browser) {
    browser.url(browser.globals.baseUrl + '/admin/projects');
    browser.waitForElementPresent('[data-testid="projects-table"]', 10000, 'AC14: projects table renders');
    browser.waitForElementPresent('[data-testid="create-project"]', 5000, 'AC14: create button renders');
    browser.waitForElementPresent('[data-testid="project-org-filter"]', 5000, 'AC14: org filter renders');
  },

  'AC14: org switcher lists organizations from the tenancy API': function (browser) {
    browser.url(browser.globals.baseUrl + '/admin/models');
    browser.waitForElementPresent('[data-testid="org-switcher-select"]', 10000, 'AC14: org switcher renders');
    browser.waitForElementPresent(
      `[data-testid="org-switcher-option-${browser.globals.orgA}"]`,
      10000,
      'AC14: orgA appears in the switcher'
    );
  }
};