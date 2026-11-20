package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	var reported, reportedTotal int64
	require.NoError(t, downloadOne(context.Background(), spec, func(n, total int64) {
		reported, reportedTotal = n, total
	}))

	assert.Equal(t, "Qwen__Qwen2.5-0.5B-Instruct/", uploadedKey)
	require.NotEmpty(t, uploadedDir)
	assert.Equal(t, int64(6), reported)
	assert.Zero(t, reportedTotal)
}

func TestDownloadOneRequiresFilesystemTargetPath(t *testing.T) {
	spec := &transfer.Spec{
		Operation:  transfer.OperationDownload,
		HubSource:  "modelscope",
		HubModelID: "Qwen/Qwen2.5-0.5B-Instruct",
	}
	err := downloadOne(context.Background(), spec, func(int64, int64) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target path")
}

// TestDownloadOneReportsTheHubTotal verifies the Job tells the control plane
// how many bytes to expect, so the download can show a real percentage
// instead of an indeterminate bar for the whole transfer.
func TestDownloadOneReportsTheHubTotal(t *testing.T) {
	original := newHubDownloader
	t.Cleanup(func() { newHubDownloader = original })
	newHubDownloader = func() modelhub.Downloader {
		return sizedHubDownloader{fakeHubDownloader{}, 1000}
	}

	dir := t.TempDir()
	spec := &transfer.Spec{
		Operation:  transfer.OperationDownload,
		HubSource:  "modelscope",
		HubModelID: "Qwen/Qwen2.5-0.5B-Instruct",
		TargetPath: dir,
	}
	var (
		mu         sync.Mutex
		totalSeen  int64
		finalBytes int64
		finalTotal int64
	)
	require.NoError(t, downloadOne(context.Background(), spec, func(n, total int64) {
		mu.Lock()
		defer mu.Unlock()
		if total > 0 {
			totalSeen = total
		}
		finalBytes, finalTotal = n, total
	}))

	assert.EqualValues(t, 1000, totalSeen)
	assert.Greater(t, finalBytes, int64(0))
	assert.EqualValues(t, 1000, finalTotal)
}

// TestDownloadOneReportsProgressWhileItRuns verifies bytes are sampled
// during the download instead of only at the end, so the console bar moves.
func TestDownloadOneReportsProgressWhileItRuns(t *testing.T) {
	original := newHubDownloader
	originalInterval := progressInterval
	t.Cleanup(func() {
		newHubDownloader = original
		progressInterval = originalInterval
	})
	progressInterval = 5 * time.Millisecond

	started := make(chan struct{})
	release := make(chan struct{})
	downloader := &blockingHubDownloader{started: started, release: release, total: 100}
	newHubDownloader = func() modelhub.Downloader { return downloader }

	dir := t.TempDir()
	spec := &transfer.Spec{
		Operation:  transfer.OperationDownload,
		HubSource:  "modelscope",
		HubModelID: "Qwen/Qwen2.5-0.5B-Instruct",
		TargetPath: dir,
	}
	var (
		mu       sync.Mutex
		midBytes int64
		midTotal int64
	)
	done := make(chan error, 1)
	go func() {
		done <- downloadOne(context.Background(), spec, func(n, total int64) {
			mu.Lock()
			defer mu.Unlock()
			if n > 0 && n < total {
				midBytes, midTotal = n, total
			}
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("download never started")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		seen := midBytes > 0
		mu.Unlock()
		if seen || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	require.NoError(t, <-done)

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, midBytes, int64(0), "a progress sample arrived while the download was still running")
	assert.EqualValues(t, 100, midTotal)
}

type fakeHubDownloader struct{}

func (fakeHubDownloader) Download(_ context.Context, _ modelhub.Source, _, destDir string) error {
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "config.json"), []byte("{}"), 0o600)
}

type sizedHubDownloader struct {
	fakeHubDownloader
	total int64
}

func (d sizedHubDownloader) TotalBytes(context.Context, modelhub.Source, string) (int64, error) {
	return d.total, nil
}

type blockingHubDownloader struct {
	started chan struct{}
	release chan struct{}
	total   int64
}

func (d *blockingHubDownloader) Download(_ context.Context, _ modelhub.Source, _, destDir string) error {
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(destDir, "weights.bin"), []byte("data"), 0o600); err != nil {
		return err
	}
	close(d.started)
	<-d.release
	return nil
}

func (d *blockingHubDownloader) TotalBytes(context.Context, modelhub.Source, string) (int64, error) {
	return d.total, nil
}
