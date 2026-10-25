package infer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// staticGatewayResolver is an injected GatewayResolver that answers with a
// fixed base URL.
type staticGatewayResolver struct{ url string }

func (s staticGatewayResolver) InferenceGatewayURL(context.Context) string { return s.url }

// playgroundGateway starts a fake serving gateway and returns the
// resolver that points at it plus a channel receiving what it was sent.
func playgroundGateway(t *testing.T, handler http.HandlerFunc) (GatewayResolver, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return staticGatewayResolver{url: srv.URL}, srv
}

// openAIChatResponse is a minimal OpenAI-compatible completion.
func openAIChatResponse(completion string) []byte {
	body, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": completion}}},
		"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 5},
	})
	return body
}

// TestPlaygroundModelForwardsThroughTheGateway covers the self-hosted
// playground path: the caller's key is sent to the serving gateway as a
// bearer token, the served model name is used (not the catalog id), and the
// completion and usage are returned.
func TestPlaygroundModelForwardsThroughTheGateway(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), &InferenceService{
		ID: NewServiceID(), OrganizationID: "org-a", Name: "svc",
		ModelID: modelID, ModelVersion: "v1", State: StateRunning,
		Endpoints: []string{"http://e"}, UpdatedAt: time.Now(),
	}))

	type captured struct {
		auth string
		body map[string]any
		path string
	}
	got := make(chan captured, 1)
	resolver, _ := playgroundGateway(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got <- captured{auth: r.Header.Get("Authorization"), body: body, path: r.URL.Path}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openAIChatResponse("hi there"))
	})
	svc.SetGatewayResolver(resolver)

	resp, err := svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello", MaxTokens: 16, ApiKey: "sk-test-key",
	})
	require.NoError(t, err)
	assert.Equal(t, "hi there", resp.GetCompletion())
	assert.EqualValues(t, 7, resp.GetPromptTokens())
	assert.EqualValues(t, 5, resp.GetCompletionTokens())
	assert.GreaterOrEqual(t, resp.GetLatencyMs(), int64(0))

	sent := <-got
	assert.Equal(t, "/v1/chat/completions", sent.path)
	assert.Equal(t, "Bearer sk-test-key", sent.auth)
	// The gateway routes on the served model name, so the catalog id would
	// be rejected.
	assert.Equal(t, "qwen-3b", sent.body["model"])
	messages, ok := sent.body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	assert.Equal(t, "hello", messages[0].(map[string]any)["content"])
}

// TestPlaygroundModelRequiresTheKey keeps the failure actionable: keys are
// stored hashed, so the server cannot invent one.
func TestPlaygroundModelRequiresTheKey(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), &InferenceService{
		ID: NewServiceID(), OrganizationID: "org-a", Name: "svc",
		ModelID: modelID, ModelVersion: "v1", State: StateRunning,
		Endpoints: []string{"http://e"}, UpdatedAt: time.Now(),
	}))
	svc.SetGatewayResolver(staticGatewayResolver{url: "http://127.0.0.1:1"})

	_, err = svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello",
	})
	assert.EqualValues(t, apierrors.CodePlaygroundKeyMissing, apierrors.CodeOf(err))
	assert.Contains(t, err.Error(), "cannot be read back")
}

// TestPlaygroundModelRelaysGatewayErrors surfaces the gateway's own message,
// which is the actionable part ("model x does not exist", a rate limit).
func TestPlaygroundModelRelaysGatewayErrors(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), &InferenceService{
		ID: NewServiceID(), OrganizationID: "org-a", Name: "svc",
		ModelID: modelID, ModelVersion: "v1", State: StateRunning,
		Endpoints: []string{"http://e"}, UpdatedAt: time.Now(),
	}))
	resolver, _ := playgroundGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	})
	svc.SetGatewayResolver(resolver)

	_, err = svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello", ApiKey: "sk-wrong",
	})
	assert.EqualValues(t, apierrors.CodePlaygroundRequestFailed, apierrors.CodeOf(err))
	assert.Contains(t, err.Error(), "invalid api key")
	assert.NotContains(t, err.Error(), "sk-wrong", "the key must never appear in an error")
}

// TestPlaygroundModelWithoutGatewayFails keeps a misconfigured cluster
// explicit instead of returning an empty completion.
func TestPlaygroundModelWithoutGatewayFails(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), &InferenceService{
		ID: NewServiceID(), OrganizationID: "org-a", Name: "svc",
		ModelID: modelID, ModelVersion: "v1", State: StateRunning,
		Endpoints: []string{"http://e"}, UpdatedAt: time.Now(),
	}))
	svc.SetGatewayResolver(staticGatewayResolver{url: ""})

	_, err = svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello", ApiKey: "sk-test",
	})
	assert.EqualValues(t, apierrors.CodeInferEndpointNotFound, apierrors.CodeOf(err))
}

func TestGatewayErrorText(t *testing.T) {
	assert.Equal(t, "boom", gatewayErrorText([]byte(`{"error":{"message":"boom"}}`)))
	assert.Equal(t, "flat", gatewayErrorText([]byte(`{"message":"flat"}`)))
	assert.Equal(t, "plain text", gatewayErrorText([]byte("plain text")))
	assert.Equal(t, "no response body", gatewayErrorText(nil))
	// An unparseable, oversized body is bounded so the error stays readable.
	long := make([]byte, 900)
	for i := range long {
		long[i] = 'x'
	}
	assert.LessOrEqual(t, len(gatewayErrorText(long)), 520)
}

// TestPlaygroundModelEnforcesTheAccessPolicy proves the policy is not just
// stored: the console's own inference path refuses a caller the policy does
// not admit.
func TestPlaygroundModelEnforcesTheAccessPolicy(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")
	repo, err := svc.repository()
	require.NoError(t, err)
	row := &InferenceService{
		ID: NewServiceID(), OrganizationID: "org-a", Name: "svc",
		ModelID: modelID, ModelVersion: "v1", State: StateRunning,
		Endpoints: []string{"http://e"}, UpdatedAt: time.Now(),
	}
	require.NoError(t, repo.Create(context.Background(), row))

	resolver, _ := playgroundGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(openAIChatResponse("ok"))
	})
	svc.SetGatewayResolver(resolver)

	// Open by default: the call goes through.
	_, err = svc.PlaygroundModel(orgContext("org-b"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello", ApiKey: "sk-test",
	})
	require.NoError(t, err)

	// Restricted to org-a: org-b is refused, org-a still works.
	svc.SetPlatformAdminResolver(fakePlatformAdmin{admin: true})
	_, err = svc.SetInferenceServiceAccess(context.Background(), &inferv1.SetInferenceServiceAccessRequest{
		ServiceId:  row.ID,
		Restricted: true,
		Subjects:   []*inferv1.InferenceServiceAccessSubject{{SubjectType: AccessSubjectOrg, SubjectId: "org-a"}},
	})
	require.NoError(t, err)

	_, err = svc.PlaygroundModel(orgContext("org-b"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello", ApiKey: "sk-test",
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
	assert.Contains(t, err.Error(), "restricted")

	resp, err := svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: modelID, Prompt: "hello", ApiKey: "sk-test",
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.GetCompletion())
}
