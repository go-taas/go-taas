// The data-plane gateway plugin is a separate module on purpose: TinyGo
// tracks the Go language version it supports, which trails the main
// module's. Keeping it here means `go build ./...` and `golangci-lint run`
// in the repository root never try to compile it (nested modules are not
// walked), and the plugin can pin the SDK version it was tested against.
module github.com/go-taas/go-taas/wasm/gateway

go 1.23

require (
	github.com/stretchr/testify v1.9.0
	github.com/tetratelabs/proxy-wasm-go-sdk v0.24.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/tetratelabs/wazero v1.7.2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
