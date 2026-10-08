package prompt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func TestValidateName(t *testing.T) {
	assert.NoError(t, ValidateName("greeting", 128))
	// Empty -> 12705.
	err := ValidateName("", 128)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidContent, apierrors.CodeOf(err))
	// Too long -> 12705.
	err = ValidateName("abcdefghij", 5)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidContent, apierrors.CodeOf(err))
}

func TestValidateContent(t *testing.T) {
	assert.NoError(t, ValidateContent("hello", 6144))
	// Empty -> 12705.
	err := ValidateContent("", 6144)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidContent, apierrors.CodeOf(err))
	// Too long -> 12705.
	err = ValidateContent("abcdefghij", 5)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidContent, apierrors.CodeOf(err))
}

func TestValidateVariables(t *testing.T) {
	assert.NoError(t, ValidateVariables([]string{"${topic}", "${tone}"}))
	// Malformed -> 12706.
	err := ValidateVariables([]string{"topic"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidVariable, apierrors.CodeOf(err))
	err = ValidateVariables([]string{"${}"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidVariable, apierrors.CodeOf(err))
	err = ValidateVariables([]string{"${a{b}}"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodePromptInvalidVariable, apierrors.CodeOf(err))
}
