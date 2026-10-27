package modelhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
