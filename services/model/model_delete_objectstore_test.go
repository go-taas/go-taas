package model

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/storage"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

// fakeObjectStore serves the two S3 calls a prefix delete makes
// (ListObjectsV2 and RemoveObject) and records the deleted keys, so a test can
// prove the bucket is cleaned and not only the catalog row.
func fakeObjectStore(t *testing.T, keys []string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	deleted := make([]string, 0, len(keys))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			var body strings.Builder
			body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
			body.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
			body.WriteString(`<Name>taas</Name><Prefix></Prefix><KeyCount>` + strconv.Itoa(len(keys)) + `</KeyCount>`)
			body.WriteString(`<MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`)
			for _, key := range keys {
				body.WriteString(`<Contents><Key>` + key + `</Key><Size>4</Size>`)
				body.WriteString(`<LastModified>2026-01-01T00:00:00.000Z</LastModified><ETag>"e"</ETag></Contents>`)
			}
			body.WriteString(`</ListBucketResult>`)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body.String()))
		case http.MethodDelete:
			mu.Lock()
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/taas/"))
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), deleted...)
	}
}

// TestDeleteModelRemovesObjectStoreWeights verifies deleting a model stored on
// an object-store backend also deletes the bucket prefix, not just the
// catalog row.
func TestDeleteModelRemovesObjectStoreWeights(t *testing.T) {
	srv, deleted := fakeObjectStore(t, []string{"qwen/v1/model.bin", "qwen/v1/config.json"})
	t.Setenv("TAAS_STORAGE_MINIO_ACCESS_KEY", "access")
	t.Setenv("TAAS_STORAGE_MINIO_SECRET_KEY", "secret")

	svc := newModelTestService(t)
	svc.SetStorageConfig(storage.Config{
		DefaultBackend: "minio",
		Backends: map[string]storage.Backend{
			"minio": {
				Name: "minio",
				Type: storage.TypeObjectStore,
				ObjectStore: storage.ObjectStore{
					Endpoint: srv.URL, Bucket: "taas", Region: "us-east-1", ForcePathStyle: true,
				},
			},
		},
	})
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-obj", HubModelId: "qwen/obj", Version: "v1", WeightPath: "qwen/v1/",
	})
	require.NoError(t, err)
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.SetVersionStorageBackend(adminCtx(), reg.GetModelId(), "v1", "minio"))

	_, err = svc.DeleteModel(adminCtx(), &modelv1.DeleteModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"qwen/v1/model.bin", "qwen/v1/config.json"}, deleted(),
		"the object-store prefix is deleted, not only the catalog row")
}
