package modelhub

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServer builds an httptest server that serves a fake hub: a
// file-listing endpoint and per-file download endpoints. It returns the
// server and the set of files it serves.
func newTestServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// ModelScope-style listing.
	mux.HandleFunc("/api/v1/models/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Data":{"Files":[{"Path":"config.json"},{"Path":"model.safetensors"}]}}`))
	})
	// HuggingFace-style listing.
	mux.HandleFunc("/api/models/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"siblings":[{"rfilename":"config.json"},{"rfilename":"model.safetensors"}]}`))
	})
	// Download endpoints: /models/{id}/resolve/master/{file} (ModelScope)
	// and /{id}/resolve/main/{file} (HuggingFace).
	mux.HandleFunc("/models/", func(w http.ResponseWriter, r *http.Request) {
		serveDownload(w, r, files)
	})
	// HuggingFace download: /{owner}/{name}/resolve/main/{file}. Match
	// any path containing "/resolve/main/".
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !containsResolveMain(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		serveDownload(w, r, files)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func containsResolveMain(p string) bool {
	for i := 0; i+len("/resolve/main/") <= len(p); i++ {
		if p[i:i+len("/resolve/main/")] == "/resolve/main/" {
			return true
		}
	}
	return false
}

func serveDownload(w http.ResponseWriter, r *http.Request, files map[string]string) {
	// The file name is the last path segment.
	name := filepath.Base(r.URL.Path)
	content, ok := files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(content))
}

func TestParseSource(t *testing.T) {
	cases := []struct {
		in      string
		want    Source
		wantErr bool
	}{
		{"modelscope", SourceModelScope, false},
		{"MODELSCOPE", SourceModelScope, false},
		{"huggingface", SourceHuggingFace, false},
		{"HuggingFace", SourceHuggingFace, false},
		{"", "", true},
		{"unknown", "", true},
	}
	for _, c := range cases {
		got, err := ParseSource(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("ParseSource(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseSource(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseSource(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDownloadModelScope(t *testing.T) {
	files := map[string]string{
		"config.json":       `{"model_type":"qwen"}`,
		"model.safetensors": "weights-bytes",
	}
	srv := newTestServer(t, files)
	d := NewHTTPDownloader(Options{BaseURL: srv.URL})

	dest := t.TempDir()
	err := d.Download(context.Background(), SourceModelScope, "Qwen/Qwen2.5-0.5B", dest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestDownloadHuggingFace(t *testing.T) {
	files := map[string]string{
		"config.json":       `{"model_type":"qwen"}`,
		"model.safetensors": "weights-bytes",
	}
	srv := newTestServer(t, files)
	d := NewHTTPDownloader(Options{BaseURL: srv.URL})

	dest := t.TempDir()
	err := d.Download(context.Background(), SourceHuggingFace, "Qwen/Qwen2.5-0.5B", dest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestDownloadNestedFiles(t *testing.T) {
	// A nested file path must be preserved under destDir.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/models/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Data":{"Files":[{"Path":"sub/dir/tokenizer.json"}]}}`))
	})
	mux.HandleFunc("/models/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tokenizer"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	d := NewHTTPDownloader(Options{BaseURL: srv.URL})
	dest := t.TempDir()
	if err := d.Download(context.Background(), SourceModelScope, "org/model", dest); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "sub", "dir", "tokenizer.json"))
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if string(got) != "tokenizer" {
		t.Fatalf("nested file = %q", got)
	}
}

func TestDownloadUnsupportedSource(t *testing.T) {
	d := NewHTTPDownloader(Options{})
	err := d.Download(context.Background(), Source("nope"), "org/model", t.TempDir())
	if err == nil {
		t.Fatal("expected error for unsupported source")
	}
}

func TestDownloadListFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	d := NewHTTPDownloader(Options{BaseURL: srv.URL})
	if err := d.Download(context.Background(), SourceModelScope, "org/model", t.TempDir()); err == nil {
		t.Fatal("expected error on list failure")
	}
}

func TestCleanModelID(t *testing.T) {
	if got := CleanModelID("Qwen/Qwen2.5-0.5B"); got != "Qwen__Qwen2.5-0.5B" {
		t.Fatalf("CleanModelID = %q", got)
	}
	if got := JoinPath("/data/weights", "Qwen/Qwen2.5-0.5B"); got != "/data/weights/Qwen__Qwen2.5-0.5B" {
		t.Fatalf("JoinPath = %q", got)
	}
}

// TestParseModelScopeFileSizes verifies the ModelScope parser keeps the
// per-file size, which is what lets a download report a real percentage.
func TestParseModelScopeFileSizes(t *testing.T) {
	body := []byte(`{"Data":{"Files":[
		{"Path":"config.json","Size":100},
		{"Path":"model.safetensors","Size":940000000}
	]}}`)
	files, err := parseModelScopeFiles(body)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "config.json", files[0].Path)
	assert.EqualValues(t, 100, files[0].Size)
	assert.EqualValues(t, 940000000, files[1].Size)
}

// TestParseHuggingFaceFileSizes verifies the HuggingFace parser prefers the
// LFS size (the real weight size) and falls back to the plain size.
func TestParseHuggingFaceFileSizes(t *testing.T) {
	body := []byte(`{"siblings":[
		{"rfilename":"config.json","size":100},
		{"rfilename":"model.safetensors","size":10,"lfs":{"size":940000000}}
	]}`)
	files, err := parseHuggingFaceFiles(body)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.EqualValues(t, 100, files[0].Size)
	assert.EqualValues(t, 940000000, files[1].Size)
}

// TestHTTPDownloaderTotalBytes verifies TotalBytes sums the file sizes the
// hub reports.
func TestHTTPDownloaderTotalBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Data":{"Files":[{"Path":"a","Size":5},{"Path":"b","Size":7}]}}`))
	}))
	defer srv.Close()

	d := NewHTTPDownloader(Options{BaseURL: srv.URL})
	total, err := d.TotalBytes(context.Background(), SourceModelScope, "Qwen/Qwen2.5-0.5B")
	require.NoError(t, err)
	assert.EqualValues(t, 12, total)
}

