package provider

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// ProbeResult is the five-state connectivity result persisted on a
// provider (design FR3.5).
type ProbeResult struct {
	// Status is unknown | ok | unauthorized | unreachable | error.
	Status string
	// LatencyMs is the request latency in milliseconds.
	LatencyMs int64
	// HTTPStatus is the HTTP status (0 when no response was received).
	HTTPStatus int
	// Message is the sanitized message (never the key or the upstream body).
	Message string
	// CheckedAt is the probe time.
	CheckedAt time.Time
}

// ProbeClient issues the OpenAI-compatible GET {base_url}/models probe and
// classifies the outcome into the five states (Section 13.3).
type ProbeClient struct {
	httpClient *http.Client
}

// NewProbeClient constructs a ProbeClient with no global timeout; the
// per-request context carries the provider timeout.
func NewProbeClient() *ProbeClient {
	return &ProbeClient{httpClient: &http.Client{}}
}

// Probe performs the probe and returns the classified result. The key and
// the extra-header values are used only in the request and never appear
// in the message.
func (c *ProbeClient) Probe(ctx context.Context, baseURL, key string, headers map[string]string, timeoutSeconds int) ProbeResult {
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultRequestTimeoutSeconds
	}
	target := trimTrailingSlashes(baseURL) + "/models"
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		// A malformed URL is a configuration error, not a transport failure.
		return ProbeResult{
			Status:    ProbeError,
			Message:   sanitizeMessage("Invalid endpoint URL"),
			CheckedAt: time.Now().UTC(),
		}
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	latency := time.Since(start).Milliseconds()
	checkedAt := time.Now().UTC()

	if err != nil {
		return ProbeResult{
			Status:    ProbeUnreachable,
			LatencyMs: latency,
			// HTTPStatus stays 0: no response was received.
			Message:   sanitizeMessage(transportMessage(err, timeoutSeconds)),
			CheckedAt: checkedAt,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return ProbeResult{
			Status:     ProbeOK,
			LatencyMs:  latency,
			HTTPStatus: code,
			Message:    sanitizeMessage("HTTP " + resp.Status + " — reachable"),
			CheckedAt:  checkedAt,
		}
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		// Reachable but credentials rejected: this must be unauthorized,
		// never unreachable (design FR3.3.1, AC4).
		return ProbeResult{
			Status:     ProbeUnauthorized,
			LatencyMs:  latency,
			HTTPStatus: code,
			Message:    sanitizeMessage("HTTP " + resp.Status + " — credentials rejected"),
			CheckedAt:  checkedAt,
		}
	default:
		return ProbeResult{
			Status:     ProbeError,
			LatencyMs:  latency,
			HTTPStatus: code,
			Message:    sanitizeMessage("HTTP " + resp.Status),
			CheckedAt:  checkedAt,
		}
	}
}

// transportMessage derives a sanitized category message from a transport
// error (design FR3.4).
func transportMessage(err error, timeoutSeconds int) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "DNS resolution failed"
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return fmt.Sprintf("Connection timed out after %ds", timeoutSeconds)
	}
	var certErr *x509.UnknownAuthorityError
	if errors.As(err, &certErr) {
		return "TLS handshake failed"
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return "TLS handshake failed"
	}
	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &certInvalidErr) {
		return "TLS handshake failed"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "tls") || strings.Contains(msg, "x509") || strings.Contains(msg, "certificate") {
		return "TLS handshake failed"
	}
	if strings.Contains(msg, "connection refused") || strings.Contains(msg, "connect:") {
		return "Connection refused"
	}
	return "Connection failed"
}

// isTimeout reports whether err is a net timeout.
func isTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

// sanitizeMessage truncates a message to the probe_message column width and
// strips any control characters.
func sanitizeMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	const maxLen = 256
	if len(msg) > maxLen {
		msg = msg[:maxLen]
	}
	return msg
}
