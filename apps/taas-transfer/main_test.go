package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/modelhub"
	"github.com/go-taas/go-taas/pkg/transfer"
)

func TestDownloadOneUploadsToObjectStoreTarget(t *testing.T) {
	originalDownloader := newHubDownloader
	originalUpload := uploadDirTo
	t.Cleanup(func() {
		newHubDownloader = originalDownloader
		uploadDirTo = originalUpload
	})

	newHubDownloader = func() modelhub.Downloader { return fakeHubDownloader{} }
	var uploadedDir, uploadedKey string
	uploadDirTo = func(_ context.Context, _ *transfer.ObjectStoreRef, dir, key string) (int64, error) {
		uploadedDir, uploadedKey = dir, key
		return 6, nil
	}

	spec := &transfer.Spec{
		Operation:         transfer.OperationDownload,
		HubSource:         "modelscope",
		HubModelID:        "Qwen/Qwen2.5-0.5B-Instruct",
		TargetObjectStore: &transfer.ObjectStoreRef{Endpoint: "http://minio:9000", Bucket: "models"},
		WeightPath:        "Qwen__Qwen2.5-0.5B-Instruct/",
	}
	var reported int64
	require.NoError(t, downloadOne(context.Background(), spec, func(n int64) { reported = n }))

	assert.Equal(t, "Qwen__Qwen2.5-0.5B-Instruct/", uploadedKey)
	require.NotEmpty(t, uploadedDir)
	assert.Equal(t, int64(6), reported)
}

func TestDownloadOneRequiresFilesystemTargetPath(t *testing.T) {
	spec := &transfer.Spec{
		Operation:  transfer.OperationDownload,
		HubSource:  "modelscope",
		HubModelID: "Qwen/Qwen2.5-0.5B-Instruct",
	}
	err := downloadOne(context.Background(), spec, func(int64) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target path")
}

type fakeHubDownloader struct{}

func (fakeHubDownloader) Download(_ context.Context, _ modelhub.Source, _, destDir string) error {
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "config.json"), []byte("{}"), 0o600)
}
