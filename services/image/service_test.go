package image

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// newServiceForTest builds a service bound to a disposable database
// with one registered image.
func newServiceForTest(t *testing.T) (*Service, *Image) {
	t.Helper()
	db := newImageTestDB(t)
	svc := NewWithRepositories(NewRepository(db), NewWarmupTaskRepository(db))
	wireRegistry(db)
	ctx := context.Background()
	img := &Image{Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm", Description: "vLLM engine"}
	require.NoError(t, svc.repo.CreateImage(ctx, img))
	return svc, img
}

func TestServiceRegisterImageValidation(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()

	cases := []struct {
		name string
		req  *imagev1.RegisterImageRequest
		code apierrors.Code
	}{
		{"empty name", &imagev1.RegisterImageRequest{Name: "", Tag: "v1", Accelerator: "nvidia", Engine: "vllm"}, apierrors.CodeImageReferenceInvalid},
		{"name with tag separator", &imagev1.RegisterImageRequest{Name: "ghcr.io/go-taas/vllm:v0.6.3", Tag: "v1", Accelerator: "nvidia", Engine: "vllm"}, apierrors.CodeImageReferenceInvalid},
		{"uppercase name", &imagev1.RegisterImageRequest{Name: "GHCR.IO/vllm", Tag: "v1", Accelerator: "nvidia", Engine: "vllm"}, apierrors.CodeImageReferenceInvalid},
		{"empty tag", &imagev1.RegisterImageRequest{Name: "ghcr.io/go-taas/vllm", Tag: "", Accelerator: "nvidia", Engine: "vllm"}, apierrors.CodeImageReferenceInvalid},
		{"tag with whitespace", &imagev1.RegisterImageRequest{Name: "ghcr.io/go-taas/vllm", Tag: "v0.6 3", Accelerator: "nvidia", Engine: "vllm"}, apierrors.CodeImageReferenceInvalid},
		{"bad digest", &imagev1.RegisterImageRequest{Name: "ghcr.io/go-taas/vllm", Tag: "v1", Digest: "sha256:short", Accelerator: "nvidia", Engine: "vllm"}, apierrors.CodeImageDigestInvalid},
		{"unsupported accelerator", &imagev1.RegisterImageRequest{Name: "ghcr.io/go-taas/vllm", Tag: "v1", Accelerator: "tpu", Engine: "vllm"}, apierrors.CodeImageIncompatible},
		{"empty engine", &imagev1.RegisterImageRequest{Name: "ghcr.io/go-taas/vllm", Tag: "v1", Accelerator: "nvidia", Engine: ""}, apierrors.CodeImageReferenceInvalid},
		{"long description", &imagev1.RegisterImageRequest{Name: "ghcr.io/go-taas/vllm", Tag: "v1", Accelerator: "nvidia", Engine: "vllm", Description: string(make([]byte, 1025))}, apierrors.CodeImageReferenceInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RegisterImage(ctx, tc.req)
			require.Error(t, err)
			ae, ok := apierrors.As(err)
			require.True(t, ok)
			assert.Equal(t, tc.code, ae.Code)
		})
	}
}

