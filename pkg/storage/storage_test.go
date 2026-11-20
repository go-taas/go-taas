package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeLegacyDefaults(t *testing.T) {
	cfg := Config{}.Normalize("model-weights", "juicefs-platform-shared", "/data/weights")
	require.True(t, cfg.Enabled())
	assert.Equal(t, "default", cfg.DefaultBackend)
	backend, err := cfg.Resolve("")
	require.NoError(t, err)
	assert.Equal(t, TypeFilesystem, backend.Type)
	assert.Equal(t, "model-weights", backend.Filesystem.ExistingClaim)
	assert.Equal(t, "juicefs-platform-shared", backend.Filesystem.Provision.StorageClass)
	assert.True(t, backend.Filesystem.Provision.Enabled)
	assert.Equal(t, ModeMount, backend.Delivery.Default)
	uri, err := backend.URI("Qwen/Qwen2.5-7B")
	require.NoError(t, err)
	assert.Equal(t, "pvc://model-weights/Qwen/Qwen2.5-7B", uri)
}

func TestNormalizeDefaultsMountPathAndDelivery(t *testing.T) {
	cfg := Config{
		DefaultBackend: "s3",
		Backends: map[string]Backend{
			"s3": {
				Type:        TypeObjectStore,
				ObjectStore: ObjectStore{Bucket: "models", Prefix: "weights"},
			},
		},
	}.Normalize("", "", "")
	backend, err := cfg.Resolve("")
	require.NoError(t, err)
	assert.Equal(t, "/data/weights", backend.Filesystem.MountPath)
	assert.Equal(t, ModeCache, backend.Delivery.Default)
	assert.True(t, backend.Supports(ModeCache))
	uri, err := backend.URI("Qwen/Qwen2.5-7B")
	require.NoError(t, err)
	assert.Equal(t, "s3://models/weights/Qwen/Qwen2.5-7B", uri)
}

func TestKeyRejectsUnsafePaths(t *testing.T) {
	for _, key := range []string{"", "/abs", "../x", "a/../../b", "a\\b", "a//b", "./a", "a/./b", "a\x00b"} {
		_, err := Key(key)
		assert.Error(t, err, "key %q must be rejected", key)
	}
	got, err := Key("Qwen/Qwen2.5-7B/")
	require.NoError(t, err)
	assert.Equal(t, "Qwen/Qwen2.5-7B", got)
}

func TestCleanJoin(t *testing.T) {
	got, err := CleanJoin("/data/weights", "org/model/v1")
	require.NoError(t, err)
	assert.Equal(t, "/data/weights/org/model/v1", got)
	_, err = CleanJoin("/data/weights", "../etc/passwd")
	assert.Error(t, err)
}

func TestValidateRules(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "default missing",
			cfg: Config{DefaultBackend: "missing", Backends: map[string]Backend{
				"fs": {Type: TypeFilesystem, Filesystem: Filesystem{ExistingClaim: "pvc"},
					Delivery: Delivery{Default: ModeMount, Allowed: []string{ModeMount}}},
			}},
			want: "not in storage.backends",
		},
		{
			name: "filesystem without claim",
			cfg: Config{DefaultBackend: "fs", Backends: map[string]Backend{
				"fs": {Type: TypeFilesystem, Delivery: Delivery{Default: ModeMount, Allowed: []string{ModeMount}}},
			}},
			want: "existingClaim or provision.enabled",
		},
		{
			name: "objectstore without bucket",
			cfg: Config{DefaultBackend: "s3", Backends: map[string]Backend{
				"s3": {Type: TypeObjectStore, Delivery: Delivery{Default: ModeCache, Allowed: []string{ModeCache}}},
			}},
			want: "bucket is required",
		},
		{
			name: "objectstore mount without class",
			cfg: Config{DefaultBackend: "s3", Backends: map[string]Backend{
				"s3": {Type: TypeObjectStore, ObjectStore: ObjectStore{Bucket: "b", Endpoint: "e", Credentials: Credentials{ExistingSecret: "s"}},
					Delivery: Delivery{Default: ModeMount, Allowed: []string{ModeMount}}},
			}},
			want: "storageClass",
		},
		{
			name: "default not allowed",
			cfg: Config{DefaultBackend: "fs", Backends: map[string]Backend{
				"fs": {Type: TypeFilesystem, Filesystem: Filesystem{ExistingClaim: "pvc"},
					Delivery: Delivery{Default: ModeCache, Allowed: []string{ModeMount}}},
			}},
			want: "not in delivery.allowed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidateAcceptsObjectStoreCache(t *testing.T) {
	cfg := Config{
		DefaultBackend: "s3",
		Backends: map[string]Backend{
			"s3": {
				Type:        TypeObjectStore,
				ObjectStore: ObjectStore{Bucket: "models", Endpoint: "https://minio:9000", Credentials: Credentials{ExistingSecret: "creds"}},
				Delivery:    Delivery{Default: ModeCache, Allowed: []string{ModeCache, ModeDownload}, Download: DeliveryDownload{Enabled: true}},
			},
		},
	}
	assert.NoError(t, cfg.Validate())
}

func TestCredentialEnvNames(t *testing.T) {
	access, secret := CredentialEnvNames("minio")
	assert.Equal(t, "TAAS_STORAGE_MINIO_ACCESS_KEY", access)
	assert.Equal(t, "TAAS_STORAGE_MINIO_SECRET_KEY", secret)

	// Names are data, so punctuation and case must not produce an invalid
	// environment variable or collide between a dash and a dot.
	access, secret = CredentialEnvNames("archive-eu.1")
	assert.Equal(t, "TAAS_STORAGE_ARCHIVE_EU_1_ACCESS_KEY", access)
	assert.Equal(t, "TAAS_STORAGE_ARCHIVE_EU_1_SECRET_KEY", secret)

	access, _ = CredentialEnvNames("   ")
	assert.Equal(t, "TAAS_STORAGE_DEFAULT_ACCESS_KEY", access)
}