// TestDownloadPreservesModelIDSlashes guards against escaping the slash in
// a nested model ID. ModelScope and HuggingFace resolve endpoints treat
// "org%2Fmodel" as a different (missing) repository, so the resolved
// download URL must keep the separator.
func TestDownloadPreservesModelIDSlashes(t *testing.T) {
	for _, tc := range []struct {
		source Source
		prefix string
	}{
		{SourceModelScope, "/models/org/model/resolve/master/"},
		{SourceHuggingFace, "/org/model/resolve/main/"},
	} {
		t.Run(string(tc.source), func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := r.URL.EscapedPath()
				if strings.Contains(p, "/api/") {
					if tc.source == SourceModelScope {
						_, _ = w.Write([]byte(`{"Data":{"Files":[{"Path":"config.json"}]}}`))
						return
					}
					_, _ = w.Write([]byte(`{"siblings":[{"rfilename":"config.json"}]}`))
					return
				}
				gotPath = p
				if strings.Contains(p, "%2F") {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)

			d := NewHTTPDownloader(Options{BaseURL: srv.URL})
			err := d.Download(context.Background(), tc.source, "org/model", t.TempDir())
			require.NoError(t, err)
			assert.Contains(t, gotPath, tc.prefix)
			assert.NotContains(t, gotPath, "%2F")
		})
	}
}

// shortDelay makes retries instant in the tests that exercise them.
func shortDelay(t *testing.T) {
	t.Helper()
	old := downloadRetryDelay
	downloadRetryDelay = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { downloadRetryDelay = old })
}

// truncatedResponse announces the full Content-Length, writes only a
// prefix and then drops the connection, which is what a CDN cutting a
// large transfer looks like to the client (io.ErrUnexpectedEOF).
func truncatedResponse(w http.ResponseWriter, payload []byte, prefix int) {
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	_, _ = w.Write(payload[:prefix])
	w.(http.Flusher).Flush()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}

