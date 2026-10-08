package evaluation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateChecks(t *testing.T) {
	tests := []struct {
		name   string
		output string
		checks []Check
		passed int
		all    bool
	}{
		{
			name:   "exact contains regex and json pass",
			output: `{"answer":"hello world"}`,
			checks: []Check{
				{Type: CheckExact, Expected: `{"answer":"hello world"}`},
				{Type: CheckContains, Expected: `"answer"`},
				{Type: CheckRegex, Expected: `hello\s+world`},
				{Type: CheckValidJSON},
			},
			passed: 4,
			all:    true,
		},
		{
			name:   "failed checks are reported individually",
			output: "hello",
			checks: []Check{
				{Type: CheckContains, Expected: "hello"},
				{Type: CheckExact, Expected: "world"},
				{Type: CheckValidJSON},
			},
			passed: 1,
			all:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, all := EvaluateChecks(tt.output, tt.checks)
			require.Equal(t, tt.all, all)
			require.Len(t, results, len(tt.checks))
			passed := 0
			for _, result := range results {
				if result.Passed {
					passed++
				}
			}
			require.Equal(t, tt.passed, passed)
		})
	}
}

func TestValidateChecksRejectsInvalidRegex(t *testing.T) {
	err := ValidateChecks([]Check{{Type: CheckRegex, Expected: "("}})
	require.Error(t, err)
}
