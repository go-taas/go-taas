package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-taas/go-taas/pkg/transfer"
)

// defaultTargetOwner is the uid and gid of the server process. Transfers run
// as root so they can read root-only hub caches; without adopting the target
// afterwards the server could not delete the weights it owns in the catalog.
const defaultTargetOwner = 65532

// targetOwner returns the uid and gid that should own a local transfer target.
// It is configurable for installations that run the server as another user.
func targetOwner() (int, int) {
	return envID("TRANSFER_TARGET_UID"), envID("TRANSFER_TARGET_GID")
}

func envID(name string) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultTargetOwner
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return defaultTargetOwner
	}
	return value
}

// adoptsLocalTarget reports whether an operation writes a local target that
// the server user must be able to read afterwards and later delete.
func adoptsLocalTarget(operation string) bool {
	switch operation {
	case transfer.OperationCopy, transfer.OperationMove,
		transfer.OperationDownload, transfer.OperationUpload:
		return true
	default:
		return false
	}
}

// adoptTargetOwnership hands a local transfer target to the server user so a
// later model deletion can remove it. It is a no-op when the transfer does not
// write a local path (object-store targets and deletes) or when the process is
// not root. Ownership is best effort: the transfer already succeeded, and the
// caller logs the failure.
func adoptTargetOwnership(spec *transfer.Spec) error {
	if !adoptsLocalTarget(spec.Operation) {
		return nil
	}
	if objectStoreRefPresent(spec.TargetObjectStore) || strings.TrimSpace(spec.TargetPath) == "" {
		return nil
	}
	if os.Geteuid() != 0 {
		return nil
	}
	uid, gid := targetOwner()
	return adoptTree(spec.TargetPath, uid, gid)
}

// adoptTree gives every entry under root to uid/gid. Missing roots are not an
// error: a transfer can legitimately produce no local files.
func adoptTree(root string, uid, gid int) error {
	var firstErr error
	walkErr := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		// WalkDir does not follow directory symlinks, and Lchown does not
		// follow the final link, so the walk stays inside the transfer target.
		if chownErr := os.Lchown(path, uid, gid); chownErr != nil && firstErr == nil { //nolint:gosec // G122: see above
			firstErr = chownErr
		}
		return nil
	})
	if walkErr != nil && firstErr == nil {
		firstErr = walkErr
	}
	if firstErr != nil && os.IsNotExist(firstErr) {
		return nil
	}
	return firstErr
}
