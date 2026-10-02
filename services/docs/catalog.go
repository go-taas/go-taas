// Package docs implements the curated user-realm API catalog served by
// the API documentation explorer (feature #38). It serves the end-user
// API Docs page (GetApiDocs), returning a static, versioned projection
// of the endpoints a tenant/Agent can call — the OpenAI-compatible
// inference API plus the user-realm control-plane APIs — and never
// exposes admin endpoints or operator internals (AD2, AD3).
package docs

import (
	docsv1 "github.com/go-taas/go-taas/proto/taas/docs/v1"
)

// catalogVersion is the version of the curated catalog (AD3, AD7).
const catalogVersion = "v1"

// catalogV1 is the static, versioned catalog. It is a curated projection
// (AD2): it lists only the endpoints a tenant/Agent can call and never
// exposes admin endpoints or operator internals.
var catalogV1 = []*docsv1.ApiDocsCategory{
	{
		CategoryId:   "inference",
		CategoryName: "Inference",
		Endpoints: []*docsv1.ApiDocsEndpoint{
			{
				EndpointId:      "chat-completions",
				Method:          "POST",
				Path:            "/v1/chat/completions",
				Summary:         "Create a chat completion",
				Description:     "Send a chat conversation to a model and receive a completion. This is the OpenAI-compatible inference endpoint Agents call.",
				Parameters: []*docsv1.ApiDocsParameter{
					{Name: "model", In: "body", Required: true, Type: "string", Description: "The model id to call."},
					{Name: "messages", In: "body", Required: true, Type: "array", Description: "The chat messages."},
					{Name: "temperature", In: "body", Required: false, Type: "number", Description: "Sampling temperature."},
				},
				RequestExample:  `{"model":"qwen-3b","messages":[{"role":"user","content":"Hello"}]}`,
				ResponseExample: `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"Hi there!"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
				ErrorCodes: []*docsv1.ApiDocsErrorCode{
					{Code: 10008, Constant: "API_KEY_INVALID", Message: "API key invalid"},
					{Code: 10009, Constant: "API_KEY_REVOKED", Message: "API key revoked"},
					{Code: 10105, Constant: "MODEL_NOT_AUTHORIZED", Message: "model not authorized"},
				},
				Tryable: true,
			},
			{
				EndpointId:      "embeddings",
				Method:          "POST",
				Path:            "/v1/embeddings",
				Summary:         "Create an embedding",
				Description:     "Send text to a model and receive a vector embedding.",
				Parameters: []*docsv1.ApiDocsParameter{
					{Name: "model", In: "body", Required: true, Type: "string", Description: "The model id to call."},
					{Name: "input", In: "body", Required: true, Type: "string", Description: "The text to embed."},
				},
				RequestExample:  `{"model":"qwen-3b","input":"Hello world"}`,
				ResponseExample: `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2,0.3],"index":0}]}`,
				ErrorCodes: []*docsv1.ApiDocsErrorCode{
					{Code: 10008, Constant: "API_KEY_INVALID", Message: "API key invalid"},
					{Code: 10105, Constant: "MODEL_NOT_AUTHORIZED", Message: "model not authorized"},
				},
				Tryable: true,
			},
		},
	},
	{
		CategoryId:   "models",
		CategoryName: "Models",
		Endpoints: []*docsv1.ApiDocsEndpoint{
			{
				EndpointId:      "list-models",
				Method:          "GET",
				Path:            "/api/v1/models",
				Summary:         "List available models",
				Description:     "List the models your organization is authorized to call.",
				Parameters:      []*docsv1.ApiDocsParameter{},
				RequestExample:  ``,
				ResponseExample: `{"models":[{"modelId":"qwen-3b","name":"Qwen 3B","latestVersion":"v1"}]}`,
				ErrorCodes: []*docsv1.ApiDocsErrorCode{
					{Code: 10036, Constant: "FORBIDDEN", Message: "forbidden"},
				},
				Tryable: true,
			},
		},
	},
	{
		CategoryId:   "api-keys",
		CategoryName: "API Keys",
		Endpoints: []*docsv1.ApiDocsEndpoint{
			{
				EndpointId:      "list-api-keys",
				Method:          "GET",
				Path:            "/api/v1/auth/api-keys",
				Summary:         "List API keys",
				Description:     "List the API keys of your organization.",
				Parameters:      []*docsv1.ApiDocsParameter{},
				RequestExample:  ``,
				ResponseExample: `{"keys":[{"keyId":"key-1","name":"prod","prefix":"sk-abc"}]}`,
				ErrorCodes: []*docsv1.ApiDocsErrorCode{
					{Code: 10036, Constant: "FORBIDDEN", Message: "forbidden"},
				},
				Tryable: true,
			},
		},
	},
	{
		CategoryId:   "usage",
		CategoryName: "Usage",
		Endpoints: []*docsv1.ApiDocsEndpoint{
			{
				EndpointId:      "get-usage",
				Method:          "GET",
				Path:            "/api/v1/usage",
				Summary:         "Get usage",
				Description:     "Get your organization's token usage over a time range.",
				Parameters: []*docsv1.ApiDocsParameter{
					{Name: "since", In: "query", Required: false, Type: "integer", Description: "Start of the range, unix seconds."},
					{Name: "until", In: "query", Required: false, Type: "integer", Description: "End of the range, unix seconds."},
				},
				RequestExample:  ``,
				ResponseExample: `{"usage":{"totalTokens":"1000","requests":"10"}}`,
				ErrorCodes: []*docsv1.ApiDocsErrorCode{
					{Code: 10404, Constant: "METERING_RANGE_INVALID", Message: "metering range invalid"},
				},
				Tryable: true,
			},
		},
	},
	{
		CategoryId:   "inference-endpoint",
		CategoryName: "Inference endpoint",
		Endpoints: []*docsv1.ApiDocsEndpoint{
			{
				EndpointId:      "get-inference-endpoint",
				Method:          "GET",
				Path:            "/api/v1/inference-endpoint",
				Summary:         "Get the inference base URL",
				Description:     "Get the public base URL of the OpenAI-compatible inference gateway.",
				Parameters:      []*docsv1.ApiDocsParameter{},
				RequestExample:  ``,
				ResponseExample: `{"baseUrl":"https://infer.example.com"}`,
				ErrorCodes:      []*docsv1.ApiDocsErrorCode{},
				Tryable:         true,
			},
		},
	},
}

// catalog returns the curated catalog for the configured version. Only
// v1 is shipped; an unknown version falls back to v1.
func catalog(version string) []*docsv1.ApiDocsCategory {
	if version == "" || version == "v1" {
		return catalogV1
	}
	return catalogV1
}