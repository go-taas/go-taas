// Package evaluation implements tenant-scoped prompt evaluation and scoring.
//
//nolint:revive // Scoring helpers are an internal service API exercised by FVT.
package evaluation

//revive:disable:exported

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	CheckExact     = "exact"      //nolint:revive // CheckExact compares the full completion.
	CheckContains  = "contains"   //nolint:revive // CheckContains checks for a substring.
	CheckRegex     = "regex"      //nolint:revive // CheckRegex applies a Go RE2 expression.
	CheckValidJSON = "valid_json" //nolint:revive // CheckValidJSON requires one valid JSON value.
)

// Check describes one deterministic assertion against a completion.
type Check struct {
	Type     string `json:"type"`
	Expected string `json:"expected,omitempty"`
}

// CheckResult is the immutable result of evaluating one assertion.
type CheckResult struct {
	Type     string `json:"type"`
	Expected string `json:"expected,omitempty"`
	Passed   bool   `json:"passed"`
	Message  string `json:"message,omitempty"`
}

// ValidateChecks validates supported check types and their expected values.
func ValidateChecks(checks []Check) error {
	if len(checks) == 0 {
		return fmt.Errorf("at least one check is required")
	}
	for _, check := range checks {
		switch check.Type {
		case CheckExact, CheckContains:
			if strings.TrimSpace(check.Expected) == "" {
				return fmt.Errorf("check %q requires an expected value", check.Type)
			}
		case CheckRegex:
			if strings.TrimSpace(check.Expected) == "" {
				return fmt.Errorf("regex check requires an expected value")
			}
			if _, err := regexp.Compile(check.Expected); err != nil {
				return fmt.Errorf("invalid regex: %w", err)
			}
		case CheckValidJSON:
		default:
			return fmt.Errorf("unsupported check type %q", check.Type)
		}
	}
	return nil
}

// EvaluateChecks evaluates every check and reports whether all passed.
func EvaluateChecks(output string, checks []Check) ([]CheckResult, bool) {
	results := make([]CheckResult, 0, len(checks))
	allPassed := len(checks) > 0
	for _, check := range checks {
		result := CheckResult{Type: check.Type, Expected: check.Expected}
		switch check.Type {
		case CheckExact:
			result.Passed = output == check.Expected
		case CheckContains:
			result.Passed = strings.Contains(output, check.Expected)
		case CheckRegex:
			compiled, err := regexp.Compile(check.Expected)
			result.Passed = err == nil && compiled.MatchString(output)
		case CheckValidJSON:
			var value any
			result.Passed = json.Unmarshal([]byte(output), &value) == nil
		}
		if !result.Passed {
			allPassed = false
		}
		results = append(results, result)
	}
	return results, allPassed
}
