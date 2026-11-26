package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/go-taas/go-taas/pkg/modelhub"
	"github.com/go-taas/go-taas/pkg/storage"
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
	uploadDirTo = func(_ context.Context, _ *transfer.ObjectStoreRef, dir, key string, progress func(int64)) (int64, error) {
		uploadedDir, uploadedKey = dir, key
		written := storage.DirSize(dir)
		if progress != nil {
			progress(written)
		}
		return written, nil
	}

	spec := &transfer.Spec{
		Operation:         transfer.OperationDownload,
		HubSource:         "modelscope",
		HubModelID:        "Qwen/Qwen2.5-0.5B-Instruct",
		TargetObjectStore: &transfer.ObjectStoreRef{Endpoint: "http://minio:9000", Bucket: "models"},
		WeightPath:        "Qwen__Qwen2.5-0.5B-Instruct/",
	}
	var reported, reportedTotal int64
	var phases []string
	require.NoError(t, downloadOne(context.Background(), spec, func(phase string, n, total int64) {
		phases = append(phases, phase)
		reported, reportedTotal = n, total
	}))

	assert.Equal(t, "Qwen__Qwen2.5-0.5B-Instruct/", uploadedKey)
	require.NotEmpty(t, uploadedDir)
	assert.EqualValues(t, 2, reported, "config.json is the two bytes the fake hub wrote")
	assert.EqualValues(t, 2, reportedTotal, "the upload phase reports the bytes it moves")
	assert.Contains(t, phases, transfer.PhaseUpload, "the object-store write is its own phase")
	assert.Contains(t, phases, transfer.PhaseDownload)
}

func TestDownloadOneRequiresFilesystemTargetPath(t *testing.T) {
	spec := &transfer.Spec{
		Operation:  transfer.OperationDownload,
		HubSource:  "modelscope",
		HubModelID: "Qwen/Qwen2.5-0.5B-Instruct",
	}
	err := downloadOne(context.Background(), spec, func(string, int64, int64) {})
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
	var sawDownloadPhase bool
	require.NoError(t, downloadOne(context.Background(), spec, func(phase string, n, total int64) {
		mu.Lock()
		defer mu.Unlock()
		if phase == transfer.PhaseDownload {
			sawDownloadPhase = true
		}
		if total > 0 {
			totalSeen = total
		}
		finalBytes, finalTotal = n, total
	}))

	assert.True(t, sawDownloadPhase)
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
		done <- downloadOne(context.Background(), spec, func(_ string, n, total int64) {
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

func TestMoveOneCopiesThenDeletesSource(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	sourceDir := filepath.Join(sourceRoot, "qwen", "v1")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "model.bin"), []byte("weights"), 0o644))

	spec := &transfer.Spec{
		Operation:  transfer.OperationMove,
		SourcePath: sourceDir,
		TargetPath: filepath.Join(targetRoot, "qwen", "v1"),
		WeightPath: "qwen/v1",
	}
	require.NoError(t, moveOne(context.Background(), spec, func(string, int64, int64) {}))

	content, err := os.ReadFile(filepath.Join(targetRoot, "qwen", "v1", "model.bin"))
	require.NoError(t, err)
	assert.Equal(t, "weights", string(content))
	assert.NoFileExists(t, sourceDir)
}

func TestMoveOneKeepsSourceWhenCopyFails(t *testing.T) {
	sourceRoot := t.TempDir()
	sourceFile := filepath.Join(sourceRoot, "model.bin")
	require.NoError(t, os.WriteFile(sourceFile, []byte("weights"), 0o644))
	blocker := filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o644))

	spec := &transfer.Spec{Operation: transfer.OperationMove, SourcePath: sourceFile, TargetPath: filepath.Join(blocker, "child")}
	require.Error(t, moveOne(context.Background(), spec, func(string, int64, int64) {}))
	assert.FileExists(t, sourceFile)
}

// TestProgressReporterResetsBytesOnPhaseChange guards the console display:
// after the hub download reaches its total, the upload phase must start at
// zero bytes instead of reporting the download's 100% as the upload's.
func TestProgressReporterResetsBytesOnPhaseChange(t *testing.T) {
	client := fake.NewSimpleClientset()
	reporter := &progressReporter{id: "phase-test", client: client, ns: "taas", name: "taas-transfer-progress-phase-test"}
	ctx := context.Background()

	reporter.report(ctx, transfer.PhaseDownload, transfer.StateRunning, "", 100, 100)
	reporter.report(ctx, transfer.PhaseUpload, transfer.StateRunning, "", 0, 100)
	reporter.report(ctx, transfer.PhaseUpload, transfer.StateRunning, "", 40, 100)

	cm, err := client.CoreV1().ConfigMaps("taas").Get(ctx, "taas-transfer-progress-phase-test", metav1.GetOptions{})
	require.NoError(t, err)
	var got transfer.Progress
	require.NoError(t, json.Unmarshal([]byte(cm.Data["progress.json"]), &got))
	assert.Equal(t, transfer.PhaseUpload, got.Phase)
	assert.EqualValues(t, 40, got.Bytes)
	assert.EqualValues(t, 100, got.TotalBytes)
}

// TestProgressReporterPhaseResetsBytes asserts the reset directly: the
// upload's first sample is zero even though the download ended at its total.
func TestProgressReporterPhaseResetsBytes(t *testing.T) {
	client := fake.NewSimpleClientset()
	reporter := &progressReporter{id: "phase-test-2", client: client, ns: "taas", name: "taas-transfer-progress-phase-test-2"}
	ctx := context.Background()

	reporter.report(ctx, transfer.PhaseDownload, transfer.StateRunning, "", 100, 100)
	reporter.report(ctx, transfer.PhaseUpload, transfer.StateRunning, "", 0, 100)

	cm, err := client.CoreV1().ConfigMaps("taas").Get(ctx, "taas-transfer-progress-phase-test-2", metav1.GetOptions{})
	require.NoError(t, err)
	var got transfer.Progress
	require.NoError(t, json.Unmarshal([]byte(cm.Data["progress.json"]), &got))
	assert.EqualValues(t, 0, got.Bytes, "a new phase starts at zero bytes")
}
