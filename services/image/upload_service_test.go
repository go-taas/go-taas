package image

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/registry"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeArchiveImporter records the archive push and returns a configurable
// error. It embeds the registry importer so the service's ImportImage path
// stays available.
type fakeArchiveImporter struct {
	archivePath string
	srcRef      string
	dstRef      string
	err         error
}

func (f *fakeArchiveImporter) Import(context.Context, string, *registry.Credentials, string, *registry.Credentials) error {
	return f.err
}

func (f *fakeArchiveImporter) ImportArchive(_ context.Context, archivePath, srcRef, dstRef string, _ *registry.Credentials) error {
	f.archivePath = archivePath
	f.srcRef = srcRef
	f.dstRef = dstRef
	return f.err
}

// writeArchive writes a docker-save style tar carrying manifest.json with the
// given repo tag.
func writeArchive(t *testing.T, repoTag string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image.tar")
	file, err := os.Create(path) //nolint:gosec // test temp file
	require.NoError(t, err)
	writer := tar.NewWriter(file)
	manifest := `[{"RepoTags":["` + repoTag + `"],"Layers":["layer.tar"]}]`
	require.NoError(t, writer.WriteHeader(&tar.Header{
		Name: "manifest.json", Mode: 0o600, Size: int64(len(manifest)),
	}))
	_, err = writer.Write([]byte(manifest))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
	return path
}

func TestUploadImageHappyPath(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	imp := &fakeArchiveImporter{}
	svc.SetImporter(imp)

	archive := writeArchive(t, "docker.io/library/a/b/vllm:v1.2.3")
	res, err := svc.UploadImage(ctx, UploadImageRequest{
		ArchivePath: archive,
		Accelerator: "nvidia",
		Engine:      "vllm",
		Description: "uploaded engine",
	})
	require.NoError(t, err)

	// The destination is the configured project and only the image's own name
	// survives: an upload never reproduces the archive's namespace tree.
	assert.Equal(t, "hub.example.com/taas/vllm:v1.2.3", res.Reference)
	assert.Equal(t, "hub.example.com/taas/vllm", res.Name)
	assert.Equal(t, "v1.2.3", res.Tag)
	assert.Equal(t, archive, imp.archivePath)
	assert.Equal(t, "docker.io/library/a/b/vllm:v1.2.3", imp.srcRef,
		"the archive member is selected by its own reference")
	assert.Equal(t, "hub.example.com/taas/vllm:v1.2.3", imp.dstRef)

	// The catalog row is registered under the Harbor reference.
	stored, err := svc.repo.FindByID(ctx, res.ImageID)
	require.NoError(t, err)
	assert.Equal(t, "hub.example.com/taas/vllm", stored.Name)
	assert.Equal(t, "v1.2.3", stored.Tag)
	assert.Equal(t, "nvidia", stored.Accelerator)
	assert.Equal(t, "vllm", stored.Engine)
	assert.Equal(t, "uploaded engine", stored.Description)
}

func TestUploadImageUsesFormFieldsWhenArchiveIsUntagged(t *testing.T) {
	svc, _ := newServiceForTest(t)
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	imp := &fakeArchiveImporter{}
	svc.SetImporter(imp)

	archive := writeArchive(t, "<none>:<none>")
	res, err := svc.UploadImage(context.Background(), UploadImageRequest{
		ArchivePath:     archive,
		SourceReference: "my-engine:7",
		Accelerator:     "cpu",
		Engine:          "custom",
	})
	require.NoError(t, err)
	assert.Equal(t, "hub.example.com/taas/my-engine:7", res.Reference)
	assert.Equal(t, "hub.example.com/taas/my-engine", res.Name)
	assert.Equal(t, "7", res.Tag)
	assert.Empty(t, imp.srcRef,
		"an untagged archive cannot be selected by a reference; the form only names the destination")
}

func TestUploadImageExplicitNameAndTagWin(t *testing.T) {
	svc, _ := newServiceForTest(t)
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	svc.SetImporter(&fakeArchiveImporter{})

	res, err := svc.UploadImage(context.Background(), UploadImageRequest{
		ArchivePath: writeArchive(t, "docker.io/library/vllm:v1"),
		Name:        "vllm-openai",
		Tag:         "v0.28.0",
		Accelerator: "nvidia",
		Engine:      "vllm",
	})
	require.NoError(t, err)
	assert.Equal(t, "hub.example.com/taas/vllm-openai:v0.28.0", res.Reference)
}

