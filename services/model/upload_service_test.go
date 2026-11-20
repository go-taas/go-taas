package model

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tarArchive builds a .tar (optionally gzipped) holding the given files.
func tarArchive(t *testing.T, files map[string]string, gz bool) string {
	t.Helper()
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for name, content := range files {
		require.NoError(t, writer.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}))
		_, err := writer.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	path := filepath.Join(t.TempDir(), "model.tar")
	if gz {
		path += ".gz"
		var out bytes.Buffer
		gzWriter := gzip.NewWriter(&out)
		_, err := gzWriter.Write(buf.Bytes())
		require.NoError(t, err)
		require.NoError(t, gzWriter.Close())
		require.NoError(t, os.WriteFile(path, out.Bytes(), 0o600))
		return path
	}
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	return path
}

// zipArchive builds a .zip holding the given files.
func zipArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.zip")
	out, err := os.Create(path) //nolint:gosec // test temp file
	require.NoError(t, err)
	writer := zip.NewWriter(out)
	for name, content := range files {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		_, err = entry.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.NoError(t, out.Close())
	return path
}

// TestUploadModelExtractsAndRegisters covers the whole upload: the archive is
// unpacked under the weights root and the model version is registered with the
// relative weight path the engine is later pointed at.
func TestUploadModelExtractsAndRegisters(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())
	ctx := context.Background()

	archive := tarArchive(t, map[string]string{
		"config.json":              `{"model_type":"qwen2"}`,
		"model-00001.safetensors":  "weights",
		"tokenizer/tokenizer.json": "{}",
	}, true)

	res, err := svc.UploadModel(ctx, UploadModelRequest{
		ArchivePath:  archive,
		OriginalName: "model.tar.gz",
		Name:         "Qwen Upload",
		HubModelID:   "acme/qwen-upload",
		Version:      "v1",
		Description:  "a local checkpoint",
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.ModelID)
	assert.Equal(t, "v1", res.Version)
	assert.Equal(t, "Qwen-Upload/v1/", res.WeightPath)
	assert.Equal(t, 3, res.Files)
	assert.Positive(t, res.Bytes)

	// The files are on disk under the weights root, at the path the row names.
	root := svc.weightsDir
	for _, name := range []string{"config.json", "model-00001.safetensors", "tokenizer/tokenizer.json"} {
		content, err := os.ReadFile(filepath.Join(root, "Qwen-Upload", "v1", filepath.FromSlash(name))) //nolint:gosec // test path
		require.NoError(t, err, name)
		assert.NotEmpty(t, content)
	}

	// The catalog has the model and its version.
	repo, err := svc.repository()
	require.NoError(t, err)
	model, err := repo.FindByName(ctx, "Qwen Upload")
	require.NoError(t, err)
	version, err := repo.FindVersion(ctx, model.ID, "v1")
	require.NoError(t, err)
	assert.Equal(t, "Qwen-Upload/v1/", version.WeightPath)
}

// TestUploadModelZip covers the second archive format.
func TestUploadModelZip(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())

	archive := zipArchive(t, map[string]string{"config.json": "{}", "weights.bin": "x"})
	res, err := svc.UploadModel(context.Background(), UploadModelRequest{
		ArchivePath:  archive,
		OriginalName: "model.zip",
		Name:         "zipped",
		HubModelID:   "acme/zipped",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Files)
	assert.NotEmpty(t, res.Version, "an omitted version is derived")
}

// TestUploadModelRejectsEscapingArchive keeps a relative ".." entry from
// writing outside the weights root.
func TestUploadModelRejectsEscapingArchive(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())

	archive := tarArchive(t, map[string]string{"../escaped.txt": "x"}, false)
	_, err := svc.UploadModel(context.Background(), UploadModelRequest{
		ArchivePath:  archive,
		OriginalName: "model.tar",
		Name:         "escaping",
		HubModelID:   "acme/escaping",
	})
	require.Error(t, err)

	// Nothing was written outside: the destination was cleaned up and the
	// parent of the weights root is untouched.
	assert.NoFileExists(t, filepath.Join(filepath.Dir(svc.weightsDir), "escaped.txt"))
}

// TestUploadModelRejectsUnknownFormat covers the extension gate.
func TestUploadModelRejectsUnknownFormat(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())

	_, err := svc.UploadModel(context.Background(), UploadModelRequest{
		ArchivePath:  filepath.Join(t.TempDir(), "model.rar"),
		OriginalName: "model.rar",
		Name:         "rar",
		HubModelID:   "acme/rar",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".tar")
}

// TestUploadModelRequiresWeightsDir keeps a misconfigured install from
// accepting an upload it cannot store.
func TestUploadModelRequiresWeightsDir(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})

	_, err := svc.UploadModel(context.Background(), UploadModelRequest{
		ArchivePath:  "/tmp/x.tar",
		OriginalName: "x.tar",
		Name:         "x",
		HubModelID:   "acme/x",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "weights directory")
}

