package batch

import (
	"bytes"
	"encoding/json"
	"strings"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// RequestLine is one parsed JSONL input line (AD2, §5.2).
type RequestLine struct {
	CustomID string          `json:"custom_id"`
	Method   string          `json:"method"`
	URL      string          `json:"url"`
	Body     json.RawMessage `json:"body"`
}

// maxCustomIDLen bounds the custom_id length (§5.2).
const maxCustomIDLen = 256

// validBatchURLs is the set of accepted request URLs (§5.2).
var validBatchURLs = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/embeddings":       true,
}

// ValidateJSONL parses and validates a JSONL input file. It returns the
// parsed request lines, the single model, or a business error (AC1).
func ValidateJSONL(content []byte, maxBytes int64, maxLines int) ([]RequestLine, string, error) {
	if int64(len(content)) > maxBytes {
		return nil, "", apierrors.New(apierrors.CodeBatchInputTooLarge)
	}
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) > maxLines {
		return nil, "", apierrors.New(apierrors.CodeBatchInputTooLarge)
	}
	seen := make(map[string]bool, len(lines))
	model := ""
	var requests []RequestLine
	for _, raw := range lines {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var req RequestLine
		if err := json.Unmarshal(line, &req); err != nil {
			return nil, "", apierrors.New(apierrors.CodeBatchInputInvalid)
		}
		if strings.TrimSpace(req.CustomID) == "" || len(req.CustomID) > maxCustomIDLen {
			return nil, "", apierrors.New(apierrors.CodeBatchInputInvalid)
		}
		if seen[req.CustomID] {
			return nil, "", apierrors.New(apierrors.CodeBatchInputInvalid)
		}
		seen[req.CustomID] = true
		if req.Method != "POST" {
			return nil, "", apierrors.New(apierrors.CodeBatchInputInvalid)
		}
		if !validBatchURLs[req.URL] {
			return nil, "", apierrors.New(apierrors.CodeBatchInputInvalid)
		}
		if len(req.Body) == 0 {
			return nil, "", apierrors.New(apierrors.CodeBatchInputInvalid)
		}
		// Extract the model from the body for single-model consistency.
		var body struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return nil, "", apierrors.New(apierrors.CodeBatchInputInvalid)
		}
		if strings.TrimSpace(body.Model) == "" {
			return nil, "", apierrors.New(apierrors.CodeBatchInputInvalid)
		}
		if model == "" {
			model = body.Model
		} else if body.Model != model {
			return nil, "", apierrors.New(apierrors.CodeBatchModelMismatch)
		}
		requests = append(requests, req)
	}
	if len(requests) == 0 {
		return nil, "", apierrors.New(apierrors.CodeBatchInputInvalid)
	}
	return requests, model, nil
}

// validateCompletionWindow checks the completion window format and range
// (default "24h", range "1h"–"14d", AD2).
func validateCompletionWindow(window string) (string, error) {
	if window == "" {
		return "24h", nil
	}
	// Accept the documented presets.
	switch window {
	case "1h", "6h", "24h", "3d", "7d", "14d":
		return window, nil
	}
	return "", apierrors.Newf(apierrors.CodeBatchInputInvalid, "invalid completion window %q", window)
}

// resultLine is one result-file line (AD6).
type resultLine struct {
	CustomID string          `json:"custom_id"`
	Response json.RawMessage `json:"response"`
}

// errorLine is one error-file line (AD6).
type errorLine struct {
	CustomID string `json:"custom_id"`
	Error    string `json:"error"`
}

// formatResultLine renders a result line.
func formatResultLine(customID string, response json.RawMessage) []byte {
	line, _ := json.Marshal(resultLine{CustomID: customID, Response: response})
	return line
}

// formatErrorLine renders an error line.
func formatErrorLine(customID, errMsg string) []byte {
	line, _ := json.Marshal(errorLine{CustomID: customID, Error: errMsg})
	return line
}
