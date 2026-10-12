package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProbeStatusMapping covers AC4: the exact five-state classification.
func TestProbeStatusMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantStatus string
		wantHTTP   int
	}{
		{"ok", http.StatusOK, ProbeOK, 200},
		{"unauthorized 401", http.StatusUnauthorized, ProbeUnauthorized, 401},
		{"unauthorized 403", http.StatusForbidden, ProbeUnauthorized, 403},
		{"error 404", http.StatusNotFound, ProbeError, 404},
		{"error 500", http.StatusInternalServerError, ProbeError, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/models", r.URL.Path)
				assert.Equal(t, "Bearer secret-key", r.Header.Get("Authorization"))
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			res := NewProbeClient().Probe(context.Background(), srv.URL, "secret-key", nil, 5)
			assert.Equal(t, tc.wantStatus, res.Status)
			assert.Equal(t, tc.wantHTTP, res.HTTPStatus)
			assert.NotContains(t, res.Message, "secret-key")
		})
	}
}

// TestProbeDeepSeekStyleUnauthorizedIsNotUnreachable asserts the core AC4
// distinction: a 401 must be unauthorized, never unreachable.
func TestProbeDeepSeekStyleUnauthorizedIsNotUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	res := NewProbeClient().Probe(context.Background(), srv.URL+"/", "placeholder", nil, 5)
	require.Equal(t, ProbeUnauthorized, res.Status)
	require.Equal(t, 401, res.HTTPStatus)
	require.NotEqual(t, ProbeUnreachable, res.Status)
}

// TestProbeTransportFailuresAreUnreachable asserts that DNS/connect
// failures map to unreachable with http_status 0.
func TestProbeTransportFailuresAreUnreachable(t *testing.T) {
	// A closed port on localhost gives connection refused.
	res := NewProbeClient().Probe(context.Background(), "http://127.0.0.1:1", "k", nil, 2)
	assert.Equal(t, ProbeUnreachable, res.Status)
	assert.Equal(t, 0, res.HTTPStatus)

	// A nonexistent host gives a DNS failure.
	res = NewProbeClient().Probe(context.Background(), "http://no-such-host.invalid", "k", nil, 2)
	assert.Equal(t, ProbeUnreachable, res.Status)
	assert.Equal(t, 0, res.HTTPStatus)
}

// TestProbeTimeoutIsUnreachable asserts a timeout maps to unreachable.
func TestProbeTimeoutIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	start := time.Now()
	res := NewProbeClient().Probe(context.Background(), srv.URL, "k", nil, 1)
	require.Equal(t, ProbeUnreachable, res.Status)
	require.Less(t, time.Since(start), 1400*time.Millisecond, "probe must respect the timeout")
}

// TestProbeSendsExtraHeaders asserts the extra headers are sent.
func TestProbeSendsExtraHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "tenant-a", r.Header.Get("X-Tenant"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	res := NewProbeClient().Probe(context.Background(), srv.URL, "k", map[string]string{"X-Tenant": "tenant-a"}, 5)
	assert.Equal(t, ProbeOK, res.Status)
}
