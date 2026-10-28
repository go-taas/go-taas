package image

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/registry"
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
		{"accelerator with bad syntax", &imagev1.RegisterImageRequest{Name: "ghcr.io/go-taas/vllm", Tag: "v1", Accelerator: "TPU/../x", Engine: "vllm"}, apierrors.CodeImageIncompatible},
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

// fakeImporter records the import call and returns a configurable error.
type fakeImporter struct {
	srcRef string
	dstRef string
	err    error
}

func (f *fakeImporter) Import(_ context.Context, srcRef string, _ *registry.Credentials, dstRef string, _ *registry.Credentials) error {
	f.srcRef = srcRef
	f.dstRef = dstRef
	return f.err
}

// setTestHarbor installs a package-level Harbor config for the import
// tests.
func setTestHarbor(url, user, pass, project string) {
	cfg := config.GetConfig()
	if cfg == nil {
		cfg = &config.Configuration{}
	}
	cfg.Image.Harbor = config.HarborConfig{URL: url, Username: user, Password: pass, Project: project}
	config.SetConfigForTest(cfg)
}

func TestServiceImportImageHappyPath(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	imp := &fakeImporter{}
	svc.SetImporter(imp)

	resp, err := svc.ImportImage(ctx, &imagev1.ImportImageRequest{
		SourceReference: "docker.1ms.run/library/vllm:v0.6.3",
		Accelerator:     "nvidia",
		Engine:          "vllm",
		Description:     "imported vLLM",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetImageId())
	// The destination reference lands in the internal Harbor project.
	assert.Equal(t, "hub.example.com/taas/vllm:v0.6.3", resp.GetReference())
	assert.Equal(t, "docker.1ms.run/library/vllm:v0.6.3", imp.srcRef)
	assert.Equal(t, "hub.example.com/taas/vllm:v0.6.3", imp.dstRef)

	// The catalog row carries the Harbor reference as its name.
	found, err := svc.repo.FindByID(ctx, resp.GetImageId())
	require.NoError(t, err)
	assert.Equal(t, "hub.example.com/taas/vllm", found.Name)
	assert.Equal(t, "v0.6.3", found.Tag)
	assert.Equal(t, "nvidia", found.Accelerator)
}

func TestServiceImportImageValidation(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	svc.SetImporter(&fakeImporter{})

	// Empty source reference.
	_, err := svc.ImportImage(ctx, &imagev1.ImportImageRequest{Accelerator: "nvidia", Engine: "vllm"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageReferenceInvalid, ae.Code)

	// Accelerator with invalid syntax (the usable set is the live fleet's
	// vendors plus the CPU marker, validated syntactically).
	_, err = svc.ImportImage(ctx, &imagev1.ImportImageRequest{SourceReference: "vllm:latest", Accelerator: "TPU/../x", Engine: "vllm"})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageIncompatible, ae.Code)
}

func TestServiceImportImageNotConfigured(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()
	// No Harbor configured.
	setTestHarbor("", "", "", "taas")

	_, err := svc.ImportImage(ctx, &imagev1.ImportImageRequest{SourceReference: "vllm:latest", Accelerator: "nvidia", Engine: "vllm"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageReferenceInvalid, ae.Code)

	// Harbor configured but no importer wired.
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	_, err = svc.ImportImage(ctx, &imagev1.ImportImageRequest{SourceReference: "vllm:latest", Accelerator: "nvidia", Engine: "vllm"})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageReferenceInvalid, ae.Code)
}

func TestServiceImportImageFailure(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	svc.SetImporter(&fakeImporter{err: errors.New("manifest unknown")})

	_, err := svc.ImportImage(ctx, &imagev1.ImportImageRequest{SourceReference: "vllm:latest", Accelerator: "nvidia", Engine: "vllm"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageReferenceInvalid, ae.Code)
	assert.Contains(t, ae.Error(), "manifest unknown")
}

func TestServiceImportImageDuplicate(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	svc.SetImporter(&fakeImporter{})

	_, err := svc.ImportImage(ctx, &imagev1.ImportImageRequest{SourceReference: "vllm:latest", Accelerator: "nvidia", Engine: "vllm"})
	require.NoError(t, err)
	// Importing the same source again is a duplicate.
	_, err = svc.ImportImage(ctx, &imagev1.ImportImageRequest{SourceReference: "vllm:latest", Accelerator: "nvidia", Engine: "vllm"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageExists, ae.Code)
}

// fakeNodeMatcher reports a fixed match count and the available labels.
type fakeNodeMatcher struct {
	matched   int
	available []string
	err       error
}

func (f fakeNodeMatcher) MatchNodes(context.Context, map[string]string) (int, []string, error) {
	return f.matched, f.available, f.err
}

func TestServiceTriggerWarmupRejectsUnmatchedSelector(t *testing.T) {
	svc, img := newServiceForTest(t)
	svc.SetNodeMatcher(fakeNodeMatcher{
		matched:   0,
		available: []string{"nvidia.com/gpu.product=NVIDIA-GeForce-RTX-4090"},
	})

	_, err := svc.TriggerWarmup(context.Background(), &imagev1.TriggerWarmupRequest{
		ImageId:      img.ID,
		NodeSelector: map[string]string{"nvidia.com": "NVIDIA-GeForce-RTX-4090"},
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageIncompatible, ae.Code)
	assert.Contains(t, ae.Error(), "nvidia.com/gpu.product=NVIDIA-GeForce-RTX-4090",
		"the error names the labels the cluster actually carries")

	// No task row is created for a rejected selector.
	_, total, lerr := svc.tasks.ListByImageID(context.Background(), img.ID, 0, 100)
	require.NoError(t, lerr)
	assert.EqualValues(t, 0, total)
}

func TestServiceTriggerWarmupAcceptsMatchedSelector(t *testing.T) {
	svc, img := newServiceForTest(t)
	svc.SetNodeMatcher(fakeNodeMatcher{matched: 1})

	// The publish still fails (no MQ in tests) but the selector passed.
	_, err := svc.TriggerWarmup(context.Background(), &imagev1.TriggerWarmupRequest{
		ImageId:      img.ID,
		NodeSelector: map[string]string{"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090"},
	})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInternal, ae.Code, "the validation did not reject the selector")
}

func TestServiceTriggerWarmupEmptySelectorSkipsValidation(t *testing.T) {
	svc, img := newServiceForTest(t)
	// A matcher that would reject everything must not be consulted for an
	// empty selector (all nodes).
	svc.SetNodeMatcher(fakeNodeMatcher{matched: 0})

	_, err := svc.TriggerWarmup(context.Background(), &imagev1.TriggerWarmupRequest{ImageId: img.ID})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInternal, ae.Code)
}

// TestRecoverInterruptedWarmupTasks verifies a task a previous process left
// active is failed at startup, so the active-task gate no longer blocks new
// warmups for the image.
func TestRecoverInterruptedWarmupTasks(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	task := &WarmupTask{ImageID: img.ID, State: TaskStateRunning}
	require.NoError(t, svc.tasks.Create(ctx, task))

	// A new warmup is blocked while the stale task is active.
	_, err := svc.TriggerWarmup(ctx, &imagev1.TriggerWarmupRequest{ImageId: img.ID})
	require.Error(t, err)

	n, err := svc.tasks.RecoverInterruptedTasks(ctx, "interrupted by a platform restart")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	rows, _, err := svc.tasks.ListByImageID(ctx, img.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, TaskStateFailed, rows[0].State)
	require.NotNil(t, rows[0].FailureReason)
	assert.Contains(t, *rows[0].FailureReason, "interrupted")

	// The gate is free again (the retry fails at publish, not at the gate).
	_, err = svc.TriggerWarmup(ctx, &imagev1.TriggerWarmupRequest{ImageId: img.ID})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInternal, ae.Code)
}

// fakePlatformGuard answers the platform-administrator gate.
type fakePlatformGuard struct {
	platform bool
}

func (f fakePlatformGuard) RequirePlatformAdmin(context.Context) error {
	if !f.platform {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

// TestServiceImageMutationsPlatformAdminOnly verifies the image catalog
// is a platform-owned asset: with the platform gate wired an
// organization administrator cannot register, edit or delete images, and
// a platform administrator can.
func TestServiceImageMutationsPlatformAdminOnly(t *testing.T) {
	svc, img := newServiceForTest(t)
	ctx := context.Background()

	svc.SetPlatformAdminGuard(fakePlatformGuard{platform: false})
	_, err := svc.RegisterImage(ctx, &imagev1.RegisterImageRequest{
		Name: "ghcr.io/go-taas/sglang", Tag: "v0.1.4", Accelerator: "nvidia", Engine: "sglang",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
	_, err = svc.UpdateImage(ctx, &imagev1.UpdateImageRequest{ImageId: img.ID, Description: "x"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
	_, err = svc.DeleteImage(ctx, &imagev1.DeleteImageRequest{ImageId: img.ID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	svc.SetPlatformAdminGuard(fakePlatformGuard{platform: true})
	_, err = svc.UpdateImage(ctx, &imagev1.UpdateImageRequest{ImageId: img.ID, Description: "edited"})
	require.NoError(t, err)
	found, err := svc.repo.FindByID(ctx, img.ID)
	require.NoError(t, err)
	assert.Equal(t, "edited", found.Description)
}

// TestServiceCompatibilityMatrixPlatformAdminOnly verifies the matrix is
// part of the platform-owned image catalog: an organization
// administrator cannot edit a cell.
func TestServiceCompatibilityMatrixPlatformAdminOnly(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()

	svc.SetPlatformAdminGuard(fakePlatformGuard{platform: false})
	_, err := svc.SetCompatibilityStatus(ctx, &imagev1.SetCompatibilityStatusRequest{
		ModelId: "m1", Engine: "vllm", CardType: "nvidia", Status: "supported",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestServiceRegisterImageCPU verifies a CPU-only image registers: an image
// that needs no accelerator is a first-class entry, not an error.
func TestServiceRegisterImageCPU(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()

	resp, err := svc.RegisterImage(ctx, &imagev1.RegisterImageRequest{
		Name: "ghcr.io/go-taas/embedding", Tag: "v1", Accelerator: "cpu", Engine: "tei",
	})
	require.NoError(t, err)
	stored, err := svc.repo.FindByID(ctx, resp.GetImageId())
	require.NoError(t, err)
	assert.Equal(t, AcceleratorCPU, stored.Accelerator)

	// Omitting the accelerator means the same thing.
	empty, err := svc.RegisterImage(ctx, &imagev1.RegisterImageRequest{
		Name: "ghcr.io/go-taas/tokenizer", Tag: "v1", Engine: "tokenizer",
	})
	require.NoError(t, err)
	stored, err = svc.repo.FindByID(ctx, empty.GetImageId())
	require.NoError(t, err)
	assert.Equal(t, AcceleratorCPU, stored.Accelerator)
}

// TestIsValidImageAccelerator verifies the acceptance rule: the CPU marker,
// an empty value, and any syntactically valid vendor name (so a fleet that
// reports a new vendor needs no code change), while anything that would
// break an image reference is refused.
func TestIsValidImageAccelerator(t *testing.T) {
	// A vendor is normalized to lowercase, so an uppercase spelling is
	// accepted and stored lowercased by the caller.
	for _, ok := range []string{"", "cpu", "nvidia", "TPU", "cambricon", "a-new-vendor"} {
		assert.True(t, IsValidImageAccelerator(ok), "%q must be accepted", ok)
	}
	// Anything that would break the reference or exceed the column is out.
	for _, bad := range []string{"gp u", "x/y", "x:y", "x;y", strings.Repeat("a", 33)} {
		assert.False(t, IsValidImageAccelerator(bad), "%q must be refused", bad)
	}
}

// TestServiceImportImageFlattensPath verifies the import keeps only the
// image's last path segment: docker.io/library/a/b/hello:1.0.0 becomes
// <harbor>/<project>/hello:1.0.0, so no source namespace tree is mirrored.
func TestServiceImportImageFlattensPath(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	imp := &fakeImporter{}
	svc.SetImporter(imp)

	resp, err := svc.ImportImage(ctx, &imagev1.ImportImageRequest{
		SourceReference: "docker.io/library/a/b/hello:1.0.0",
		Accelerator:     "cpu",
		Engine:          "custom",
	})
	require.NoError(t, err)
	assert.Equal(t, "hub.example.com/taas/hello:1.0.0", resp.GetReference())
	assert.Equal(t, "docker.io/library/a/b/hello:1.0.0", imp.srcRef)
	assert.Equal(t, "hub.example.com/taas/hello:1.0.0", imp.dstRef)

	stored, err := svc.repo.FindByID(ctx, resp.GetImageId())
	require.NoError(t, err)
	assert.Equal(t, "hub.example.com/taas/hello", stored.Name)
	assert.Equal(t, AcceleratorCPU, stored.Accelerator)
}

// TestServiceGetImageRegistryConfig reports the configured Harbor registry so
// the console shows the real host and project.
func TestServiceGetImageRegistryConfig(t *testing.T) {
	svc, _ := newServiceForTest(t)
	ctx := context.Background()

	// Unconfigured: the import action is unavailable and nothing is invented.
	setTestHarbor("", "", "", "")
	resp, err := svc.GetImageRegistryConfig(ctx, &imagev1.GetImageRegistryConfigRequest{})
	require.NoError(t, err)
	assert.False(t, resp.GetConfigured())
	assert.Empty(t, resp.GetHostname())
	assert.Empty(t, resp.GetProject())

	// Configured: host, project and the reference shape the import produces.
	setTestHarbor("hub.example.com", "admin", "secret", "taas")
	resp, err = svc.GetImageRegistryConfig(ctx, &imagev1.GetImageRegistryConfigRequest{})
	require.NoError(t, err)
	assert.True(t, resp.GetConfigured())
	assert.Equal(t, "hub.example.com", resp.GetHostname())
	assert.Equal(t, "taas", resp.GetProject())
	assert.Equal(t, "hub.example.com/taas/<image>:<tag>", resp.GetExampleReference())
}
