package batch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// FileStore persists the JSONL input/result/error files under a
// batch/{org_id}/{batch_id}/ prefix (AD2, §4.2). The interface is
// implemented by a local-filesystem store for the compose stack and by
// an in-memory store for tests/FVT.
type FileStore interface {
	// PutInput writes the input file and returns its object-store key.
	PutInput(ctx context.Context, orgID, batchID string, content []byte) (string, error)
	// PutResult writes the result file and returns its key.
	PutResult(ctx context.Context, orgID, batchID string, content []byte) (string, error)
	// PutError writes the error file and returns its key.
	PutError(ctx context.Context, orgID, batchID string, content []byte) (string, error)
	// GetFile reads a file by its key.
	GetFile(ctx context.Context, key string) ([]byte, error)
	// DeleteFiles deletes the result/error files of a job (AD8).
	DeleteFiles(ctx context.Context, orgID, batchID string) error
}

// inputKey returns the object-store key of a job's input file.
func inputKey(orgID, batchID string) string {
	return fmt.Sprintf("batch/%s/%s/input.jsonl", orgID, batchID)
}

// resultKey returns the object-store key of a job's result file.
func resultKey(orgID, batchID string) string {
	return fmt.Sprintf("batch/%s/%s/result.jsonl", orgID, batchID)
}

// errorKey returns the object-store key of a job's error file.
func errorKey(orgID, batchID string) string {
	return fmt.Sprintf("batch/%s/%s/error.jsonl", orgID, batchID)
}

// LocalFileStore is a local-filesystem FileStore rooted at a directory.
// It is used by the compose stack (the object store is a JuiceFS mount).
type LocalFileStore struct {
	root string
}

// NewLocalFileStore constructs a LocalFileStore rooted at dir.
func NewLocalFileStore(dir string) *LocalFileStore {
	return &LocalFileStore{root: dir}
}

func (s *LocalFileStore) path(key string) string {
	return filepath.Join(s.root, filepath.FromSlash(key))
}

// PutInput implements FileStore.
func (s *LocalFileStore) PutInput(_ context.Context, orgID, batchID string, content []byte) (string, error) {
	key := inputKey(orgID, batchID)
	return key, s.write(key, content)
}

// PutResult implements FileStore.
func (s *LocalFileStore) PutResult(_ context.Context, orgID, batchID string, content []byte) (string, error) {
	key := resultKey(orgID, batchID)
	return key, s.write(key, content)
}

// PutError implements FileStore.
func (s *LocalFileStore) PutError(_ context.Context, orgID, batchID string, content []byte) (string, error) {
	key := errorKey(orgID, batchID)
	return key, s.write(key, content)
}

func (s *LocalFileStore) write(key string, content []byte) error {
	p := s.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, content, 0o600)
}

// GetFile implements FileStore.
func (s *LocalFileStore) GetFile(_ context.Context, key string) ([]byte, error) {
	return os.ReadFile(s.path(key))
}

// DeleteFiles implements FileStore.
func (s *LocalFileStore) DeleteFiles(_ context.Context, orgID, batchID string) error {
	dir := filepath.Join(s.root, "batch", orgID, batchID)
	// Delete only the result/error files, never the input.
	for _, key := range []string{resultKey(orgID, batchID), errorKey(orgID, batchID)} {
		_ = os.Remove(s.path(key))
	}
	_ = os.Remove(dir)
	return nil
}

// MemFileStore is an in-memory FileStore for tests and FVT.
type MemFileStore struct {
	files map[string][]byte
}

// NewMemFileStore constructs an empty in-memory FileStore.
func NewMemFileStore() *MemFileStore {
	return &MemFileStore{files: map[string][]byte{}}
}

// PutInput implements FileStore.
func (s *MemFileStore) PutInput(_ context.Context, orgID, batchID string, content []byte) (string, error) {
	key := inputKey(orgID, batchID)
	s.files[key] = content
	return key, nil
}

// PutResult implements FileStore.
func (s *MemFileStore) PutResult(_ context.Context, orgID, batchID string, content []byte) (string, error) {
	key := resultKey(orgID, batchID)
	s.files[key] = content
	return key, nil
}

// PutError implements FileStore.
func (s *MemFileStore) PutError(_ context.Context, orgID, batchID string, content []byte) (string, error) {
	key := errorKey(orgID, batchID)
	s.files[key] = content
	return key, nil
}

// GetFile implements FileStore.
func (s *MemFileStore) GetFile(_ context.Context, key string) ([]byte, error) {
	content, ok := s.files[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return content, nil
}

// DeleteFiles implements FileStore.
func (s *MemFileStore) DeleteFiles(_ context.Context, orgID, batchID string) error {
	delete(s.files, resultKey(orgID, batchID))
	delete(s.files, errorKey(orgID, batchID))
	return nil
}
