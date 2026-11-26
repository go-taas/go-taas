package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// RemoteClient wraps a MinIO/S3 client and its bucket. The core handle is
// kept alongside the high-level client so chunked (multipart) uploads can
// expose lower-level primitives that Client's high-level PutObject would not
// represent as separate, resumable parts.
type RemoteClient struct {
	client *minio.Client
	core   *minio.Core
	bucket string
	prefix string
}

// PartSize constants for the chunked upload contract.
const (
	// MinPartSize is S3/MinIO's lower bound for a multipart part.
	MinPartSize = int64(5) << 20
)

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
	core, err := minio.NewCore(endpoint, &minio.Options{
		Creds:        creds,
		Secure:       secure,
		Region:       ref.Region,
		BucketLookup: bucketLookup(ref.ForcePathStyle),
	})
	if err != nil {
		return nil, err
	}
	return &RemoteClient{client: client, core: core, bucket: ref.Bucket, prefix: strings.Trim(ref.Prefix, "/")}, nil
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

// uploadProgressInterval bounds how often an upload reports its byte count,
// so a fast object store does not turn every read into a progress report.
// The Job writes each report to a Kubernetes ConfigMap, which is the reason
// the interval is coarse.
const uploadProgressInterval = time.Second

// countingReader reports how many bytes have been read from it, at most
// once per interval and once more at EOF. It lets an object upload show
// progress inside one large file instead of jumping from 0 to its size.
type countingReader struct {
	reader   io.Reader
	interval time.Duration
	read     int64
	reported int64
	last     time.Time
	progress func(int64)
}

// Read implements io.Reader.
func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	if r.last.IsZero() {
		r.last = time.Now()
	}
	// Report the size at EOF even inside one throttle window, but never
	// report the same byte count twice.
	if r.progress != nil && (err == io.EOF || time.Since(r.last) >= r.interval) && r.read > r.reported {
		r.reported = r.read
		r.last = time.Now()
		r.progress(r.read)
	}
	return n, err
}

// PrefixBytes returns the summed size of the objects under key. A transfer
// that downloads a whole prefix uses it as the progress total, so the console
// shows "12 GB / 30 GB" instead of a bare byte count.
func (c *RemoteClient) PrefixBytes(ctx context.Context, key string) (int64, error) {
	prefix := c.key(key)
	var total int64
	for object := range c.client.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return 0, object.Err
		}
		total += object.Size
	}
	return total, nil
}

// UploadDir copies a local directory under key. It is used by the upload
// flow when the backend is an object store.
func (c *RemoteClient) UploadDir(ctx context.Context, dir, key string) (int64, error) {
	return c.UploadDirWithProgress(ctx, dir, key, nil)
}