// TestDownloadResumesTruncatedStream guards the retry-and-resume path: the
// second request must continue at the byte the first one reached instead
// of restarting a multi-gigabyte file from zero.
func TestDownloadResumesTruncatedStream(t *testing.T) {
	shortDelay(t)
	payload := bytes.Repeat([]byte("0123456789"), 1000) // 10 000 bytes
	var mu sync.Mutex
	requests := 0
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			_, _ = fmt.Fprintf(w, `{"Data":{"Files":[{"Path":"model.safetensors","Size":%d}]}}`, len(payload))
			return
		}
		mu.Lock()
		requests++
		attempt := requests
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		if attempt == 1 {
			truncatedResponse(w, payload, len(payload)/2)
			return
		}
		start := 0
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &start); err != nil {
			start = 0
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)-start))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start:])
	}))
	t.Cleanup(srv.Close)

	d := NewHTTPDownloader(Options{BaseURL: srv.URL})
	dest := t.TempDir()
	require.NoError(t, d.Download(context.Background(), SourceModelScope, "org/model", dest))
	got, err := os.ReadFile(filepath.Join(dest, "model.safetensors"))
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.Equal(t, 2, requests, "one truncated attempt plus one resumed attempt")
	assert.Equal(t, []string{"", "bytes=5000-"}, ranges)
}

// TestDownloadRestartsWhenHubIgnoresRange covers a hub that answers a Range
// request with the whole file (200): the partial prefix must be dropped so
// bytes are not written twice.
func TestDownloadRestartsWhenHubIgnoresRange(t *testing.T) {
	shortDelay(t)
	payload := bytes.Repeat([]byte("abcdefghij"), 1000)
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			_, _ = fmt.Fprintf(w, `{"Data":{"Files":[{"Path":"model.safetensors","Size":%d}]}}`, len(payload))
			return
		}
		mu.Lock()
		requests++
		attempt := requests
		mu.Unlock()
		if attempt == 1 {
			truncatedResponse(w, payload, len(payload)/2)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	d := NewHTTPDownloader(Options{BaseURL: srv.URL})
	dest := t.TempDir()
	require.NoError(t, d.Download(context.Background(), SourceModelScope, "org/model", dest))
	got, err := os.ReadFile(filepath.Join(dest, "model.safetensors"))
	require.NoError(t, err)
	assert.Equal(t, payload, got, "an ignored Range must not duplicate the prefix")
	assert.Equal(t, 2, requests)
}

// TestDownloadFailsAfterRetriesWhenStreamTruncates verifies a connection
// that always breaks mid-file fails after the bounded number of attempts
// instead of retrying forever.
func TestDownloadFailsAfterRetriesWhenStreamTruncates(t *testing.T) {
	shortDelay(t)
	oldAttempts := downloadAttempts
	downloadAttempts = 2
	t.Cleanup(func() { downloadAttempts = oldAttempts })

	payload := bytes.Repeat([]byte("xyz"), 1000)
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			_, _ = fmt.Fprintf(w, `{"Data":{"Files":[{"Path":"model.safetensors","Size":%d}]}}`, len(payload))
			return
		}
		mu.Lock()
		requests++
		mu.Unlock()
		truncatedResponse(w, payload, len(payload)/2)
	}))
	t.Cleanup(srv.Close)

	d := NewHTTPDownloader(Options{BaseURL: srv.URL})
	err := d.Download(context.Background(), SourceModelScope, "org/model", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected EOF")
	assert.Equal(t, 2, requests)
}

// TestDownloadDoesNotRetryForbidden guards against retrying a final status:
// a 403 will not change, so the download must fail on the first response.
func TestDownloadDoesNotRetryForbidden(t *testing.T) {
	shortDelay(t)
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			_, _ = w.Write([]byte(`{"Data":{"Files":[{"Path":"model.safetensors","Size":10}]}}`))
			return
		}
		mu.Lock()
		requests++
		mu.Unlock()
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	d := NewHTTPDownloader(Options{BaseURL: srv.URL})
	err := d.Download(context.Background(), SourceModelScope, "org/model", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
	assert.Equal(t, 1, requests)
}
