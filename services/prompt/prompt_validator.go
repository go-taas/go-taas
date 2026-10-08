package prompt

import (
	"strings"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// ValidateName checks a prompt/folder/template name (AC1).
func ValidateName(name string, maxLen int) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxLen {
		return apierrors.New(apierrors.CodePromptInvalidContent)
	}
	return nil
}

// ValidateContent checks prompt/template content (AC1).
func ValidateContent(content string, maxLen int) error {
	if strings.TrimSpace(content) == "" || len(content) > maxLen {
		return apierrors.New(apierrors.CodePromptInvalidContent)
	}
	return nil
}

// ValidateVariables checks each ${var} reference is well-formed (AC1).
func ValidateVariables(vars []string) error {
	for _, v := range vars {
		if !validVariable(v) {
			return apierrors.New(apierrors.CodePromptInvalidVariable)
		}
	}
	return nil
}

// validVariable reports whether a variable reference is well-formed:
// ${name} with a non-empty name and balanced braces.
func validVariable(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) < 4 || !strings.HasPrefix(v, "${") || !strings.HasSuffix(v, "}") {
		return false
	}
	name := v[2 : len(v)-1]
	if strings.TrimSpace(name) == "" {
		return false
	}
	// No nested braces.
	if strings.Contains(name, "{") || strings.Contains(name, "}") {
		return false
	}
	return true
}
