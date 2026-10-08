package batch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func validJSONL() []byte {
	return []byte(
		`{"custom_id":"a","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}}` + "\n" +
			`{"custom_id":"b","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o","messages":[{"role":"user","content":"yo"}]}}` + "\n",
	)
}

func TestValidateJSONLValid(t *testing.T) {
	requests, model, err := ValidateJSONL(validJSONL(), 1<<20, 100)
	require.NoError(t, err)
	assert.Len(t, requests, 2)
	assert.Equal(t, "gpt-4o", model)
	assert.Equal(t, "a", requests[0].CustomID)
}

func TestValidateJSONLMalformed(t *testing.T) {
	_, _, err := ValidateJSONL([]byte(`{"custom_id":`), 1<<20, 100)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchInputInvalid, apierrors.CodeOf(err))
}

func TestValidateJSONLTooLarge(t *testing.T) {
	_, _, err := ValidateJSONL(validJSONL(), 10, 100)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchInputTooLarge, apierrors.CodeOf(err))
}

func TestValidateJSONLTooManyLines(t *testing.T) {
	_, _, err := ValidateJSONL(validJSONL(), 1<<20, 1)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchInputTooLarge, apierrors.CodeOf(err))
}

func TestValidateJSONLModelMismatch(t *testing.T) {
	content := []byte(
		`{"custom_id":"a","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o"}}` + "\n" +
			`{"custom_id":"b","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-3.5"}}` + "\n",
	)
	_, _, err := ValidateJSONL(content, 1<<20, 100)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchModelMismatch, apierrors.CodeOf(err))
}

func TestValidateJSONLDuplicateCustomID(t *testing.T) {
	content := []byte(
		`{"custom_id":"a","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o"}}` + "\n" +
			`{"custom_id":"a","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o"}}` + "\n",
	)
	_, _, err := ValidateJSONL(content, 1<<20, 100)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchInputInvalid, apierrors.CodeOf(err))
}

func TestValidateJSONLBadMethod(t *testing.T) {
	content := []byte(`{"custom_id":"a","method":"GET","url":"/v1/chat/completions","body":{"model":"gpt-4o"}}` + "\n")
	_, _, err := ValidateJSONL(content, 1<<20, 100)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchInputInvalid, apierrors.CodeOf(err))
}

func TestValidateCompletionWindow(t *testing.T) {
	w, err := validateCompletionWindow("")
	assert.NoError(t, err)
	assert.Equal(t, "24h", w)

	w, err = validateCompletionWindow("7d")
	assert.NoError(t, err)
	assert.Equal(t, "7d", w)

	_, err = validateCompletionWindow("99h")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeBatchInputInvalid, apierrors.CodeOf(err))
}
