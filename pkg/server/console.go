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
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

// consoleFS holds the built admin console bundle (web/dist). The build
// step copies the Vite output here via a symlink-free copy in the
// Dockerfile; a missing bundle leaves the console disabled (API-only
// gateway), which keeps unit-test and library consumers working.
//
//go:embed all:console
var consoleFS embed.FS

// consoleEnabled reports whether a console bundle is embedded.
func consoleEnabled() bool {
	_, err := fs.Stat(consoleFS, "console")
	return err == nil
}

// withConsole wraps the gateway mux so that:
//   - /api/* and other gateway routes pass through unchanged;
//   - static assets are served from the embedded bundle;
//   - unknown non-API paths fall back to index.html (SPA history
//     routing: /models, /inference-services/... must render the app).
func withConsole(gw *runtime.ServeMux) http.Handler {
	return consoleHandler(gw)
}

// consoleHandler builds the console-aware handler around any gateway
// handler (the runtime mux in production, a stub in tests).
func consoleHandler(gw http.Handler) http.Handler {
	sub, err := fs.Sub(consoleFS, "console")
	if err != nil {
		// No bundle embedded: gateway only.
		return gw
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" && path != "index.html" {
			if _, err := fs.Stat(sub, path); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: serve index.html for any non-API path.
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			index, err := fs.ReadFile(sub, "index.html")
			if err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(index)
				return
			}
		}
		gw.ServeHTTP(w, r)
	})
}