func TestUploadImageValidation(t *testing.T) {
	svc, _ := newServiceForTest(t)
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	svc.SetImporter(&fakeArchiveImporter{})
	archive := writeArchive(t, "docker.io/library/vllm:v1")

	t.Run("engine required", func(t *testing.T) {
		_, err := svc.UploadImage(context.Background(), UploadImageRequest{
			ArchivePath: archive, Accelerator: "nvidia",
		})
		require.Error(t, err)
		assert.Equal(t, apierrors.CodeImageReferenceInvalid, apierrors.CodeOf(err))
	})

	t.Run("unknown accelerator", func(t *testing.T) {
		_, err := svc.UploadImage(context.Background(), UploadImageRequest{
			ArchivePath: archive, Accelerator: "tpu!!", Engine: "vllm",
		})
		require.Error(t, err)
		assert.Equal(t, apierrors.CodeImageIncompatible, apierrors.CodeOf(err))
	})

	t.Run("missing archive", func(t *testing.T) {
		_, err := svc.UploadImage(context.Background(), UploadImageRequest{
			Accelerator: "nvidia", Engine: "vllm",
		})
		require.Error(t, err)
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := svc.UploadImage(context.Background(), UploadImageRequest{
			ArchivePath: filepath.Join(t.TempDir(), "absent.tar"),
			Accelerator: "nvidia", Engine: "vllm",
		})
		require.Error(t, err)
	})

	t.Run("undecidable name", func(t *testing.T) {
		_, err := svc.UploadImage(context.Background(), UploadImageRequest{
			ArchivePath: writeArchive(t, "<none>:<none>"),
			Accelerator: "nvidia", Engine: "vllm",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot derive the image name")
	})
}

func TestUploadImageRequiresHarborAndImporter(t *testing.T) {
	svc, _ := newServiceForTest(t)
	archive := writeArchive(t, "docker.io/library/vllm:v1")

	setTestHarbor("", "", "", "")
	svc.SetImporter(&fakeArchiveImporter{})
	_, err := svc.UploadImage(context.Background(), UploadImageRequest{
		ArchivePath: archive, Accelerator: "nvidia", Engine: "vllm",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Harbor")

	// An importer that cannot push archives fails closed.
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	svc.SetImporter(fakeImporterOnly{})
	_, err = svc.UploadImage(context.Background(), UploadImageRequest{
		ArchivePath: archive, Accelerator: "nvidia", Engine: "vllm",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

// fakeImporterOnly implements the registry importer but not the archive one.
type fakeImporterOnly struct{}

func (fakeImporterOnly) Import(context.Context, string, *registry.Credentials, string, *registry.Credentials) error {
	return nil
}

func TestUploadImageDuplicatesAndPushFailure(t *testing.T) {
	svc, img := newServiceForTest(t)
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	imp := &fakeArchiveImporter{}
	svc.SetImporter(imp)
	ctx := context.Background()

	// The seeded catalog row is ghcr.io/go-taas/vllm:v0.6.3; an upload of the
	// same triple in the configured project is a different triple, so seed
	// the duplicate explicitly.
	duplicate := &Image{Name: "hub.example.com/taas/vllm", Tag: "v1", Accelerator: "nvidia", Engine: "vllm"}
	require.NoError(t, svc.repo.CreateImage(ctx, duplicate))
	archive := writeArchive(t, "docker.io/library/vllm:v1")

	_, err := svc.UploadImage(ctx, UploadImageRequest{
		ArchivePath: archive, Accelerator: "nvidia", Engine: "vllm",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeImageExists, apierrors.CodeOf(err))

	// A push failure surfaces as a business error and writes no catalog row.
	imp.err = assert.AnError
	_, err = svc.UploadImage(ctx, UploadImageRequest{
		ArchivePath: writeArchive(t, "docker.io/library/other:v1"),
		Accelerator: "nvidia", Engine: "vllm",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload failed")

	// The seeded row is untouched.
	found, err := svc.repo.FindByID(ctx, img.ID)
	require.NoError(t, err)
	assert.Equal(t, "ghcr.io/go-taas/vllm", found.Name)
}
