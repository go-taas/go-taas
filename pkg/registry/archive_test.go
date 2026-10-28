package registry

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeArchive writes a tar (optionally gzipped) holding manifest.json with
// the given repo tags plus a dummy layer.
func writeArchive(t *testing.T, gzipped bool, repoTags ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image.tar")
	if gzipped {
		path += ".gz"
	}
	file, err := os.Create(path) //nolint:gosec // test temp file
	require.NoError(t, err)

	var out io.Writer = file
	var gz *gzip.Writer
	if gzipped {
		gz = gzip.NewWriter(file)
		out = gz
	}
	writer := tar.NewWriter(out)

	manifest := `[{"Config":"config.json","RepoTags":[`
	for i, tag := range repoTags {
		if i > 0 {
			manifest += ","
		}
		manifest += `"` + tag + `"`
	}
	manifest += `],"Layers":["layer.tar"]}]`
	require.NoError(t, writer.WriteHeader(&tar.Header{
		Name: "manifest.json", Mode: 0o600, Size: int64(len(manifest)),
	}))
	_, err = writer.Write([]byte(manifest))
	require.NoError(t, err)
	require.NoError(t, writer.WriteHeader(&tar.Header{
		Name: "layer.tar", Mode: 0o600, Size: 3,
	}))
	_, err = writer.Write([]byte("abc"))
	require.NoError(t, err)

	require.NoError(t, writer.Close())
	if gz != nil {
		require.NoError(t, gz.Close())
	}
	require.NoError(t, file.Close())
	return path
}

func TestReadArchiveReference(t *testing.T) {
	path := writeArchive(t, false, "docker.io/library/vllm:v0.6.3")
	got, err := ReadArchiveReference(path)
	require.NoError(t, err)
	assert.Equal(t, "docker.io/library/vllm:v0.6.3", got)
}

func TestReadArchiveReferenceGzipped(t *testing.T) {
	path := writeArchive(t, true, "hub.example.com/taas/sglang:2024.11")
	got, err := ReadArchiveReference(path)
	require.NoError(t, err)
	assert.Equal(t, "hub.example.com/taas/sglang:2024.11", got)
}

func TestReadArchiveReferenceSkipsUntaggedImages(t *testing.T) {
	// `docker build -o` produces an archive whose image was never tagged.
	path := writeArchive(t, false, "<none>:<none>")
	got, err := ReadArchiveReference(path)
	require.NoError(t, err)
	assert.Empty(t, got, "an untagged archive has no reference; the form supplies it")
}

func TestReadArchiveReferenceWithoutManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.tar")
	file, err := os.Create(path) //nolint:gosec // test temp file
	require.NoError(t, err)
	writer := tar.NewWriter(file)
	require.NoError(t, writer.WriteHeader(&tar.Header{Name: "other.txt", Mode: 0o600, Size: 1}))
	_, err = writer.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())

	got, err := ReadArchiveReference(path)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestReadArchiveReferenceMissingFile(t *testing.T) {
	_, err := ReadArchiveReference(filepath.Join(t.TempDir(), "absent.tar"))
	require.Error(t, err)
}

func TestReadArchiveReferenceNotAnArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.tar")
	require.NoError(t, os.WriteFile(path, []byte("not a tar"), 0o600))
	_, err := ReadArchiveReference(path)
	require.Error(t, err)
}

func TestImportArchiveBuildsDockerArchiveCopy(t *testing.T) {
	var gotArgs []string
	imp := &SkopeoImporter{Runner: func(_ context.Context, name string, args ...string) ([]byte, error) {
		require.Equal(t, "skopeo", name)
		gotArgs = args
		return []byte("ok"), nil
	}}
	creds := &Credentials{Username: "admin", Password: "secret"}
	require.NoError(t, imp.ImportArchive(context.Background(), "/tmp/x.tar", "docker.io/library/vllm:v0.6.3",
		"hub.example.com/taas/vllm:v0.6.3", creds))

	joined := strings.Join(gotArgs, " ")
	assert.Contains(t, joined, "copy")
	assert.Contains(t, joined, "docker-archive:/tmp/x.tar:docker.io/library/vllm:v0.6.3")
	assert.Contains(t, joined, "docker://hub.example.com/taas/vllm:v0.6.3")
	assert.Contains(t, joined, "--dest-creds admin:secret")
}

func TestImportArchiveWithoutReference(t *testing.T) {
	var gotArgs []string
	imp := &SkopeoImporter{Runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return nil, nil
	}}
	require.NoError(t, imp.ImportArchive(context.Background(), "/tmp/x.tar", "", "hub.example.com/taas/x:1", nil))
	joined := strings.Join(gotArgs, " ")
	assert.Contains(t, joined, "docker-archive:/tmp/x.tar ")
	assert.NotContains(t, joined, "--dest-creds")
}

func TestImportArchiveReportsFailure(t *testing.T) {
	imp := &SkopeoImporter{Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("manifest unknown"), assert.AnError
	}}
	err := imp.ImportArchive(context.Background(), "/tmp/x.tar", "", "hub.example.com/taas/x:1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest unknown")
}