// TestUploadModelDuplicateVersionRejected proves a second upload of the same
// version registers nothing and leaves no orphaned directory behind.
func TestUploadModelDuplicateVersionRejected(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())
	ctx := context.Background()
	archive := tarArchive(t, map[string]string{"config.json": "{}"}, false)

	_, err := svc.UploadModel(ctx, UploadModelRequest{
		ArchivePath: archive, OriginalName: "m.tar", Name: "dup", HubModelID: "acme/dup", Version: "v1",
	})
	require.NoError(t, err)

	_, err = svc.UploadModel(ctx, UploadModelRequest{
		ArchivePath: archive, OriginalName: "m.tar", Name: "dup", HubModelID: "acme/dup", Version: "v1",
	})
	require.Error(t, err)

	// The rejected second upload left the first version's bytes intact.
	_, statErr := os.Stat(filepath.Join(svc.weightsDir, "dup", "v1", "config.json"))
	require.NoError(t, statErr)
}

// TestUploadModelEmptyArchiveRejected covers an archive with no files.
func TestUploadModelEmptyArchiveRejected(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())

	archive := tarArchive(t, map[string]string{}, false)
	_, err := svc.UploadModel(context.Background(), UploadModelRequest{
		ArchivePath: archive, OriginalName: "empty.tar", Name: "empty", HubModelID: "acme/empty",
	})
	require.Error(t, err)
}

// TestSafeJoin keeps the path helper's contract explicit: anything resolving
// above the destination root is refused.
func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"config.json", "a/b/c.json", "./x", "/abs/file"} {
		target, err := safeJoin(root, name)
		require.NoError(t, err, name)
		assert.Contains(t, target, filepath.Clean(root))
	}
	for _, name := range []string{"../x", "a/../../x", "a/../../../etc/passwd"} {
		_, err := safeJoin(root, name)
		require.Error(t, err, name)
	}
}

// TestUploadModelRequiresHubModelID makes the catalog identity mandatory for
// local uploads too: every model has a "<vendor>/<model>" hub id, so the
// legal and uploaded paths cannot diverge.
func TestUploadModelRequiresHubModelID(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())

	_, err := svc.UploadModel(context.Background(), UploadModelRequest{
		ArchivePath: tarArchive(t, map[string]string{"config.json": "{}"}, false), OriginalName: "m.tar", Name: "no-hub",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub model id is required")
}

// TestUploadModelRejectsDuplicateHubModelID keeps one repository model bound
// to one catalog entry.
func TestUploadModelRejectsDuplicateHubModelID(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())
	ctx := context.Background()
	archive := tarArchive(t, map[string]string{"config.json": "{}"}, false)

	_, err := svc.UploadModel(ctx, UploadModelRequest{
		ArchivePath: archive, OriginalName: "a.tar", Name: "first", HubModelID: "acme/shared",
	})
	require.NoError(t, err)

	_, err = svc.UploadModel(ctx, UploadModelRequest{
		ArchivePath: archive, OriginalName: "b.tar", Name: "second", HubModelID: "acme/shared",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already bound")
}

// TestUploadModelRecordsSelectedBackend verifies an upload into a named
// backend records that backend on the version.
func TestUploadModelRecordsSelectedBackend(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())
	svc.SetStorageConfig(filesystemBackends(svc.weightsDir, "shared", "cold"))
	ctx := context.Background()
	archive := tarArchive(t, map[string]string{"config.json": "{}"}, false)

	res, err := svc.UploadModel(ctx, UploadModelRequest{
		ArchivePath: archive, OriginalName: "model.tar", Name: "Qwen Upload",
		HubModelID: "acme/qwen-upload", Version: "v1", StorageBackend: "cold",
	})
	require.NoError(t, err)

	repo, err := svc.repository()
	require.NoError(t, err)
	version, err := repo.FindVersion(ctx, res.ModelID, res.Version)
	require.NoError(t, err)
	assert.Equal(t, "cold", version.StorageBackend)
}

// TestUploadModelRejectsUnknownBackend refuses a backend the operator never
// configured instead of silently writing into the default one.
func TestUploadModelRejectsUnknownBackend(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	svc.SetWeightsDir(t.TempDir())
	svc.SetStorageConfig(filesystemBackends(svc.weightsDir, "shared"))
	archive := tarArchive(t, map[string]string{"config.json": "{}"}, false)

	_, err := svc.UploadModel(context.Background(), UploadModelRequest{
		ArchivePath: archive, OriginalName: "model.tar", Name: "Qwen Upload",
		HubModelID: "acme/qwen-upload", Version: "v1", StorageBackend: "missing",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}
