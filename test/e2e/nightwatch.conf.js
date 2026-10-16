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

// Nightwatch configuration for the go-taas e2e suites.
//
// The suites exercise the control-plane REST gateway of the local docker
// compose stack (deploy/compose/docker-compose.yaml). Requests are issued
// from a real headless Chromium browser session via fetch(), so every case
// runs through the same browser HTTP stack a console user would use.
//
// Environment overrides:
//   E2E_BASE_URL   gateway base url   (default http://127.0.0.1:9091)
//   CHROMEDRIVER_PATH  custom chromedriver binary (default: the npm
//                      `chromedriver` package bundled with this project)

const path = require('path');
const os = require('os');
const fs = require('fs');

const baseUrl = process.env.E2E_BASE_URL || 'http://127.0.0.1:9091';
// The chromedriver port. Overridable so a stale driver holding the
// default port does not block a run.
const driverPort = parseInt(process.env.E2E_DRIVER_PORT || '9515', 10);

// Snap-confined Chromium redirects --user-data-dir under /tmp into its
// private ~/snap/chromium/common/chromium profile, so chromedriver can
// never find the DevToolsActivePort file it expects next to the requested
// directory. A profile directory inside the snap-writable home keeps the
// file where chromedriver looks. Non-snap Chrome/Chromium ignores this
// and works with any path, so the flag is harmless there.
const snapWritableHome = path.join(os.homedir(), 'snap', 'chromium', 'common');
// The console localizes to the browser language (en unless the browser is
// Chinese); the suites assert English copy, so pin the browser locale to
// English regardless of the host locale.
const localeArgs = ['--lang=en-US'];
let chromeArgs;
if (fs.existsSync(snapWritableHome)) {
  const profileDir = fs.mkdtempSync(path.join(snapWritableHome, 'e2e-profile-'));
  chromeArgs = [
    '--headless=new',
    '--no-sandbox',
    '--disable-gpu',
    '--disable-dev-shm-usage',
    ...localeArgs,
    `--user-data-dir=${profileDir}`
  ];
} else {
  chromeArgs = ['--headless=new', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage', ...localeArgs];
}

let chromedriverPath;
try {
  // Prefer the version-pinned driver shipped as an npm devDependency so the
  // driver major version always matches the installed Chromium.
  chromedriverPath = require('chromedriver').path;
} catch (err) {
  chromedriverPath = undefined;
}

module.exports = {
  src_folders: ['tests'],

  output_folder: 'reports',

  globals: {
    // Unique suffix shared by every test of one run: the compose database
    // persists between runs, so names must never collide across runs.
    runId: String(Math.floor(Date.now() / 1000)),
    // Distinct organization ids used for ownership/isolation assertions.
    orgA: null, // set per suite in beforeEach
    orgB: null,
    waitForConditionTimeout: 10000,
    retryAssertionTimeout: 5000
  },

  test_settings: {
    default: {
      globals: {
        baseUrl
      },
      webdriver: {
        start_process: true,
        server_path: chromedriverPath,
        port: driverPort,
        cli_args: []
      },
      desiredCapabilities: {
        browserName: 'chrome',
        'goog:chromeOptions': {
          w3c: true,
          args: chromeArgs,
          // Also pin the accept-languages preference so navigator.language
          // is en-US even where --lang is ignored.
          prefs: {'intl.accept_languages': 'en-US,en'}
        }
      },
      screenshots: {
        enabled: false
      }
    }
  }
};
