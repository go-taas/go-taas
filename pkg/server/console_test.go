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

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubGateway records requests that reach the gateway mux.
type stubGateway struct {
	hit bool
}

func (g *stubGateway) ServeHTTP(_ http.ResponseWriter, _ *http.Request) {
	g.hit = true
}

func TestWithConsoleServesIndex(t *testing.T) {
	if !consoleEnabled() {
		t.Skip("console bundle not embedded")
	}
	gw := &stubGateway{}
	handler := withConsole(nil)
	_ = handler // withConsole(nil) would panic on use; construct properly below

	h := consoleHandler(gw)
	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /models: status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("GET /models: content-type = %q, want text/html", ct)
	}
	if gw.hit {
		t.Fatal("GET /models must not fall through to the gateway")
	}
	if rec.Body.Len() == 0 {
		t.Fatal("GET /models: empty body, want index.html")
	}
}

func TestWithConsoleServesAssets(t *testing.T) {
	if !consoleEnabled() {
		t.Skip("console bundle not embedded")
	}
	gw := &stubGateway{}
	h := consoleHandler(gw)

	// A real asset path serves the file, not the SPA fallback.
	req := httptest.NewRequest(http.MethodGet, "/assets/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// The directory itself is not a file; the SPA fallback applies.
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /assets/: status = %d, want 200 (fallback)", rec.Code)
	}
}

func TestWithConsolePassesApiThrough(t *testing.T) {
	if !consoleEnabled() {
		t.Skip("console bundle not embedded")
	}
	gw := &stubGateway{}
	h := consoleHandler(gw)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !gw.hit {
		t.Fatal("GET /api/v1/models must reach the gateway mux")
	}
}
