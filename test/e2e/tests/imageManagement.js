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

// Feature-03 (image-management) e2e suite, run against the compose stack
// gateway. Covers the API-level acceptance criteria of
// docs/design/image-management.md (AC1-AC5, AC8, AC11). AC6/AC7 (warmup
// helper pods on real nodes) need a k8s cluster with the controller and
// are covered by the controller unit tests; in compose a triggered warmup
// legitimately stays `pending` because no controller consumes the event.

const api = require('../page-objects/api.js');

module.exports = {
  '@tags': ['image', 'feature-03'],

  before(browser) {
    // Nothing: each test navigates in beforeEach because afterEach ends
    // the session and a fresh page starts at about:blank, where a
    // cross-origin fetch to the gateway would fail (null origin).
  },

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.orgA = `org-e2e-${browser.globals.runId}`;
    // Feature #6: the org-scoped APIs validate the org header against the
    // organizations table, so the org id must exist first.
    api.ensureOrg(browser, browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  'AC1: register image succeeds; duplicate triple conflicts': function (browser) {
    const org = browser.globals.orgA;
    const name = `ghcr.io/e2e/img-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/images',
      org,
      body: {name, tag: 'v1', accelerator: 'nvidia', engine: 'vllm'}
    }, (res) => {
      const created = api.assertOk(browser, res, 'register image');
      browser.assert.ok(Boolean(created.imageId), 'AC1: imageId returned');

      // Same (name, tag, accelerator) again -> 10202 IMAGE_EXISTS.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/images',
        org,
        body: {name, tag: 'v1', accelerator: 'nvidia', engine: 'vllm'}
      }, (res2) => {
        api.assertBusinessError(browser, res2, 10202, 'duplicate triple conflicts');
      });

      // A different accelerator is a distinct image.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/images',
        org,
        body: {name, tag: 'v1', accelerator: 'metax', engine: 'vllm'}
      }, (res3) => {
        const other = api.assertOk(browser, res3, 'register metax variant');
        browser.assert.notEqual(
          other.imageId,
          created.imageId,
          'AC1: different accelerator is a distinct image'
        );
      });
    });
  },

  'AC2: list filters by accelerator and engine': function (browser) {
    const org = browser.globals.orgA;
    const name = `ghcr.io/e2e/list-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/images',
      org,
      body: {name, tag: 'v1', accelerator: 'iluvatar', engine: 'sglang'}
    }, () => {
      api.request(browser, {
        path: `/api/v1/admin/images?accelerator=iluvatar&engine=sglang`,
        org
      }, (res2) => {
        const body = api.assertOk(browser, res2, 'list filtered');
        const images = body.images || [];
        const hit = images.find((i) => i.name === name);
        browser.assert.ok(Boolean(hit), 'AC2: registered image found by filters');
        browser.assert.equal(hit.engine, 'sglang', 'AC2: engine echoed');
        browser.assert.equal(hit.inUseCount, '0', 'AC2: inUseCount is 0 for a fresh image');
      });

      // A non-matching accelerator filter excludes it.
      api.request(browser, {
        path: `/api/v1/admin/images?accelerator=metax&engine=sglang`,
        org
      }, (res3) => {
        const body = api.assertOk(browser, res3, 'list non-matching');
        const images = body.images || [];
        browser.assert.ok(
          !images.some((i) => i.name === name),
          'AC2: non-matching filter excludes the image'
        );
      });
    });
  },

  'AC3: detail returns the image with empty in-use services': function (browser) {
    const org = browser.globals.orgA;
    const name = `ghcr.io/e2e/detail-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/images',
      org,
      body: {name, tag: 'v1', accelerator: 'nvidia', engine: 'vllm', description: 'e2e detail'}
    }, (res) => {
      const created = api.assertOk(browser, res, 'register image');
      const imageId = created.imageId;

      api.request(browser, {
        path: `/api/v1/admin/images/${imageId}`,
        org
      }, (res2) => {
        const body = api.assertOk(browser, res2, 'get image');
        browser.assert.equal(body.image.name, name, 'AC3: name echoed');
        browser.assert.equal(body.image.description, 'e2e detail', 'AC3: description echoed');
        browser.assert.deepEqual(
          body.inUseServices || [],
          [],
          'AC3: inUseServices empty for a fresh image'
        );
      });
    });
  },

  'AC4: patch updates only the description': function (browser) {
    const org = browser.globals.orgA;
    const name = `ghcr.io/e2e/patch-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/images',
      org,
      body: {name, tag: 'v1', accelerator: 'nvidia', engine: 'vllm'}
    }, (res) => {
      const created = api.assertOk(browser, res, 'register image');
      const imageId = created.imageId;

      api.request(browser, {
        method: 'PATCH',
        path: `/api/v1/admin/images/${imageId}`,
        org,
        body: {description: 'updated by e2e'}
      }, () => {
        api.request(browser, {
          path: `/api/v1/admin/images/${imageId}`,
          org
        }, (res3) => {
          const body = api.assertOk(browser, res3, 'get after patch');
          browser.assert.equal(
            body.image.description,
            'updated by e2e',
            'AC4: description updated'
          );
          browser.assert.equal(body.image.tag, 'v1', 'AC4: tag unchanged');
        });
      });
    });
  },

  'AC5: delete a free image succeeds; unknown image is 10201': function (browser) {
    const org = browser.globals.orgA;
    const name = `ghcr.io/e2e/del-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/images',
      org,
      body: {name, tag: 'v1', accelerator: 'nvidia', engine: 'vllm'}
    }, (res) => {
      const created = api.assertOk(browser, res, 'register image');
      const imageId = created.imageId;

      api.request(browser, {
        method: 'DELETE',
        path: `/api/v1/admin/images/${imageId}`,
        org
      }, (res2) => {
        api.assertOk(browser, res2, 'delete image');

        // The deleted image is gone: 10201 IMAGE_NOT_FOUND.
        api.request(browser, {
          path: `/api/v1/admin/images/${imageId}`,
          org
        }, (res3) => {
          api.assertBusinessError(browser, res3, 10201, 'deleted image is gone');
        });
      });
    });

    // Unknown image on delete: 10201.
    api.request(browser, {
      method: 'DELETE',
      path: '/api/v1/admin/images/img-missing',
      org
    }, (res4) => {
      api.assertBusinessError(browser, res4, 10201, 'unknown image delete is 10201');
    });
  },

  'AC8: warmup trigger creates a pending task; active gate rejects a second': function (browser) {
    const org = browser.globals.orgA;
    const name = `ghcr.io/e2e/warm-${browser.globals.runId}`;

    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/images',
      org,
      body: {name, tag: 'v1', accelerator: 'nvidia', engine: 'vllm'}
    }, (res) => {
      const created = api.assertOk(browser, res, 'register image');
      const imageId = created.imageId;

      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/images/${imageId}:warmup`,
        org,
        body: {nodeSelector: {'taas.go-taas.github.io/accelerator': 'nvidia'}}
      }, (res2) => {
        const body = api.assertOk(browser, res2, 'trigger warmup');
        browser.assert.ok(Boolean(body.taskId), 'AC8: taskId returned');
        const taskId = body.taskId;

        // A second trigger while the first is active -> 10205.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/images/${imageId}:warmup`,
          org
        }, (res3) => {
          api.assertBusinessError(browser, res3, 10205, 'active warmup gate rejects');
        });

        // The task is visible in the history with state pending (no
        // controller runs in compose, so it never leaves pending).
        api.request(browser, {
          path: `/api/v1/admin/images/${imageId}/warmup-tasks`,
          org
        }, (res4) => {
          const body4 = api.assertOk(browser, res4, 'list warmup tasks');
          const tasks = body4.tasks || [];
          browser.assert.ok(
            tasks.some((t) => t.taskId === taskId),
            'AC8: task listed in history'
          );
        });

        // Task detail carries the node selector.
        api.request(browser, {
          path: `/api/v1/admin/warmup-tasks/${taskId}`,
          org
        }, (res5) => {
          const body5 = api.assertOk(browser, res5, 'get warmup task');
          browser.assert.equal(
            body5.task.state,
            'pending',
            'AC8: task stays pending without a controller'
          );
        });
      });
    });
  },

  'AC11: validation matrix rejects invalid fields with business codes': function (browser) {
    const org = browser.globals.orgA;
    const base = {name: `ghcr.io/e2e/val-${browser.globals.runId}`, tag: 'v1', accelerator: 'nvidia', engine: 'vllm'};

    const cases = [
      {label: 'name with colon', body: Object.assign({}, base, {name: 'ghcr.io/x:v1'}), code: 10207},
      {label: 'empty tag', body: Object.assign({}, base, {tag: ''}), code: 10207},
      {label: 'bad digest', body: Object.assign({}, base, {digest: 'sha256:zz'}), code: 10203},
      {label: 'bad accelerator', body: Object.assign({}, base, {accelerator: 'tpu'}), code: 10204},
      {label: 'empty engine', body: Object.assign({}, base, {engine: ''}), code: 10207}
    ];

    cases.forEach((tc) => {
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/images',
        org,
        body: tc.body
      }, (res) => {
        api.assertBusinessError(browser, res, tc.code, `AC11: ${tc.label} -> ${tc.code}`);
      });
    });

    // Nothing was written: the invalid names never appear in the catalog.
    api.request(browser, {
      path: `/api/v1/admin/images?engine=vllm`,
      org
    }, (res2) => {
      const body = api.assertOk(browser, res2, 'list after validation');
      const images = body.images || [];
      browser.assert.ok(
        !images.some((i) => i.name === 'ghcr.io/x:v1'),
        'AC11: invalid image not persisted'
      );
    });
  }
};
