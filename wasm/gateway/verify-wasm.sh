#!/bin/sh
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

# verify-wasm.sh checks that a built plugin is loadable by Envoy.
#
# It exists because a build can succeed and still produce an unusable module:
# `-buildmode=c-shared` links, exports every callback, and silently drops all
# but one of the proxy-wasm host functions — so the filter instantiates and
# then fails at the first hostcall. Checking the imports is the only way to
# catch that from the outside.
set -eu

wasm="${1:?usage: verify-wasm.sh <module.wasm>}"

for symbol in proxy_on_configure proxy_on_request_headers proxy_on_http_call_response \
	proxy_http_call proxy_get_header_map_value proxy_send_local_response \
	proxy_get_shared_data proxy_set_shared_data; do
	if ! grep -q "$symbol" "$wasm"; then
		echo "verify-wasm: $wasm does not reference $symbol" >&2
		exit 1
	fi
done

echo "verify-wasm: $wasm looks loadable"
