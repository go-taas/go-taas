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

GO ?= go

.PHONY: all pbgen deps lint ut build test clean

all: build

## pbgen: update buf dependencies and regenerate protobuf code
pbgen:
	buf dep update
	buf generate

## deps: download go module dependencies
deps:
	$(GO) mod download

## lint: run golangci-lint over the repository
lint:
	golangci-lint run

## ut: run unit tests
ut:
	$(GO) test -count=1 ./...

## build: compile all binaries
build:
	$(GO) build ./...

## test: run unit tests with race detector
test:
	$(GO) test -race -count=1 ./...

## clean: remove build artifacts
clean:
	$(GO) clean
