package infer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/model"
)

// fakeExternalProxy records the external proxy call (feature #48, AD14).
type fakeExternalProxy struct {
	resp       []byte
	err        error
	calls      int
	gotModelID string
	gotOrg     string
	gotKeyID   string
	gotBody    []byte
}

func (f *fakeExternalProxy) ProxyExternalChat(_ context.Context, modelID, orgID, apiKeyID string, body []byte) ([]byte, error) {
	f.calls++
	f.gotModelID, f.gotOrg, f.gotKeyID, f.gotBody = modelID, orgID, apiKeyID, body
	return f.resp, f.err
}

// TestPlaygroundModelExternal covers AC9/AC10 at the unit level: the
// external branch delegates to the injected proxy, maps the OpenAI response
// and passes the catalog model id, org and api key id through.
func TestPlaygroundModelExternal(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)

	id := uuid.NewString()
	require.NoError(t, db.Create(&model.Model{
		ID: id, Name: "ext-model", External: true, Published: true,
		ProviderID: "prov-1", UpstreamModelID: "gpt-4o",
	}).Error)

	proxy := &fakeExternalProxy{resp: []byte(`{"choices":[{"message":{"content":"hi there"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)}
	svc.SetExternalModelProxy(proxy)

	resp, err := svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{
		ModelId: id, ApiKeyId: "key-1", Prompt: "hello", Temperature: 0.5, MaxTokens: 32,
	})
	require.NoError(t, err)
	assert.Equal(t, "hi there", resp.GetCompletion())
	assert.EqualValues(t, 3, resp.GetPromptTokens())
	assert.EqualValues(t, 2, resp.GetCompletionTokens())
	assert.Equal(t, id, proxy.gotModelID)
	assert.Equal(t, "org-a", proxy.gotOrg)
	assert.Equal(t, "key-1", proxy.gotKeyID)

	var body map[string]any
	require.NoError(t, json.Unmarshal(proxy.gotBody, &body))
	messages, _ := body["messages"].([]any)
	require.Len(t, messages, 1)
	assert.Equal(t, "hello", messages[0].(map[string]any)["content"])

	// A proxy failure passes through unchanged (the resolver's fail-closed
	// code).
	proxy.err = apierrors.New(apierrors.CodeProviderDisabled)
	_, err = svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{ModelId: id, Prompt: "hello"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderDisabled, apierrors.CodeOf(err))
}

// TestPlaygroundModelExternalUnwiredFailsClosed asserts an external model
// with no proxy wired fails closed.
func TestPlaygroundModelExternalUnwiredFailsClosed(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	id := uuid.NewString()
	require.NoError(t, db.Create(&model.Model{
		ID: id, Name: "ext-2", External: true, Published: true, ProviderID: "p",
	}).Error)

	_, err := svc.PlaygroundModel(orgContext("org-a"), &inferv1.PlaygroundModelRequest{ModelId: id, Prompt: "hello"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, apierrors.CodeOf(err))
}

// TestParseChatCompletion covers the best-effort response parsing.
func TestParseChatCompletion(t *testing.T) {
	completion, pt, ct := parseChatCompletion([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":7}}`))
	assert.Equal(t, "ok", completion)
	assert.EqualValues(t, 5, pt)
	assert.EqualValues(t, 7, ct)

	completion, pt, ct = parseChatCompletion([]byte("not-json"))
	assert.Empty(t, completion)
	assert.Zero(t, pt)
	assert.Zero(t, ct)
}
