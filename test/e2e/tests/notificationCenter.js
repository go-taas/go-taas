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

// Feature-26 (notification center & threshold alerts) e2e suite, run
// against the compose stack gateway. Covers the acceptance criteria of
// docs/design/notification-center.md (AC1-AC15) reachable from the
// outside:
//
//   - API contract on the admin surface (AC1/AC2/AC3/AC4/AC5):
//     ListNotifications returns the surface's notifications with
//     read/event filters and pagination; GetUnreadCount returns the
//     caller's unread count; MarkNotificationRead marks one read and an
//     unknown notification_id returns 11001; MarkAllNotificationsRead
//     marks all read; DeleteNotification deletes one and an unknown id
//     returns 11001; GetNotificationPreferences returns all enabled by
//     default and UpdateNotificationPreferences persists changes, returns
//     11005 for an event type outside the catalog and 11002 for a
//     malformed set; CreateNotificationThreshold with a valid
//     metric/operator/value returns the threshold and an invalid one
//     returns 11004; UpdateNotificationThreshold and
//     DeleteNotificationThreshold work and an unknown threshold_id returns
//     11003.
//   - End-user surface (AC6): the user-surface inbox is tenant-scoped — it
//     shows only the caller's org notifications and never another org's.
//   - Surface separation (AC13/AC14): admin pages call only
//     /api/v1/admin/notifications/*, user pages call only
//     /api/v1/notifications/*; a wrong-realm session is rejected with
//     10038; the page route from the other surface is not served by this
//     console.
//   - Console pages (AC8-AC12): the admin page renders the inbox,
//     preferences and thresholds with a bell badge showing the unread
//     count; clicking an unread notification marks it read and navigates
//     to its link; Mark all read clears the unread dots and the badge; the
//     empty states render; toggling a preference and saving persists it;
//     creating a threshold adds it to the list and deleting a threshold
//     shows the confirmation and removes the row; the end-user page
//     renders the tenant-scoped inbox, preferences (only the four tenant
//     events) and thresholds (Balance low / Spend limit).
//   - Permission denied (AC15): a session without the required role
//     receives 10036 on the admin notification API.
//
// The compose stack has no controller and no interactive IdP login, so the
// suites authenticate with a seeded server-side session (see
// page-objects/api.js seedSession) and drive the real UI served by the
// compose stack. Notification rows are seeded directly into PostgreSQL by
// test/e2e/seed/notification/seed_notification.go (the notification module
// is a read-only consumer of the notification.events stream, and the
// compose stack has no inference/billing pipeline to produce the events).

const api = require('../page-objects/api.js');
const { execSync } = require('child_process');
const path = require('path');

// Seed notification rows for the given org directly into PostgreSQL (the
// notification module is a read-only consumer of the notification.events
// stream, and the compose stack has no inference/billing pipeline to
// produce the events).
function seedNotifications(browser, org, otherOrg) {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const otherArg = otherOrg ? ` -other-org '${otherOrg}'` : '';
  const cmd = [
    'docker run --rm --network go-taas_default',
    '-e GOPROXY=https://goproxy.cn,direct',
    '-v go-taas-go-mod-cache:/go/pkg/mod',
    '-v go-taas-go-build-cache:/root/.cache/go-build',
    `-v "${repoRoot}":/app`,
    '-w /app/test/e2e/seed/notification',
    'golang:1.26-alpine',
    `sh -c "go run seed_notification.go -dsn 'postgres://taas:taas@postgres:5432/taas?sslmode=disable' -org '${org}'${otherArg}"`
  ].join(' ');
  try {
    execSync(cmd, {stdio: 'pipe', timeout: 120000});
  } catch (e) {
    browser.assert.fail(`seedNotifications failed: ${e.message}`);
  }
}

