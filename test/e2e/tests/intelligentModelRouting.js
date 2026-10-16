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

// Feature-49 (intelligent model routing) e2e suite, run against the
// compose stack gateway. Covers the acceptance criteria of
// docs/design/intelligent-model-routing.md reachable from the outside:
// the admin Routing Groups list page (AC1), the create dialog
// validation (AC2), the candidate editor (AC3), strategy configuration
// (AC4), the intent-rule editor (AC5), the dry-run test panel (AC6),
// the decisions page (AC12), the tenant-facing Auto marker (AC15), the
// guards and states (AC16), and the API surface separation (AC13/AC14).

const { execSync } = require('child_process');
const path = require('path');
const api = require('../page-objects/api.js');

const CAND_A = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
const CAND_B = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb';

// Unique group names per run so the persisted compose database never
// collides across runs.
function groupName(browser, base) {
  return `${base}-${browser.globals.runId}-${browser.globals.testSeq}`;
}

// Seed the routing-group candidate catalog models directly into
// PostgreSQL (the compose stack has no controller to register models).
function seedRoutingGroups(browser, org) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/routinggroups',
    'golang:1.26-alpine',
    `sh -c "go run seed_routinggroups.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -org '${org}'"`
  ].join(' ');
  try {
    execSync(cmd, { stdio: 'pipe', timeout: 120000 });
  } catch (e) {
    browser.assert.fail(`seedRoutingGroups failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['intelligent-model-routing', 'feature-49'],

  beforeEach(browser) {
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-rg-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Surface separation (AC13/AC14) ----

  'AC13: routing-group admin APIs are admin-only (user prefix 404s)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/routing-groups?page.limit=5',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertOk(browser, res, 'AC13: admin routing-groups reachable');
    });
    // The bare user prefix has no routing-group route: a user-realm
    // session passes the user-surface realm guard and gets the gateway
    // 404.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/routing-groups?page.limit=5',
      org,
      sessionRealm: 'user'
    }, (res) => {
      browser.assert.equal(res.status, 404, 'AC13: user-prefix routing-groups is 404');
    });
  },

  'AC13: a user session cannot read or mutate routing groups (10038)': function (browser) {
    const org = browser.globals.orgA;
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/routing-groups?page.limit=5',
      org,
      sessionRealm: 'user'
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC13: user realm rejected on admin list');
    });
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/routing-groups',
      org,
      sessionRealm: 'user',
      body: {name: 'auto-chat', strategy: 'weighted'}
    }, (res) => {
      api.assertBusinessError(browser, res, 10038, 'AC13: user realm rejected on admin create');
    });
  },

  'AC14: routing-group admin RPCs require a session (10027) and the admin role (10036)': function (browser) {
    const org = browser.globals.orgA;
    // Without any session the RoleGuard cannot resolve the caller.
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/routing-groups?page.limit=5',
      org
    }, (res) => {
      api.assertBusinessError(browser, res, 10027, 'AC14: no session rejected');
    });
    // A non-member admin-realm session is denied by the RoleGuard.
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, {noMember: true});
    api.request(browser, {
      method: 'GET',
      path: '/api/v1/admin/routing-groups?page.limit=5',
      org,
      sessionRealm: 'admin'
    }, (res) => {
      api.assertBusinessError(browser, res, 10036, 'AC14: non-member session denied');
    });
  },

  'AC13: the admin page is served at /admin/routing-groups and the user console has no routing route': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/admin/routing-groups');
    browser.waitForElementPresent('[data-testid="routing-groups-page"]', 15000, 'AC13: admin page served');
    browser.waitForElementPresent('[data-testid="nav-routing-groups"]', 15000, 'AC13: nav entry present');
    // The user console has no routing-group route: it renders not-found.
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
    browser.url(browser.globals.baseUrl + '/routing-groups');
    browser.waitForElementPresent('[data-testid="not-found-home"]', 15000, 'AC13: user console not-found page');
    browser.assert.not.elementPresent('[data-testid="routing-groups-page"]', 'AC13: routing page not on user console');
  },

  // ---- API contract (AC1/AC2/AC3/AC4/AC5/AC6) ----

  'AC1/AC2: create a disabled group, list it, and validate the name errors': function (browser) {
    const org = browser.globals.orgA;
    const gname = groupName(browser, 'auto-chat');
    seedRoutingGroups(browser, org);
    // Create a disabled weighted group.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/routing-groups',
      org,
      sessionRealm: 'admin',
      body: {name: gname, strategy: 'weighted'}
    }, (res) => {
      const body = api.assertOk(browser, res, 'AC2: create group');
      const group = body.group || {};
      browser.assert.equal(group.name, gname, 'AC2: name');
      browser.assert.equal(group.enabled, false, 'AC2: created disabled');
      browser.assert.equal(group.strategy, 'weighted', 'AC2: strategy');
      const groupId = group.groupId;
      browser.assert.ok(groupId, 'AC2: group id present');

      // Duplicate name -> 13102.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/routing-groups',
        org,
        sessionRealm: 'admin',
        body: {name: gname, strategy: 'weighted'}
      }, (res2) => {
        api.assertBusinessError(browser, res2, 13102, 'AC2: duplicate name -> 13102');
      });

      // Invalid name -> 13103.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/routing-groups',
        org,
        sessionRealm: 'admin',
        body: {name: 'Bad_Name', strategy: 'weighted'}
      }, (res3) => {
        api.assertBusinessError(browser, res3, 13103, 'AC2: invalid name -> 13103');
      });

      // Reserved real-model name -> 13113.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/routing-groups',
        org,
        sessionRealm: 'admin',
        body: {name: 'aaa-routing-cand-a', strategy: 'weighted'}
      }, (res4) => {
        api.assertBusinessError(browser, res4, 13113, 'AC2: reserved name -> 13113');
      });

      // Invalid strategy -> 13107.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/routing-groups',
        org,
        sessionRealm: 'admin',
        body: {name: groupName(browser, 'auto-chat-2'), strategy: 'round_robin'}
      }, (res5) => {
        api.assertBusinessError(browser, res5, 13107, 'AC2: invalid strategy -> 13107');
      });

      // List shows the group.
      api.request(browser, {
        method: 'GET',
        path: `/api/v1/admin/routing-groups?name=${gname}`,
        org,
        sessionRealm: 'admin'
      }, (res6) => {
        const list = api.assertOk(browser, res6, 'AC1: list');
        const groups = list.groups || [];
        const found = groups.find((g) => g.groupId === groupId);
        browser.assert.ok(found, 'AC1: group listed');
        browser.assert.equal(found.enabled, false, 'AC1: disabled');
        browser.assert.equal(found.candidateCount, '0', 'AC1: zero candidates');
      });
    });
  },

  'AC3/AC4: replace candidates, validate errors, and enable the group': function (browser) {
    const org = browser.globals.orgA;
    const gname = groupName(browser, 'auto-chat');
    seedRoutingGroups(browser, org);
    let groupId;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/routing-groups',
      org,
      sessionRealm: 'admin',
      body: {name: gname, strategy: 'weighted'}
    }, (res) => {
      groupId = api.assertOk(browser, res, 'AC3: create').group.groupId;

      // Replace candidates (ordered, weights, tiers).
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/routing-groups/${groupId}/candidates`,
        org,
        sessionRealm: 'admin',
        body: {
          groupId,
          candidates: [
            {modelId: CAND_A, weight: 70, costTier: 2, latencyTier: 1},
            {modelId: CAND_B, weight: 30, costTier: 1, latencyTier: 3}
          ]
        }
      }, (res2) => {
        const body = api.assertOk(browser, res2, 'AC3: replace candidates');
        const candidates = body.candidates || [];
        browser.assert.equal(candidates.length, 2, 'AC3: two candidates');
        browser.assert.equal(candidates[0].modelId, CAND_A, 'AC3: first candidate');
        browser.assert.equal(candidates[0].position, '0', 'AC3: position 0');

        // Duplicate candidate -> 13106.
        api.request(browser, {
          method: 'PUT',
          path: `/api/v1/admin/routing-groups/${groupId}/candidates`,
          org,
          sessionRealm: 'admin',
          body: {
            groupId,
            candidates: [
              {modelId: CAND_A, weight: 10},
              {modelId: CAND_A, weight: 20}
            ]
          }
        }, (res3) => {
          api.assertBusinessError(browser, res3, 13106, 'AC3: duplicate candidate -> 13106');
        });

        // Unknown model -> 13104.
        api.request(browser, {
          method: 'PUT',
          path: `/api/v1/admin/routing-groups/${groupId}/candidates`,
          org,
          sessionRealm: 'admin',
          body: {
            groupId,
            candidates: [{modelId: 'no-such-model', weight: 10}]
          }
        }, (res4) => {
          api.assertBusinessError(browser, res4, 13104, 'AC3: unknown candidate -> 13104');
        });

        // Enable with candidates succeeds (AC4).
        api.request(browser, {
          method: 'PATCH',
          path: `/api/v1/admin/routing-groups/${groupId}`,
          org,
          sessionRealm: 'admin',
          body: {groupId, name: gname, strategy: 'weighted', enabled: true}
        }, (res5) => {
          const body5 = api.assertOk(browser, res5, 'AC4: enable');
          browser.assert.equal(body5.group.enabled, true, 'AC4: enabled');
        });
      });
    });
  },

  'AC4: enabling a manual group without a pinned candidate is blocked (13107)': function (browser) {
    const org = browser.globals.orgA;
    const gname = groupName(browser, 'auto-manual');
    seedRoutingGroups(browser, org);
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/routing-groups',
      org,
      sessionRealm: 'admin',
      body: {name: gname, strategy: 'manual'}
    }, (res) => {
      const groupId = api.assertOk(browser, res, 'AC4: create manual').group.groupId;
      // Add a candidate but no pin.
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/routing-groups/${groupId}/candidates`,
        org,
        sessionRealm: 'admin',
        body: {groupId, candidates: [{modelId: CAND_A, weight: 10}]}
      }, (res2) => {
        api.assertOk(browser, res2, 'AC4: candidates');
        // Enabling manual without a pin -> 13107.
        api.request(browser, {
          method: 'PATCH',
          path: `/api/v1/admin/routing-groups/${groupId}`,
          org,
          sessionRealm: 'admin',
          body: {groupId, name: gname, strategy: 'manual', enabled: true}
        }, (res3) => {
          api.assertBusinessError(browser, res3, 13107, 'AC4: manual without pin -> 13107');
        });
      });
    });
  },

  'AC5: replace rules, validate errors, and dry-run resolution (AC6)': function (browser) {
    const org = browser.globals.orgA;
    const gname = groupName(browser, 'auto-chat');
    seedRoutingGroups(browser, org);
    let groupId;
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/routing-groups',
      org,
      sessionRealm: 'admin',
      body: {name: gname, strategy: 'weighted'}
    }, (res) => {
      groupId = api.assertOk(browser, res, 'AC5: create').group.groupId;
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/routing-groups/${groupId}/candidates`,
        org,
        sessionRealm: 'admin',
        body: {
          groupId,
          candidates: [
            {modelId: CAND_A, weight: 50},
            {modelId: CAND_B, weight: 50}
          ]
        }
      }, (res2) => {
        api.assertOk(browser, res2, 'AC5: candidates');
        // Replace rules (keyword + regex).
        api.request(browser, {
          method: 'PUT',
          path: `/api/v1/admin/routing-groups/${groupId}/rules`,
          org,
          sessionRealm: 'admin',
          body: {
            groupId,
            rules: [
              {name: 'code', matchType: 'keyword', pattern: 'code', targetModelId: CAND_B, enabled: true},
              {name: 'fast', matchType: 'regex', pattern: '^quick', targetModelId: CAND_A, enabled: true}
            ]
          }
        }, (res3) => {
          const body3 = api.assertOk(browser, res3, 'AC5: replace rules');
          browser.assert.equal((body3.rules || []).length, 2, 'AC5: two rules');

          // Duplicate rule name -> 13112.
          api.request(browser, {
            method: 'PUT',
            path: `/api/v1/admin/routing-groups/${groupId}/rules`,
            org,
            sessionRealm: 'admin',
            body: {
              groupId,
              rules: [
                {name: 'code', matchType: 'keyword', pattern: 'code', targetModelId: CAND_A, enabled: true},
                {name: 'code', matchType: 'keyword', pattern: 'hack', targetModelId: CAND_B, enabled: true}
              ]
            }
          }, (res4) => {
            api.assertBusinessError(browser, res4, 13112, 'AC5: duplicate rule name -> 13112');
          });

          // Invalid regex -> 13108.
          api.request(browser, {
            method: 'PUT',
            path: `/api/v1/admin/routing-groups/${groupId}/rules`,
            org,
            sessionRealm: 'admin',
            body: {
              groupId,
              rules: [
                {name: 'bad', matchType: 'regex', pattern: '([unclosed', targetModelId: CAND_A, enabled: true}
              ]
            }
          }, (res5) => {
            api.assertBusinessError(browser, res5, 13108, 'AC5: invalid regex -> 13108');
          });

          // Dry-run resolution (AC6): a prompt with "code" matches the
          // keyword rule targeting CAND_B.
          api.request(browser, {
            method: 'POST',
            path: `/api/v1/admin/routing-groups/${groupId}:resolve`,
            org,
            sessionRealm: 'admin',
            body: {groupId, prompt: 'please write some code'}
          }, (res6) => {
            const body6 = api.assertOk(browser, res6, 'AC6: dry run');
            browser.assert.equal(body6.matchedRuleName, 'code', 'AC6: matched rule');
            const attempts = body6.attempts || [];
            browser.assert.ok(attempts.length >= 1, 'AC6: attempt list present');
            browser.assert.equal(attempts[0].modelId, CAND_B, 'AC6: rule target leads');

            // The dry run wrote no decision: the decision count before
            // and after the dry run is unchanged (the log is
            // platform-scoped, so it may already hold rows from other
            // runs).
            api.request(browser, {
              method: 'GET',
              path: '/api/v1/admin/routing-groups/decisions',
              org,
              sessionRealm: 'admin'
            }, (res7) => {
              const before = (api.assertOk(browser, res7, 'AC6: decisions before').decisions || []).length;
              api.request(browser, {
                method: 'POST',
                path: `/api/v1/admin/routing-groups/${groupId}:resolve`,
                org,
                sessionRealm: 'admin',
                body: {groupId, prompt: 'another prompt without a match'}
              }, (res8) => {
                api.assertOk(browser, res8, 'AC6: second dry run');
                api.request(browser, {
                  method: 'GET',
                  path: '/api/v1/admin/routing-groups/decisions',
                  org,
                  sessionRealm: 'admin'
                }, (res9) => {
                  const after = (api.assertOk(browser, res9, 'AC6: decisions after').decisions || []).length;
                  browser.assert.equal(after, before, 'AC6: dry run wrote no decision');
                });
              });
            });
          });
        });
      });
    });
  },

  'AC16: deleting a granted group is blocked (13111)': function (browser) {
    const org = browser.globals.orgA;
    const gname = groupName(browser, 'auto-chat');
    seedRoutingGroups(browser, org);
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/routing-groups',
      org,
      sessionRealm: 'admin',
      body: {name: gname, strategy: 'weighted'}
    }, (res) => {
      const groupId = api.assertOk(browser, res, 'AC16: create').group.groupId;
      // Grant the group to the org via the model grant seam.
      api.request(browser, {
        method: 'POST',
        path: `/api/v1/admin/models/${groupId}:grant`,
        org,
        sessionRealm: 'admin',
        body: {organizationId: org}
      }, (res2) => {
        // The grant may succeed or be a no-op; the delete must be blocked.
        api.request(browser, {
          method: 'DELETE',
          path: `/api/v1/admin/routing-groups/${groupId}`,
          org,
          sessionRealm: 'admin'
        }, (res3) => {
          api.assertBusinessError(browser, res3, 13111, 'AC16: delete with grant -> 13111');
        });
      });
    });
  },

  // ---- UI flow (AC1/AC15) ----

  'AC1/AC15: the admin page renders the group table and the user catalog shows the Auto marker': function (browser) {
    const org = browser.globals.orgA;
    const gname = groupName(browser, 'auto-chat');
    seedRoutingGroups(browser, org);
    // Create and enable a group so it appears as a virtual model.
    api.request(browser, {
      method: 'POST',
      path: '/api/v1/admin/routing-groups',
      org,
      sessionRealm: 'admin',
      body: {name: gname, strategy: 'weighted'}
    }, (res) => {
      const groupId = api.assertOk(browser, res, 'AC15: create').group.groupId;
      api.request(browser, {
        method: 'PUT',
        path: `/api/v1/admin/routing-groups/${groupId}/candidates`,
        org,
        sessionRealm: 'admin',
        body: {groupId, candidates: [{modelId: CAND_A, weight: 10}]}
      }, (res2) => {
        api.assertOk(browser, res2, 'AC15: candidates');
        api.request(browser, {
          method: 'PATCH',
          path: `/api/v1/admin/routing-groups/${groupId}`,
          org,
          sessionRealm: 'admin',
          body: {groupId, name: gname, strategy: 'weighted', enabled: true}
        }, (res3) => {
          api.assertOk(browser, res3, 'AC15: enable');

          // Admin page renders the table.
          browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${org}')`);
          browser.url(browser.globals.baseUrl + '/admin/routing-groups');
          browser.waitForElementPresent('[data-testid="routing-groups-table"]', 15000, 'AC1: table renders');
          browser.waitForElementPresent('[data-testid="routing-groups-add"]', 10000, 'AC1: create action');

          // The user catalog lists the granted group as a virtual entry
          // with the Auto marker.
          browser.execute(`localStorage.setItem('go-taas.user.org-id', '${org}')`);
          browser.url(browser.globals.baseUrl + '/models');
          browser.waitForElementPresent('[data-testid="models-table"]', 15000, 'AC15: models table');
          browser.waitForElementPresent('[data-testid="models-auto-badge"]', 15000, 'AC15: Auto badge present');
        });
      });
    });
  }
};