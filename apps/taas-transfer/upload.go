package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-taas/go-taas/pkg/modelarchive"
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/transfer"
)

// uploadOne turns a staged upload archive into extracted weights in the
// target backend. The archive was already composed (by the control plane) into
// a single object in the staging object store under spec.Upload.ObjectKey.
//
// The job:
//  1. downloads the single object to a temporary archive file,
//  2. unpacks it into the target (a filesystem mount) or a temporary dir that
//     is then uploaded to an object-store backend,
//  3. reports byte progress as the extraction proceeds.
//
// Registration happens server-side once the Job reports success, so the Job
// only ever moves bytes — it has no database access.
func uploadOne(ctx context.Context, spec *transfer.Spec, progress func(int64, int64)) error {
	if spec.Upload == nil || spec.Upload.ObjectKey == "" {
		return fmt.Errorf("upload needs an upload object key")
	}
	if strings.TrimSpace(spec.Upload.Format) == "" {
		return fmt.Errorf("upload needs a format")
	}
	source, err := remoteFor(spec.SourceObjectStore)
	if err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("upload needs a source object store")
	}

	// Fetch the composed archive into a temp file first: archive/zip and
	// gzip both need a seekable source, so extractArchive opens the file.
	tempDir, err := os.MkdirTemp("", "taas-upload-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	archivePath := filepath.Join(tempDir, "archive")
	if err := downloadSingle(ctx, source, spec.Upload.ObjectKey, archivePath, func(n int64) { progress(n, 0) }); err != nil {
		return err
	}

	// Learn the archive size for a determinate progress bar.
	total := fileSize(archivePath)
	progress(0, total)

	if objectStoreRefPresent(spec.TargetObjectStore) {
		// Extract into a temp dir, then push the tree into the object store.
		extractDir := filepath.Join(tempDir, "extract")
		if err := os.MkdirAll(extractDir, 0o750); err != nil {
			return err
		}
		stop := watchProgress(extractDir, total, progress)
		_, _, err = modelarchive.Unpack(archivePath, spec.Upload.Format, extractDir)
		stop()
		if err != nil {
			return err
		}
		written, upErr := uploadDirTo(ctx, spec.TargetObjectStore, extractDir, spec.WeightPath)
		if upErr != nil {
			return upErr
		}
		progress(written, total)
		return nil
	}

	if strings.TrimSpace(spec.TargetPath) == "" {
		return fmt.Errorf("upload needs a target path or object store")
	}
	stop := watchProgress(spec.TargetPath, total, progress)
	defer stop()
	_, _, err = modelarchive.Unpack(archivePath, spec.Upload.Format, spec.TargetPath)
	if err != nil {
		return err
	}
	stop()
	written := storage.DirSize(spec.TargetPath)
	if total < written {
		total = written
	}
	progress(written, total)
	return nil
}

// downloadSingle copies one object to path, reporting bytes as they land.
func downloadSingle(ctx context.Context, source *storage.RemoteClient, key, path string, progress func(int64)) error {
	obj, err := source.OpenObject(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = obj.Close() }()
	out, err := os.Create(path) //nolint:gosec // path is a temp file we own
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, obj)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	progress(n)
	return nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
