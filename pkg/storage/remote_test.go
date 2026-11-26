package storage

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestJoinObjectKey covers the weight paths the model service stores: some
// carry a trailing slash, some do not, and a relative path never starts with
// one. A doubled separator becomes part of the object name and object stores
// reject it.
func TestJoinObjectKey(t *testing.T) {
	cases := []struct {
		prefix string
		rel    string
		want   string
	}{
		{"model/version/", "config.json", "model/version/config.json"},
		{"model/version", "config.json", "model/version/config.json"},
		{"/model/version/", "/nested/config.json", "model/version/nested/config.json"},
		{"", "config.json", "config.json"},
		{"model/", "", "model"},
	}
	for _, tc := range cases {
		if got := joinObjectKey(tc.prefix, tc.rel); got != tc.want {
			t.Errorf("joinObjectKey(%q, %q) = %q, want %q", tc.prefix, tc.rel, got, tc.want)
		}
	}
}

// TestCountingReaderReportsAtEOF verifies the upload byte counter reports
// the full size even when a whole object is read inside one throttle
// window, so the final progress sample is not lost.
func TestCountingReaderReportsAtEOF(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 100)
	var reported []int64
	r := &countingReader{
		reader:   bytes.NewReader(payload),
		interval: time.Hour,
		progress: func(n int64) { reported = append(reported, n) },
	}
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.Equal(t, []int64{100}, reported)
}

// TestCountingReaderIsMonotonic verifies every report is the cumulative
// byte count, which is what the console renders as a percentage.
func TestCountingReaderIsMonotonic(t *testing.T) {
	var reported []int64
	r := &countingReader{
		reader:   bytes.NewReader(make([]byte, 10)),
		interval: 0, // report on every read
		progress: func(n int64) { reported = append(reported, n) },
	}
	_, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NotEmpty(t, reported)
	for i := 1; i < len(reported); i++ {
		assert.Greater(t, reported[i], reported[i-1])
	}
	assert.EqualValues(t, 10, reported[len(reported)-1])
}
