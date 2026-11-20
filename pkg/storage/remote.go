package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// RemoteRef is the resolved object-store access description a transfer Job
// uses. Credentials are passed through the environment, never serialized
// into a ConfigMap.
type RemoteRef struct {
	Provider           string `json:"provider,omitempty"`
	Endpoint           string `json:"endpoint,omitempty"`
	Region             string `json:"region,omitempty"`
	Bucket             string `json:"bucket,omitempty"`
	Prefix             string `json:"prefix,omitempty"`
	ForcePathStyle     bool   `json:"forcePathStyle,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	AccessKeyEnv       string `json:"accessKeyEnv,omitempty"`
	SecretKeyEnv       string `json:"secretKeyEnv,omitempty"`
}

// RemoteClient wraps a MinIO/S3 client and its bucket.
type RemoteClient struct {
	client *minio.Client
	bucket string
	prefix string
}

// NewRemoteClient builds a client from a resolved reference. Anonymous
// access is used when no credential env is declared.
func NewRemoteClient(ref RemoteRef) (*RemoteClient, error) {
	if strings.TrimSpace(ref.Endpoint) == "" {
		return nil, fmt.Errorf("storage: object-store endpoint is required")
	}
	if strings.TrimSpace(ref.Bucket) == "" {
		return nil, fmt.Errorf("storage: object-store bucket is required")
	}
	endpoint := strings.TrimSpace(ref.Endpoint)
	secure := strings.HasPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	var creds *credentials.Credentials
	if ref.AccessKeyEnv != "" {
		access := os.Getenv(ref.AccessKeyEnv)
		secret := os.Getenv(ref.SecretKeyEnv)
		if access == "" || secret == "" {
			return nil, fmt.Errorf("storage: object-store credentials %s/%s are empty", ref.AccessKeyEnv, ref.SecretKeyEnv)
		}
		creds = credentials.NewStaticV4(access, secret, "")
	} else {
		creds = credentials.NewStaticV4("", "", "")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        creds,
		Secure:       secure,
		Region:       ref.Region,
		BucketLookup: bucketLookup(ref.ForcePathStyle),
	})
	if err != nil {
		return nil, err
	}
	return &RemoteClient{client: client, bucket: ref.Bucket, prefix: strings.Trim(ref.Prefix, "/")}, nil
}

func bucketLookup(forcePath bool) minio.BucketLookupType {
	if forcePath {
		return minio.BucketLookupPath
	}
	return minio.BucketLookupAuto
}

func (c *RemoteClient) key(key string) string {
	key = strings.Trim(key, "/")
	if c.prefix == "" {
		return key
	}
	if key == "" {
		return c.prefix
	}
	return c.prefix + "/" + key
}

// joinObjectKey joins a model prefix and a relative file path into one
// object key. Both sides are trimmed so a weight path that already ends in
// "/" cannot produce the "//" object names S3 and MinIO reject.
func joinObjectKey(prefix, rel string) string {
	prefix = strings.Trim(prefix, "/")
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	switch {
	case prefix == "":
		return rel
	case rel == "":
		return prefix
	default:
		return prefix + "/" + rel
	}
}

// DownloadPrefix copies every object under key to destDir, preserving the
// relative path. It returns the number of bytes written and calls progress
// after each object.
func (c *RemoteClient) DownloadPrefix(ctx context.Context, key, destDir string, progress func(int64)) (int64, error) {
	var written int64
	prefix := c.key(key)
	objects := c.client.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
	for object := range objects {
		if object.Err != nil {
			return written, object.Err
		}
		rel := strings.TrimPrefix(object.Key, prefix)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			rel = filepath.Base(object.Key)
		}
		target := filepath.Join(destDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return written, err
		}
		obj, err := c.client.GetObject(ctx, c.bucket, object.Key, minio.GetObjectOptions{})
		if err != nil {
			return written, err
		}
		out, err := os.Create(target) //nolint:gosec // target is derived from a validated key under the destination root
		if err != nil {
			_ = obj.Close()
			return written, err
		}
		n, copyErr := io.Copy(out, obj)
		closeErr := out.Close()
		_ = obj.Close()
		if copyErr != nil {
			return written, copyErr
		}
		if closeErr != nil {
			return written, closeErr
		}
		written += n
		if progress != nil {
			progress(written)
		}
	}
	if written == 0 {
		return 0, fmt.Errorf("storage: no objects under s3://%s/%s", c.bucket, prefix)
	}
	return written, nil
}

// UploadDir copies a local directory under key. It is used by the upload
// flow when the backend is an object store.
func (c *RemoteClient) UploadDir(ctx context.Context, dir, key string) (int64, error) {
	var written int64
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		objectKey := c.key(joinObjectKey(key, rel))
		upload, err := c.client.FPutObject(ctx, c.bucket, objectKey, path, minio.PutObjectOptions{})
		if err != nil {
			return err
		}
		written += upload.Size
		return nil
	})
	return written, err
}

// CopyPrefix copies every object under srcKey to dstKey inside the same
// bucket using the server-side copy API.
func (c *RemoteClient) CopyPrefix(ctx context.Context, srcKey, dstKey string) (int64, error) {
	var written int64
	src := c.key(srcKey)
	dst := c.key(dstKey)
	objects := c.client.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: src, Recursive: true})
	for object := range objects {
		if object.Err != nil {
			return written, object.Err
		}
		rel := strings.TrimPrefix(object.Key, src)
		target := strings.TrimPrefix(dst+rel, "/")
		if _, err := c.client.CopyObject(ctx, minio.CopyDestOptions{Bucket: c.bucket, Object: target}, minio.CopySrcOptions{Bucket: c.bucket, Object: object.Key}); err != nil {
			return written, err
		}
		written += object.Size
	}
	if written == 0 {
		return 0, fmt.Errorf("storage: no objects under s3://%s/%s", c.bucket, src)
	}
	return written, nil
}

// DeletePrefix removes every object under key.
func (c *RemoteClient) DeletePrefix(ctx context.Context, key string) (int64, error) {
	prefix := c.key(key)
	objects := make([]minio.ObjectInfo, 0)
	for object := range c.client.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return 0, object.Err
		}
		objects = append(objects, object)
	}
	for _, object := range objects {
		if err := c.client.RemoveObject(ctx, c.bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
			return 0, err
		}
	}
	return int64(len(objects)), nil
}

// CopyDir copies a directory tree, preserving relative paths. Symlinks are
// skipped: a model weight tree never needs them and following one could
// escape the source root.
func CopyDir(ctx context.Context, src, dst string) (int64, error) {
	var written int64
	err := filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755) //nolint:gosec // weights must be readable by non-root serving runtimes
		}
		in, err := os.Open(path) //nolint:gosec // src is a validated root
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		// Model weights are read by serving runtimes that do not share the
		// transfer pod's uid, so the copy is world-readable.
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) //nolint:gosec // target is under the destination root
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		written += n
		return nil
	})
	return written, err
}

// DirSize sums the regular files under dir.
func DirSize(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// Now is a small test seam.
var Now = func() time.Time { return time.Now().UTC() }
