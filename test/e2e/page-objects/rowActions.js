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
 * Row-action helper.
 *
 * Console list pages render their per-row actions inside one overflow menu
 * (`RowActionsMenu` in `web/src/components.tsx`): a `*-actions-{id}` trigger
 * opens a popover that contains the action items. The item keeps its own
 * test id (`delete-{id}`, `quota-{id}`, ...), but it only exists in the DOM
 * while the menu is open, so a suite must open the menu before clicking an
 * item.
 */
const rowActions = {
  /** Selector of a row's overflow trigger. */
  trigger(actionTestId) {
    return `[data-testid="${actionTestId}"]`;
  },

  /** Selector of an action item inside an opened menu. */
  item(itemTestId) {
    return `[data-testid="${itemTestId}"]`;
  },

  /**
   * Open the menu of the row whose trigger carries `actionTestId` and wait
   * for `itemTestId` to appear.
   */
  open(browser, actionTestId, itemTestId, message) {
    browser.waitForElementPresent(
      this.trigger(actionTestId),
      10000,
      message || `row actions trigger ${actionTestId}`,
    );
    browser.click(this.trigger(actionTestId));
    if (itemTestId) {
      browser.waitForElementPresent(
        this.item(itemTestId),
        5000,
        message || `row action ${itemTestId}`,
      );
    }
    return this;
  },

  /**
   * Open the row menu and click one action. This is the common case: the
   * action lives behind the row's overflow trigger.
   */
  click(browser, actionTestId, itemTestId, message) {
    this.open(browser, actionTestId, itemTestId, message);
    browser.click(this.item(itemTestId));
    return this;
  },
};

module.exports = rowActions;
