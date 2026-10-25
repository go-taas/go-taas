package model

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

func newDownloadTestService(t *testing.T) *Service {
	t.Helper()
	svc := newModelTestService(t)
	require.NoError(t, svc.Migrate(context.Background()))
	svc.SetWeightsDir(t.TempDir())
	svc.SetModelDownloader(&fakeDownloader{})
	return svc
}

func waitForState(t *testing.T, svc *Service, downloadID, state string) *modelv1.ModelDownload {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := svc.ListModelDownloads(context.Background(), &modelv1.ListModelDownloadsRequest{})
		require.NoError(t, err)
		for _, d := range list.GetDownloads() {
			if d.GetDownloadId() == downloadID {
				if d.GetState() == state {
					return d
				}
				if d.GetState() == DownloadStateFailed && state != DownloadStateFailed {
					t.Fatalf("download failed: %s", d.GetMessage())
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("download %s never reached state %q", downloadID, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartModelDownloadRegistersModel(t *testing.T) {
	svc := newDownloadTestService(t)
	ctx := context.Background()

	resp, err := svc.StartModelDownload(ctx, &modelv1.StartModelDownloadRequest{
		Source:     "modelscope",
		HubModelId: "MiniMax/MiniMax-M3.1-Flash",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetDownload().GetDownloadId())
	// Defaults derive from the hub id's last segment.
	assert.Equal(t, "MiniMax-M3.1-Flash", resp.GetDownload().GetName())
	assert.Equal(t, "MiniMax-M3.1-Flash", resp.GetDownload().GetVersion())
	assert.Equal(t, DownloadStateQueued, resp.GetDownload().GetState())

	done := waitForState(t, svc, resp.GetDownload().GetDownloadId(), DownloadStateSucceeded)
	assert.Equal(t, "MiniMax__MiniMax-M3.1-Flash/", done.GetWeightPath())
	assert.NotEmpty(t, done.GetModelId())
	assert.Greater(t, done.GetBytes(), int64(0))

	// The job registered the catalog entry with the hub id bound.
	got, err := svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: done.GetModelId()})
	require.NoError(t, err)
	assert.Equal(t, "MiniMax/MiniMax-M3.1-Flash", got.GetModel().GetHubModelId())
	assert.Equal(t, "MiniMax-M3.1-Flash", got.GetModel().GetLatestVersion())
}

func TestStartModelDownloadValidation(t *testing.T) {
	svc := newDownloadTestService(t)
	ctx := context.Background()

	cases := []struct {
		name string
		req  *modelv1.StartModelDownloadRequest
		code apierrors.Code
	}{
		{"no source", &modelv1.StartModelDownloadRequest{HubModelId: "a/b"}, apierrors.CodeModelPathInvalid},
		{"bad source", &modelv1.StartModelDownloadRequest{Source: "ftp", HubModelId: "a/b"}, apierrors.CodeModelPathInvalid},
		{"no hub id", &modelv1.StartModelDownloadRequest{Source: "modelscope"}, apierrors.CodeModelPathInvalid},
		{"bad hub id", &modelv1.StartModelDownloadRequest{Source: "modelscope", HubModelId: "just-a-name"}, apierrors.CodeModelPathInvalid},
	}
	for _, tc := range cases {
		_, err := svc.StartModelDownload(ctx, tc.req)
		require.Error(t, err, tc.name)
		assert.Equal(t, tc.code, apierrors.CodeOf(err), tc.name)
	}

	// Not configured: the download feature needs a downloader and a
	// weights directory.
	bare := newModelTestService(t)
	require.NoError(t, bare.Migrate(ctx))
	_, err := bare.StartModelDownload(ctx, &modelv1.StartModelDownloadRequest{Source: "modelscope", HubModelId: "a/b"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelPathInvalid, apierrors.CodeOf(err))
}

func TestStartModelDownloadHubIDConflict(t *testing.T) {
	svc := newDownloadTestService(t)
	ctx := context.Background()

	// A hub id already bound to another model name is rejected before
	// any bytes are downloaded.
	_, err := svc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
		Name: "minimax", Version: "v1", HubModelId: "MiniMax/MiniMax-M3.1-Flash", WeightPath: "minimax/v1",
	})
	require.NoError(t, err)
	_, err = svc.StartModelDownload(ctx, &modelv1.StartModelDownloadRequest{
		Source: "modelscope", HubModelId: "MiniMax/MiniMax-M3.1-Flash", Name: "other",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelExists, apierrors.CodeOf(err))
}

func TestStartModelDownloadFailureIsReported(t *testing.T) {
	svc := newModelTestService(t)
	require.NoError(t, svc.Migrate(context.Background()))
	svc.SetWeightsDir(t.TempDir())
	svc.SetModelDownloader(failingDownloader{})

	resp, err := svc.StartModelDownload(context.Background(), &modelv1.StartModelDownloadRequest{
		Source: "huggingface", HubModelId: "Qwen/Qwen2.5-0.5B-Instruct",
	})
	require.NoError(t, err)
	failed := waitForState(t, svc, resp.GetDownload().GetDownloadId(), DownloadStateFailed)
	assert.Contains(t, failed.GetMessage(), "hub unreachable")
	assert.Empty(t, failed.GetModelId())
}

func TestRecoverInterruptedDownloads(t *testing.T) {
	svc := newDownloadTestService(t)
	ctx := context.Background()

	// A job a previous process left "downloading" is failed at startup.
	db, err := svc.gormDB()
	require.NoError(t, err)
	job := &DownloadJob{
		ID: "11111111-1111-1111-1111-111111111111", Source: "modelscope",
		HubModelID: "a/b", Name: "b", Version: "b", State: DownloadStateDownloading,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(job).Error)

	require.NoError(t, svc.RecoverInterruptedDownloads(ctx))
	list, err := svc.ListModelDownloads(ctx, &modelv1.ListModelDownloadsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetDownloads(), 1)
	assert.Equal(t, DownloadStateFailed, list.GetDownloads()[0].GetState())
}

func TestDirSizeCountsFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/a.bin", make([]byte, 100), 0o644))
	require.NoError(t, os.MkdirAll(dir+"/sub", 0o755))
	require.NoError(t, os.WriteFile(dir+"/sub/b.bin", make([]byte, 200), 0o644))
	assert.EqualValues(t, 300, dirSize(dir))
	assert.EqualValues(t, 0, dirSize(dir+"/missing"))
}