func TestServiceRegisterImageHappyPath(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()

	resp, err := svc.RegisterImage(ctx, &imagev1.RegisterImageRequest{
		Name: "ghcr.io/go-taas/sglang", Tag: "v0.1.4", Accelerator: "NVIDIA", Engine: "sglang", Description: "SGLang engine",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetImageId())

	// Accelerator is normalized to lowercase.
	found, err := svc.repo.FindByID(ctx, resp.GetImageId())
	require.NoError(t, err)
	assert.Equal(t, "nvidia", found.Accelerator)
	assert.Equal(t, "SGLang engine", found.Description)
}

func TestServiceRegisterImageDuplicateTriple(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	_, err := svc.RegisterImage(ctx, &imagev1.RegisterImageRequest{
		Name: img.Name, Tag: img.Tag, Accelerator: img.Accelerator, Engine: img.Engine,
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageExists, ae.Code)
}

func TestServiceListImages(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	resp, err := svc.ListImages(ctx, &imagev1.ListImagesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetImages(), 1)
	assert.Equal(t, int64(1), resp.GetPageMeta().GetTotal())
	assert.Equal(t, img.ID, resp.GetImages()[0].GetImageId())
	assert.Equal(t, "vLLM engine", resp.GetImages()[0].GetDescription())

	resp, err = svc.ListImages(ctx, &imagev1.ListImagesRequest{Accelerator: "metax"})
	require.NoError(t, err)
	assert.Empty(t, resp.GetImages())
}

func TestServiceListImagesInUseCount(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()
	svc.SetInUseProvider(func(_ context.Context, imageID string) ([]*imagev1.InUseService, error) {
		if imageID == img.ID {
			return []*imagev1.InUseService{{ServiceId: "svc-1", Name: "demo", State: "running"}}, nil
		}
		return nil, nil
	})

	resp, err := svc.ListImages(ctx, &imagev1.ListImagesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetImages(), 1)
	assert.Equal(t, int64(1), resp.GetImages()[0].GetInUseCount())
}

func TestServiceGetImage(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	resp, err := svc.GetImage(ctx, &imagev1.GetImageRequest{ImageId: img.ID})
	require.NoError(t, err)
	assert.Equal(t, img.ID, resp.GetImage().GetImageId())
	assert.Empty(t, resp.GetInUseServices())

	// Unknown image: 10201.
	_, err = svc.GetImage(ctx, &imagev1.GetImageRequest{ImageId: "img-missing"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestServiceGetImageInUseServices(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()
	svc.SetInUseProvider(func(_ context.Context, _ string) ([]*imagev1.InUseService, error) {
		return []*imagev1.InUseService{{ServiceId: "svc-1", Name: "demo", State: "running"}}, nil
	})

	resp, err := svc.GetImage(ctx, &imagev1.GetImageRequest{ImageId: img.ID})
	require.NoError(t, err)
	require.Len(t, resp.GetInUseServices(), 1)
	assert.Equal(t, "demo", resp.GetInUseServices()[0].GetName())
	assert.Equal(t, int64(1), resp.GetImage().GetInUseCount())
}

func TestServiceUpdateImage(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	resp, err := svc.UpdateImage(ctx, &imagev1.UpdateImageRequest{ImageId: img.ID, Description: "updated"})
	require.NoError(t, err)
	found, err := svc.repo.FindByID(ctx, img.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated", found.Description)
	_ = resp

	// Description over 1024 chars: 10207.
	_, err = svc.UpdateImage(ctx, &imagev1.UpdateImageRequest{ImageId: img.ID, Description: string(make([]byte, 1025))})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageReferenceInvalid, ae.Code)

	// Unknown image: 10201.
	_, err = svc.UpdateImage(ctx, &imagev1.UpdateImageRequest{ImageId: "img-missing"})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestServiceDeleteImage(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	_, err := svc.DeleteImage(ctx, &imagev1.DeleteImageRequest{ImageId: img.ID})
	require.NoError(t, err)
	_, err = svc.repo.FindByID(ctx, img.ID)
	require.Error(t, err)

	// Unknown image: 10201.
	_, err = svc.DeleteImage(ctx, &imagev1.DeleteImageRequest{ImageId: "img-missing"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestServiceDeleteImageGuardBlocks(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()
	svc.SetDeleteGuard(func(_ context.Context, _ string) error {
		return apierrors.Newf(apierrors.CodeImageInUse, "referenced by inference service demo")
	})

	_, err := svc.DeleteImage(ctx, &imagev1.DeleteImageRequest{ImageId: img.ID})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageInUse, ae.Code)

	// The image is still there.
	_, err = svc.repo.FindByID(ctx, img.ID)
	require.NoError(t, err)
}

func TestServiceTriggerWarmupRequiresMQ(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	// Unknown image: 10201.
	_, err := svc.TriggerWarmup(ctx, &imagev1.TriggerWarmupRequest{ImageId: "img-missing"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)

	// No mq component wired: the task is created, the publish fails and
	// the compensation marks it failed; the RPC surfaces CodeInternal.
	_, err = svc.TriggerWarmup(ctx, &imagev1.TriggerWarmupRequest{ImageId: img.ID})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInternal, ae.Code)

	// The compensating mark-failed left a terminal task, so a retry is
	// not blocked by the active gate.
	tasks, total, err := svc.tasks.ListByImageID(ctx, img.ID, 0, 100)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	assert.Equal(t, TaskStateFailed, tasks[0].State)
}

func TestServiceListWarmupTasks(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	// Unknown image: 10201.
	_, err := svc.ListWarmupTasks(ctx, &imagev1.ListWarmupTasksRequest{ImageId: "img-missing"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)

	require.NoError(t, svc.tasks.Create(ctx, &WarmupTask{ImageID: img.ID, State: TaskStateSucceeded, NodeSelector: []byte(`{}`)}))

	resp, err := svc.ListWarmupTasks(ctx, &imagev1.ListWarmupTasksRequest{ImageId: img.ID})
	require.NoError(t, err)
	require.Len(t, resp.GetTasks(), 1)
	assert.Equal(t, int64(1), resp.GetPageMeta().GetTotal())
	assert.Equal(t, TaskStateSucceeded, resp.GetTasks()[0].GetState())
}

func TestServiceGetWarmupTask(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	task := &WarmupTask{ImageID: img.ID, State: TaskStateRunning, NodeSelector: []byte(`{}`)}
	require.NoError(t, svc.tasks.Create(ctx, task))

	resp, err := svc.GetWarmupTask(ctx, &imagev1.GetWarmupTaskRequest{TaskId: task.ID})
	require.NoError(t, err)
	assert.Equal(t, task.ID, resp.GetTask().GetTaskId())
	assert.Equal(t, TaskStateRunning, resp.GetTask().GetState())

	// Unknown task: 10201.
	_, err = svc.GetWarmupTask(ctx, &imagev1.GetWarmupTaskRequest{TaskId: "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestServiceMigrateSeedsEmptyTable(t *testing.T) {
	db := newImageTestDB(t)
	// Drop the seeded schema so Migrate creates it from scratch.
	require.NoError(t, db.Migrator().DropTable(&Image{}, &WarmupTask{}))

	svc := &Service{repo: NewRepository(db), tasks: NewWarmupTaskRepository(db)}
	// Point the package config at a seed list.
	t.Setenv("CONFIG_IMAGE_REGISTRY", "") // no-op; config is set directly below
	setTestImageRegistry([]config.ImageRegistryEntry{
		{ImageID: "img-seed-1", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"},
	})
	ctx := context.Background()
	require.NoError(t, svc.Migrate(ctx))

	count, err := svc.repo.CountAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// A second Migrate on the now non-empty table does not re-seed.
	setTestImageRegistry([]config.ImageRegistryEntry{
		{ImageID: "img-seed-1", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"},
		{ImageID: "img-seed-2", Name: "ghcr.io/go-taas/sglang", Tag: "v0.1.4", Accelerator: "metax", Engine: "sglang"},
	})
	require.NoError(t, svc.Migrate(ctx))
	count, err = svc.repo.CountAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestRegistryLookupAndList(t *testing.T) {
	_, img := newServiceForTest(t)

	s, err := Lookup(img.ID)
	require.NoError(t, err)
	assert.Equal(t, img.Name, s.Name)
	assert.Equal(t, img.Name+":"+img.Tag, s.Reference())

	_, err = Lookup("img-missing")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)

	all := List("", "")
	assert.Len(t, all, 1)
	nvidia := List("nvidia", "")
	assert.Len(t, nvidia, 1)
	none := List("nvidia", "sglang")
	assert.Empty(t, none)
}

func TestIsValidAccelerator(t *testing.T) {
	assert.True(t, IsValidAccelerator("nvidia"))
	assert.True(t, IsValidAccelerator("NVIDIA"))
	assert.True(t, IsValidAccelerator("iluvatar"))
	assert.True(t, IsValidAccelerator("metax"))
	assert.False(t, IsValidAccelerator("tpu"))
	assert.False(t, IsValidAccelerator(""))
}

// setTestImageRegistry installs a package-level config for the Migrate
// seed test. config.GetConfig returns the global; tests swap the
// image.registry list through the exported test hook.
func setTestImageRegistry(entries []config.ImageRegistryEntry) {
	cfg := config.GetConfig()
	if cfg == nil {
		cfg = &config.Configuration{}
	}
	cfg.Image.Registry = entries
	config.SetConfigForTest(cfg)
}
