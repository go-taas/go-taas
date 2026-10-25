# Copyright 2025 The go-taas Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# The plugin is built with TinyGo, not the Go toolchain: the target is a
# proxy-wasm Wasm module, which needs a WASI-capable CGO-free backend and the
# size TinyGo's defaults produce. The image tag is pinned so the artifact is
# reproducible from the repository alone.
TINYGO_IMAGE ?= tinygo/tinygo:0.34.0
TARGET       ?= wasi
OUT          ?= bin/gateway-plugin.wasm
GOPROXY      ?= https://proxy.golang.org,direct

.PHONY: build
## build: compile the Wasm artifact
build:
	mkdir -p $(dir $(OUT))
	docker run --rm -v "$(CURDIR)":/src -w /src -e GOPROXY=$(GOPROXY) \
		$(TINYGO_IMAGE) tinygo build -target=$(TARGET) -scheduler=none \
		-opt=z -no-debug -o $(OUT) .
	@# A module that cannot reach the host is useless, so prove the ABI
	@# surface survived the build: proxy_on_configure is how the policy
	@# config arrives, and proxy_http_call is how the platform is asked.
	@$(CURDIR)/verify-wasm.sh $(OUT)

.PHONY: test
## test: run the plugin's unit tests with the standard toolchain
test:
	go test ./...

.PHONY: lint
## lint: vet the plugin
lint:
	go vet ./...

.PHONY: clean
## clean: remove build output
clean:
	rm -rf bin