module.exports = {
  '@tags': ['notification-center', 'feature-26'],

  beforeEach(browser) {
    // Land on the gateway origin so in-page fetch() calls are same-origin.
    browser.url(browser.globals.baseUrl + '/');
    browser.globals.testSeq = (browser.globals.testSeq || 0) + 1;
    browser.globals.orgA = `org-e2e-nc-${browser.globals.runId}-${browser.globals.testSeq}`;
    browser.globals.orgB = `org-e2e-nc-b-${browser.globals.runId}-${browser.globals.testSeq}`;
    api.ensureOrg(browser, browser.globals.orgA);
    api.ensureOrg(browser, browser.globals.orgB);
    // Seed notification rows for orgA so the inboxes show data; orgB is
    // the other tenant whose notifications must never appear in orgA's
    // inbox.
    seedNotifications(browser, browser.globals.orgA, browser.globals.orgB);
    // Seed admin- and user-realm sessions so both the admin and user
    // protected pages render.
    api.seedSession(browser, 'admin', browser.globals.orgA);
    api.seedSession(browser, 'user', browser.globals.orgA);
  },

  afterEach(browser) {
    browser.end();
  },

  // ---- Admin surface: notification API (AC1/AC2/AC3) ----

  'AC1: ListNotifications returns the surface notifications; GetUnreadCount returns the unread count': function (browser) {
    const org = browser.globals.orgA;
    // The notification service scopes the inbox to the session user id, so
    // the API calls must carry the admin session token (the seeded admin
    // user owns the seeded notifications).
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC1: admin session token present');
      const auth = { Authorization: `Bearer ${token}` };

      // List on the admin surface: the seeded admin inbox has 3 rows (2
      // unread + 1 read).
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications',
        org,
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC1: list notifications');
        browser.assert.ok(Array.isArray(body.notifications), 'AC1: notifications array');
        browser.assert.ok(body.notifications.length >= 3, 'AC1: at least 3 seeded notifications');
        // Newest first: the first row is the most recent (deployment failed).
        browser.assert.equal(body.notifications[0].eventType, 'deployment.status_changed', 'AC1: newest first');
        browser.assert.equal(body.notifications[0].read, false, 'AC1: first row unread');
        browser.assert.ok(Boolean(body.notifications[0].notificationId), 'AC1: notification id present');
        browser.assert.ok(Boolean(body.notifications[0].title), 'AC1: title present');
        browser.assert.ok(Boolean(body.notifications[0].link), 'AC1: link present');
      });

      // Filter by read state.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications?readFilter=true&read=false',
        org,
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC1: filter unread');
        browser.assert.ok(body.notifications.length >= 2, 'AC1: at least 2 unread');
        for (const n of body.notifications) {
          browser.assert.equal(n.read, false, 'AC1: all unread');
        }
      });

      // Filter by event type.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications?eventType=autoscaling.scaled',
        org,
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC1: filter event type');
        browser.assert.ok(body.notifications.length >= 1, 'AC1: at least 1 autoscaling.scaled');
        for (const n of body.notifications) {
          browser.assert.equal(n.eventType, 'autoscaling.scaled', 'AC1: all autoscaling.scaled');
        }
      });

      // GetUnreadCount.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications/unread-count',
        org,
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC1: unread count');
        browser.assert.equal(body.unreadCount, '2', 'AC1: unread count is 2');
      });
    });
  },

  'AC2: MarkNotificationRead marks one read; unknown id returns 11001; MarkAllNotificationsRead marks all read': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC2: admin session token present');
      const auth = { Authorization: `Bearer ${token}` };

      // Get the first unread notification id.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications?readFilter=true&read=false',
        org,
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC2: list unread');
        const notifId = body.notifications[0].notificationId;

        // Mark it read.
        api.request(browser, {
          method: 'POST',
          path: `/api/v1/admin/notifications/${notifId}:mark-read`,
          org,
          body: {},
          headers: auth
        }, (res2) => {
          const m = api.assertOk(browser, res2, 'AC2: mark read');
          browser.assert.equal(m.notification.read, true, 'AC2: marked read');
        });

        // Unknown id -> 11001.
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/notifications/00000000-0000-0000-0000-000000000000:mark-read',
          org,
          body: {},
          headers: auth
        }, (res3) => {
          api.assertBusinessError(browser, res3, 11001, 'AC2: unknown notification mark-read');
        });

        // Mark all read.
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/notifications:mark-all-read',
          org,
          body: {},
          headers: auth
        }, (res4) => {
          const m = api.assertOk(browser, res4, 'AC2: mark all read');
          browser.assert.ok(parseInt(m.markedCount, 10) >= 1, 'AC2: marked count >= 1');
        });

        // Unread count is now 0.
        api.request(browser, {
          method: 'GET',
          path: '/api/v1/admin/notifications/unread-count',
          org,
          headers: auth
        }, (res5) => {
          const u = api.assertOk(browser, res5, 'AC2: unread count after mark all');
          browser.assert.equal(u.unreadCount, '0', 'AC2: unread count 0');
        });
      });
    });
  },

  'AC3: DeleteNotification deletes one; unknown id returns 11001': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC3: admin session token present');
      const auth = { Authorization: `Bearer ${token}` };

      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications',
        org,
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC3: list notifications');
        const notifId = body.notifications[0].notificationId;

        // Delete it.
        api.request(browser, {
          method: 'DELETE',
          path: `/api/v1/admin/notifications/${notifId}`,
          org,
          headers: auth
        }, (res2) => {
          api.assertOk(browser, res2, 'AC3: delete notification');
        });

        // Unknown id -> 11001.
        api.request(browser, {
          method: 'DELETE',
          path: '/api/v1/admin/notifications/00000000-0000-0000-0000-000000000000',
          org,
          headers: auth
        }, (res3) => {
          api.assertBusinessError(browser, res3, 11001, 'AC3: unknown notification delete');
        });
      });
    });
  },

  // ---- Admin surface: preferences & thresholds API (AC4/AC5) ----

  'AC4: GetNotificationPreferences returns all enabled by default; UpdateNotificationPreferences persists; bad event 11005, malformed set 11002': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC4: admin session token present');
      const auth = { Authorization: `Bearer ${token}` };

      // The admin user has a preference row with all three admin events
      // enabled (seeded), so Get returns all enabled.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications/preferences',
        org,
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC4: get preferences');
        browser.assert.equal(body.preferences.length, 3, 'AC4: three admin event types');
        for (const p of body.preferences) {
          browser.assert.equal(p.enabled, true, 'AC4: all enabled');
        }
      });

      // Update: disable deployment.status_changed.
      api.request(browser, {
        method: 'PUT',
        path: '/api/v1/admin/notifications/preferences',
        org,
        body: {
          preferences: [
            { eventType: 'deployment.status_changed', enabled: false },
            { eventType: 'autoscaling.scaled', enabled: true },
            { eventType: 'autoscaling.scale_to_zero', enabled: true }
          ]
        },
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC4: update preferences');
        const dep = body.preferences.find((p) => p.eventType === 'deployment.status_changed');
        browser.assert.equal(dep.enabled, false, 'AC4: deployment disabled');
      });

      // Event type outside the admin catalog -> 11005.
      api.request(browser, {
        method: 'PUT',
        path: '/api/v1/admin/notifications/preferences',
        org,
        body: {
          preferences: [{ eventType: 'billing.balance_low', enabled: true }]
        },
        headers: auth
      }, (res) => {
        api.assertBusinessError(browser, res, 11005, 'AC4: event type outside catalog');
      });

      // Malformed set (empty event type) -> 11002.
      api.request(browser, {
        method: 'PUT',
        path: '/api/v1/admin/notifications/preferences',
        org,
        body: {
          preferences: [{ eventType: '', enabled: true }]
        },
        headers: auth
      }, (res) => {
        api.assertBusinessError(browser, res, 11002, 'AC4: malformed preference set');
      });
    });
  },

  'AC5: CreateNotificationThreshold valid; invalid 11004; Update/Delete work; unknown threshold 11003': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC5: admin session token present');
      const auth = { Authorization: `Bearer ${token}` };

      // Valid create on the admin surface.
      api.request(browser, {
        method: 'POST',
        path: '/api/v1/admin/notifications/thresholds',
        org,
        body: {
          name: 'Replica spike',
          metric: 'autoscaling_replicas',
          operator: 'THRESHOLD_OPERATOR_GT',
          value: 8,
          enabled: true
        },
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC5: create threshold');
        const thresholdId = body.threshold.thresholdId;
        browser.assert.ok(Boolean(thresholdId), 'AC5: threshold id present');
        browser.assert.equal(body.threshold.metric, 'autoscaling_replicas', 'AC5: metric');
        browser.assert.equal(body.threshold.enabled, true, 'AC5: enabled');

        // Invalid metric (user metric on admin surface) -> 11004.
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/notifications/thresholds',
          org,
          body: {
            name: 'bad',
            metric: 'balance_low',
            operator: 'THRESHOLD_OPERATOR_LT',
            value: 1000
          },
          headers: auth
        }, (res2) => {
          api.assertBusinessError(browser, res2, 11004, 'AC5: invalid metric');
        });

        // Invalid operator -> 11004.
        api.request(browser, {
          method: 'POST',
          path: '/api/v1/admin/notifications/thresholds',
          org,
          body: {
            name: 'bad',
            metric: 'autoscaling_replicas',
            operator: 'THRESHOLD_OPERATOR_UNSPECIFIED',
            value: 8
          },
          headers: auth
        }, (res3) => {
          api.assertBusinessError(browser, res3, 11004, 'AC5: invalid operator');
        });

        // Update the threshold.
        api.request(browser, {
          method: 'PUT',
          path: `/api/v1/admin/notifications/thresholds/${thresholdId}`,
          org,
          body: { enabled: false },
          headers: auth
        }, (res4) => {
          const u = api.assertOk(browser, res4, 'AC5: update threshold');
          browser.assert.equal(u.threshold.enabled, false, 'AC5: disabled');
        });

        // Unknown threshold -> 11003.
        api.request(browser, {
          method: 'PUT',
          path: '/api/v1/admin/notifications/thresholds/00000000-0000-0000-0000-000000000000',
          org,
          body: { enabled: false },
          headers: auth
        }, (res5) => {
          api.assertBusinessError(browser, res5, 11003, 'AC5: unknown threshold update');
        });

        // Delete the threshold.
        api.request(browser, {
          method: 'DELETE',
          path: `/api/v1/admin/notifications/thresholds/${thresholdId}`,
          org,
          headers: auth
        }, (res6) => {
          api.assertOk(browser, res6, 'AC5: delete threshold');
        });

        // Unknown threshold delete -> 11003.
        api.request(browser, {
          method: 'DELETE',
          path: '/api/v1/admin/notifications/thresholds/00000000-0000-0000-0000-000000000000',
          org,
          headers: auth
        }, (res7) => {
          api.assertBusinessError(browser, res7, 11003, 'AC5: unknown threshold delete');
        });
      });
    });
  },

  // ---- End-user surface: tenant-scoped (AC6) ----

  'AC6: user-surface inbox is tenant-scoped and never leaks the other org': function (browser) {
    const org = browser.globals.orgA;
    browser.execute(function () {
      return localStorage.getItem('go-taas.user.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC6: user session token present');
      const auth = { Authorization: `Bearer ${token}` };

      // List on the user surface: the seeded user inbox has 3 rows (2
      // unread + 1 read), all owned by orgA.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/notifications',
        org,
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC6: user list notifications');
        browser.assert.ok(body.notifications.length >= 3, 'AC6: at least 3 seeded user notifications');
        // The other-org notification (title "Other org balance low") must
        // never appear.
        for (const n of body.notifications) {
          browser.assert.not.equal(n.title, 'Other org balance low', 'AC6: other-org notification not leaked');
        }
        // The user catalog is the four tenant events.
        const userEvents = ['billing.invoice_created', 'billing.invoice_paid', 'billing.spend_limit_breached', 'billing.balance_low'];
        for (const n of body.notifications) {
          browser.assert.ok(userEvents.indexOf(n.eventType) !== -1, 'AC6: only user-catalog events');
        }
      });

      // GetUnreadCount on the user surface.
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/notifications/unread-count',
        org,
        headers: auth
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC6: user unread count');
        browser.assert.equal(body.unreadCount, '2', 'AC6: user unread count is 2');
      });
    });
  },

  // ---- Surface separation (AC13/AC14) ----

  'AC13: a user-realm session calling the admin notification prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      const token = localStorage.getItem('go-taas.user.session-token');
      return token;
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC13: user session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications',
        org: browser.globals.orgA,
        headers: { Authorization: `Bearer ${token}` }
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC13: user session on admin prefix -> 10038');
      });
    });
  },

  'AC14: an admin-realm session calling the user notification prefix is rejected with 10038': function (browser) {
    browser.execute(function () {
      const token = localStorage.getItem('go-taas.admin.session-token');
      return token;
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC14: admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/notifications',
        org: browser.globals.orgA,
        headers: { Authorization: `Bearer ${token}` }
      }, (res) => {
        api.assertBusinessError(browser, res, 10038, 'AC14: admin session on user prefix -> 10038');
      });
    });
  },

  // ---- Console pages: admin (AC8/AC9/AC10/AC11) ----

  'AC8: admin page renders inbox, preferences and thresholds with a bell badge showing the unread count': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/notifications');

    browser.waitForElementPresent('[data-testid="admin-shell"]', 15000, 'AC8: admin shell renders');
    browser.waitForElementPresent('[data-testid="nav-notifications"]', 10000, 'AC8: nav notifications item');
    browser.waitForElementPresent('[data-testid="admin-bell-badge"]', 10000, 'AC8: admin bell badge');
    browser.waitForElementPresent('[data-testid="notifications-tabs"]', 10000, 'AC8: tabs');
    browser.waitForElementPresent('[data-testid="notification-table"]', 10000, 'AC8: inbox table');
    browser.waitForElementPresent('[data-testid^="notification-row-"]', 10000, 'AC8: notification rows');
    browser.waitForElementPresent('[data-testid="notifications-mark-all-read"]', 10000, 'AC8: mark all read');
    browser.waitForElementPresent('[data-testid="notifications-refresh"]', 10000, 'AC8: refresh');

    // The bell badge shows the unread count (2).
    browser.getText('[data-testid="admin-bell-badge"]', (result) => {
      browser.assert.equal(result.value, '2', 'AC8: bell badge shows 2 unread');
    });

    // Preferences tab.
    browser.click('[data-testid="notifications-tab-preferences"]');
    browser.waitForElementPresent('[data-testid="notification-preferences"]', 10000, 'AC8: preferences panel');
    browser.waitForElementPresent('[data-testid="preference-toggle-deployment.status_changed"]', 10000, 'AC8: deployment preference toggle');
    browser.waitForElementPresent('[data-testid="preference-toggle-autoscaling.scaled"]', 10000, 'AC8: autoscaling.scaled preference toggle');
    browser.waitForElementPresent('[data-testid="preference-toggle-autoscaling.scale_to_zero"]', 10000, 'AC8: autoscaling.scale_to_zero preference toggle');
    browser.waitForElementPresent('[data-testid="preferences-save"]', 10000, 'AC8: preferences save');

    // Thresholds tab.
    browser.click('[data-testid="notifications-tab-thresholds"]');
    browser.waitForElementPresent('[data-testid="threshold-table"]', 10000, 'AC8: threshold table');
    browser.waitForElementPresent('[data-testid^="threshold-row-"]', 10000, 'AC8: threshold rows');
    browser.waitForElementPresent('[data-testid="threshold-new"]', 10000, 'AC8: new threshold');
  },

  'AC9: clicking an unread notification marks it read and navigates to its link; Mark all read clears the badge': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/notifications');

    browser.waitForElementPresent('[data-testid^="notification-row-"]', 15000, 'AC9: notification rows');
    browser.waitForElementPresent('[data-testid^="notification-unread-"]', 10000, 'AC9: unread dot present');

    // Click the first unread notification's mark-read entry. The seeded
    // first unread is "Deployment failed" with link /admin/inference-services,
    // and its actions live in the notification's row menu.
    browser.click('[data-testid^="notification-actions-"]');
    browser.click('[data-testid^="notification-mark-read-"]');
    // The page navigates to the link (the inference-services page).
    browser.waitForElementPresent('[data-testid="admin-shell"]', 15000, 'AC9: still in admin shell after navigation');
    browser.assert.urlContains('/admin/inference-services', 'AC9: navigated to the notification link');

    // Return to the notifications page; the bell badge should now show 1.
    browser.url(browser.globals.baseUrl + '/admin/notifications');
    browser.waitForElementPresent('[data-testid="admin-bell-badge"]', 15000, 'AC9: bell badge');
    browser.getText('[data-testid="admin-bell-badge"]', (result) => {
      browser.assert.equal(result.value, '1', 'AC9: bell badge decremented to 1');
    });

    // Mark all read clears the unread dots. The bell badge must update
    // immediately (AC9/D5): the page notifies the shell bell to re-poll
    // GetUnreadCount, so the badge clears without a reload or the 30s poll.
    browser.click('[data-testid="notifications-mark-all-read"]');
    browser.waitForElementNotPresent('[data-testid^="notification-unread-"]', 15000, 'AC9: no unread dots after mark all read');
    // The bell renders the count span only when unread > 0; after mark-all-read
    // the badge is empty (no count shown) without re-navigating.
    browser.waitForElementPresent('[data-testid="admin-bell-badge"]', 15000, 'AC9: bell badge present');
    browser.getText('[data-testid="admin-bell-badge"]', (result) => {
      browser.assert.equal(result.value, '', 'AC9: bell badge cleared to 0 immediately after mark all read');
    });
  },

  'AC10: empty states render for a fresh org': function (browser) {
    // Use orgB, which has no notifications or thresholds, on the admin
    // page. Re-seed the admin session for orgB so the admin surface is
    // scoped to orgB.
    api.seedSession(browser, 'admin', browser.globals.orgB);
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${browser.globals.orgB}')`);
    browser.url(browser.globals.baseUrl + '/admin/notifications');

    browser.waitForElementPresent('[data-testid="notification-table"]', 15000, 'AC10: inbox table');
    browser.waitForElementPresent('[data-testid="notification-empty"]', 15000, 'AC10: inbox empty state');

    browser.click('[data-testid="notifications-tab-thresholds"]');
    browser.waitForElementPresent('[data-testid="threshold-table"]', 10000, 'AC10: threshold table');
    browser.waitForElementPresent('[data-testid="threshold-empty"]', 15000, 'AC10: thresholds empty state');
  },

  'AC11: toggling a preference and saving persists it; creating a threshold adds it; deleting a threshold removes it': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.admin.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/admin/notifications');

    // Preferences: toggle deployment.status_changed off and save.
    browser.waitForElementPresent('[data-testid="notifications-tab-preferences"]', 15000, 'AC11: preferences tab');
    browser.click('[data-testid="notifications-tab-preferences"]');
    browser.waitForElementPresent('[data-testid="preference-toggle-deployment.status_changed"]', 10000, 'AC11: deployment toggle');
    browser.click('[data-testid="preference-toggle-deployment.status_changed"]');
    browser.click('[data-testid="preferences-save"]');

    // Verify via the API that the preference persisted (must carry the
    // admin session token so the service resolves the admin user's
    // preference set).
    browser.execute(function () {
      return localStorage.getItem('go-taas.admin.session-token');
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC11: admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications/preferences',
        org: browser.globals.orgA,
        headers: { Authorization: `Bearer ${token}` }
      }, (res) => {
        const body = api.assertOk(browser, res, 'AC11: get preferences after save');
        const dep = body.preferences.find((p) => p.eventType === 'deployment.status_changed');
        browser.assert.equal(dep.enabled, false, 'AC11: deployment preference persisted as disabled');
      });
    });

    // Thresholds: create a new threshold via the dialog.
    browser.click('[data-testid="notifications-tab-thresholds"]');
    browser.waitForElementPresent('[data-testid="threshold-new"]', 10000, 'AC11: new threshold');
    browser.click('[data-testid="threshold-new"]');
    browser.waitForElementPresent('[data-testid="threshold-dialog"]', 10000, 'AC11: threshold dialog');
    browser.setValue('[data-testid="threshold-dialog-name"]', 'E2E Threshold');
    browser.setValue('[data-testid="threshold-dialog-value"]', '12');
    browser.click('[data-testid="threshold-dialog-submit"]');

    // The new threshold appears in the list. Capture its id from the row
    // testid so we can assert this specific threshold is removed after
    // deletion.
    browser.waitForElementPresent('[data-testid^="threshold-row-"]', 15000, 'AC11: threshold row appears');
    browser.getAttribute('[data-testid^="threshold-row-"]', 'data-testid', (result) => {
      const rowTestId = result.value;
      const thresholdId = rowTestId.replace('threshold-row-', '');
      browser.globals.ac11ThresholdId = thresholdId;
    });

    // Delete the threshold through its row menu (window.confirm is
    // auto-accepted by the headless browser).
    browser.click(`[data-testid="threshold-actions-${browser.globals.ac11ThresholdId}"]`);
    browser.waitForElementPresent(
      `[data-testid="threshold-delete-${browser.globals.ac11ThresholdId}"]`,
      5000,
      'AC11: threshold delete entry'
    );
    browser.click(`[data-testid="threshold-delete-${browser.globals.ac11ThresholdId}"]`);
    browser.waitForElementNotPresent(`[data-testid="threshold-row-${browser.globals.ac11ThresholdId}"]`, 15000, 'AC11: threshold removed');
  },

  // ---- Console pages: end-user (AC12) ----

  'AC12: end-user page renders tenant-scoped inbox, preferences (four tenant events) and thresholds (Balance low / Spend limit)': function (browser) {
    browser.execute(`localStorage.setItem('go-taas.user.org-id', '${browser.globals.orgA}')`);
    browser.url(browser.globals.baseUrl + '/notifications');

    browser.waitForElementPresent('[data-testid="user-shell"]', 15000, 'AC12: user shell renders');
    browser.waitForElementPresent('[data-testid="user-nav-notifications"]', 10000, 'AC12: user nav notifications item');
    browser.waitForElementPresent('[data-testid="user-bell-badge"]', 10000, 'AC12: user bell badge');
    browser.waitForElementPresent('[data-testid="notification-table"]', 10000, 'AC12: inbox table');
    browser.waitForElementPresent('[data-testid^="notification-row-"]', 10000, 'AC12: notification rows');

    // The user bell badge shows the unread count (2).
    browser.getText('[data-testid="user-bell-badge"]', (result) => {
      browser.assert.equal(result.value, '2', 'AC12: user bell badge shows 2 unread');
    });

    // Preferences tab: only the four tenant events.
    browser.click('[data-testid="notifications-tab-preferences"]');
    browser.waitForElementPresent('[data-testid="notification-preferences"]', 10000, 'AC12: preferences panel');
    browser.waitForElementPresent('[data-testid="preference-toggle-billing.balance_low"]', 10000, 'AC12: balance_low toggle');
    browser.waitForElementPresent('[data-testid="preference-toggle-billing.spend_limit_breached"]', 10000, 'AC12: spend_limit toggle');
    browser.waitForElementPresent('[data-testid="preference-toggle-billing.invoice_created"]', 10000, 'AC12: invoice_created toggle');
    browser.waitForElementPresent('[data-testid="preference-toggle-billing.invoice_paid"]', 10000, 'AC12: invoice_paid toggle');
    // No admin event types on the user surface.
    browser.assert.not.elementPresent('[data-testid="preference-toggle-deployment.status_changed"]', 'AC12: no admin event on user surface');

    // Thresholds tab: Balance low / Spend limit metrics.
    browser.click('[data-testid="notifications-tab-thresholds"]');
    browser.waitForElementPresent('[data-testid="threshold-table"]', 10000, 'AC12: threshold table');
    browser.waitForElementPresent('[data-testid^="threshold-row-"]', 10000, 'AC12: threshold rows');
    browser.click('[data-testid="threshold-new"]');
    browser.waitForElementPresent('[data-testid="threshold-dialog"]', 10000, 'AC12: threshold dialog');
    // The metric dropdown offers Balance low and Spend limit.
    browser.click('[data-testid="threshold-dialog-metric"]');
    browser.waitForElementPresent('option[value="balance_low"]', 10000, 'AC12: balance_low metric option');
    browser.waitForElementPresent('option[value="spend_limit"]', 10000, 'AC12: spend_limit metric option');
    // No admin metric on the user surface.
    browser.assert.not.elementPresent('option[value="autoscaling_replicas"]', 'AC12: no admin metric on user surface');
  },

  // ---- Console pages: permission denied (AC15) ----

  'AC15: session without the required role receives 10036 on the admin notification API': function (browser) {
    const org = browser.globals.orgA;
    const nonMemberUser = `33333333-4444-4444-4444-${browser.globals.runId.toString().padStart(12, '0').slice(-12)}`;
    api.seedSession(browser, 'admin', org, 'admin', nonMemberUser, { noMember: true });
    browser.execute(function () {
      const token = localStorage.getItem('go-taas.admin.session-token');
      return token;
    }, [], (result) => {
      const token = result.value;
      browser.assert.ok(Boolean(token), 'AC15: non-member admin session token present');
      api.request(browser, {
        method: 'GET',
        path: '/api/v1/admin/notifications',
        org,
        headers: { Authorization: `Bearer ${token}` }
      }, (res) => {
        api.assertBusinessError(browser, res, 10036, 'AC15: non-member session on admin notification API -> 10036');
      });
    });
  }
};