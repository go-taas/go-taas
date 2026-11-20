package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/transfer"
)

func TestTargetOwnerDefaultsToTheConsoleServiceAccount(t *testing.T) {
	t.Setenv("TRANSFER_TARGET_UID", "")
	t.Setenv("TRANSFER_TARGET_GID", "")

	uid, gid := targetOwner()

	assert.Equal(t, 65532, uid)
	assert.Equal(t, 65532, gid)
}

func TestTargetOwnerHonorsTheEnvironmentOverride(t *testing.T) {
	t.Setenv("TRANSFER_TARGET_UID", "1000")
	t.Setenv("TRANSFER_TARGET_GID", "2000")

	uid, gid := targetOwner()

	assert.Equal(t, 1000, uid)
	assert.Equal(t, 2000, gid)
}

func TestAdoptTargetOwnershipSkipsObjectStoreAndDeleteTargets(t *testing.T) {
	t.Setenv("TRANSFER_TARGET_UID", "")
	t.Setenv("TRANSFER_TARGET_GID", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "weights")
	require.NoError(t, os.Mkdir(path, 0o750))

	assert.NoError(t, adoptTargetOwnership(&transfer.Spec{
		Operation:  transfer.OperationDelete,
		SourcePath: path,
	}))
	assert.NoError(t, adoptTargetOwnership(&transfer.Spec{
		Operation:         transfer.OperationDownload,
		TargetPath:        path,
		TargetObjectStore: &transfer.ObjectStoreRef{Endpoint: "http://minio:9000", Bucket: "models"},
	}))
}

func TestEnvIDRejectsInvalidValues(t *testing.T) {
	t.Setenv("TRANSFER_TARGET_UID", "not-a-number")
	t.Setenv("TRANSFER_TARGET_GID", "-1")

	uid, gid := targetOwner()

	assert.Equal(t, defaultTargetOwner, uid)
	assert.Equal(t, defaultTargetOwner, gid)
}

func TestAdoptTreeChownsEveryEntry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "weights")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "nested"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "nested", "model.bin"), []byte("weights"), 0o640))
	link := filepath.Join(root, "latest")
	if err := os.Symlink("nested", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	uid, gid := os.Getuid(), os.Getgid()
	require.NoError(t, adoptTree(root, uid, gid))

	for _, path := range []string{
		root,
		filepath.Join(root, "nested"),
		filepath.Join(root, "nested", "model.bin"),
		link,
	} {
		info, err := os.Lstat(path)
		require.NoError(t, err)
		stat, ok := info.Sys().(*syscall.Stat_t)
		require.True(t, ok)
		assert.Equal(t, uint32(uid), stat.Uid, path)
		assert.Equal(t, uint32(gid), stat.Gid, path)
	}
}

func TestAdoptTreeIgnoresAMissingRoot(t *testing.T) {
	assert.NoError(t, adoptTree(filepath.Join(t.TempDir(), "absent"), os.Getuid(), os.Getgid()))
}