// UploadDirWithProgress copies a local directory under key and calls
// progress with the cumulative bytes written. It is the upload counterpart
// of DownloadPrefix: a transfer that stages through the Job can report the
// object-store write instead of freezing at the download's 100%.
func (c *RemoteClient) UploadDirWithProgress(ctx context.Context, dir, key string, progress func(int64)) (int64, error) {
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
		in, err := os.Open(path) //nolint:gosec // path is under the validated destination root
		if err != nil {
			return err
		}
		base := written
		reader := &countingReader{
			reader:   in,
			interval: uploadProgressInterval,
			progress: func(n int64) {
				if progress != nil {
					progress(base + n)
				}
			},
		}
		upload, err := c.client.PutObject(ctx, c.bucket, objectKey, reader, info.Size(), minio.PutObjectOptions{})
		closeErr := in.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		written += upload.Size
		if progress != nil {
			progress(written)
		}
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

// BeginMultipartUpload opens a new multi-part upload for one object and
// returns its upload ID. The ID is the resume/cancel token the caller keeps,
// and the same ID is re-used across retries so an incomplete upload can be
// resumed instead of restarted.
func (c *RemoteClient) BeginMultipartUpload(ctx context.Context, key string) (string, error) {
	return c.core.NewMultipartUpload(ctx, c.bucket, c.key(key), minio.PutObjectOptions{})
}

// UploadPart sends one part of a multipart upload. partNumber is 1-based.
// It returns the etag, which must be handed back at completion time in the
// correct order. size caps the part read from r.
func (c *RemoteClient) UploadPart(ctx context.Context, key, uploadID string, partNumber int, r io.Reader, size int64) (string, error) {
	part, err := c.core.PutObjectPart(ctx, c.bucket, c.key(key), uploadID, partNumber, r, size, minio.PutObjectPartOptions{})
	if err != nil {
		return "", err
	}
	return part.ETag, nil
}

// ListUploadedParts returns the part numbers already present for an
// incomplete upload, so a client can resume from where it stopped instead of
// re-sending bytes. partsAreOrdered is true when the returned list is in
// ascending part-number order (guaranteed by S3 compatible stores).
func (c *RemoteClient) ListUploadedParts(ctx context.Context, key, uploadID string) ([]int, error) {
	var parts []int
	marker := 0
	for {
		result, err := c.core.ListObjectParts(ctx, c.bucket, c.key(key), uploadID, marker, 1000)
		if err != nil {
			return nil, err
		}
		for _, part := range result.ObjectParts {
			parts = append(parts, part.PartNumber)
			if part.PartNumber > marker {
				marker = part.PartNumber
			}
		}
		if !result.IsTruncated {
			break
		}
	}
	return parts, nil
}

// UploadedParts is the completion record of a multipart upload: the ordered
// list of (partNumber, etag) pairs the store committed.
type UploadedParts struct {
	Parts []UploadedPart `json:"parts"`
}

// UploadedPart identifies one committed multipart part.
type UploadedPart struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
}

// CompleteUploadPartList re-reads the committed parts of an upload and
// returns them in ascending order for CompleteMultipartUpload. It is the
// authoritative source of what to finish, so the caller never trusts the
// client for part etags.
func (c *RemoteClient) CompleteUploadPartList(ctx context.Context, key, uploadID string) (*UploadedParts, error) {
	var ordered []UploadedPart
	marker := 0
	for {
		result, err := c.core.ListObjectParts(ctx, c.bucket, c.key(key), uploadID, marker, 1000)
		if err != nil {
			return nil, err
		}
		// ListObjectParts isn't guaranteed to sort, so collect then sort.
		for _, part := range result.ObjectParts {
			ordered = append(ordered, UploadedPart{PartNumber: part.PartNumber, ETag: part.ETag})
			if part.PartNumber > marker {
				marker = part.PartNumber
			}
		}
		if !result.IsTruncated {
			break
		}
	}
	// Ascending part number is required by CompleteMultipartUpload.
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PartNumber < ordered[j].PartNumber })
	return &UploadedParts{Parts: ordered}, nil
}

// CompleteMultipartUpload concatenates the committed parts into one object.
func (c *RemoteClient) CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts *UploadedParts) error {
	complete := make([]minio.CompletePart, 0, len(parts.Parts))
	for _, part := range parts.Parts {
		complete = append(complete, minio.CompletePart{PartNumber: part.PartNumber, ETag: part.ETag})
	}
	_, err := c.core.CompleteMultipartUpload(ctx, c.bucket, c.key(key), uploadID, complete, minio.PutObjectOptions{})
	return err
}

// AbortMultipartUpload discards an incomplete multipart upload and its parts.
func (c *RemoteClient) AbortMultipartUpload(ctx context.Context, key, uploadID string) error {
	return c.core.AbortMultipartUpload(ctx, c.bucket, c.key(key), uploadID)
}

// StatObject returns the object metadata, used by the upload Job to size the
// bytes fetched before extraction.
func (c *RemoteClient) StatObject(ctx context.Context, key string) (minio.ObjectInfo, error) {
	return c.client.StatObject(ctx, c.bucket, c.key(key), minio.GetObjectOptions{})
}

// OpenObject opens one object for streaming reads. The caller must close the
// returned reader.
func (c *RemoteClient) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return c.client.GetObject(ctx, c.bucket, c.key(key), minio.GetObjectOptions{})
}
