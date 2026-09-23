package image

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func seedRegistry(t *testing.T) {
	t.Helper()
	restore := ResetForTest([]*Summary{
		{ImageID: "img-vllm-nvidia", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"},
		{ImageID: "img-vllm-iluvatar", Name: "ghcr.io/go-taas/vllm-iluvatar", Tag: "v0.6.3", Accelerator: "iluvatar", Engine: "vllm"},
		{ImageID: "img-sglang-metax", Name: "ghcr.io/go-taas/sglang", Tag: "v0.1.4", Accelerator: "metax", Engine: "sglang"},
	})
	t.Cleanup(restore)
}

func TestRegistryLookup(t *testing.T) {
	seedRegistry(t)

	s, err := Lookup("img-vllm-nvidia")
	require.NoError(t, err)
	assert.Equal(t, "ghcr.io/go-taas/vllm", s.Name)
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", s.Reference())
	assert.Equal(t, "vllm", s.Engine)

	_, err = Lookup("img-missing")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)
}

func TestRegistryListFilters(t *testing.T) {
	seedRegistry(t)

	all := List("", "")
	assert.Len(t, all, 3)

	nvidia := List("nvidia", "")
	require.Len(t, nvidia, 1)
	assert.Equal(t, "img-vllm-nvidia", nvidia[0].ImageID)

	vllm := List("", "vllm")
	assert.Len(t, vllm, 2)

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

func TestServiceListImages(t *testing.T) {
	seedRegistry(t)
	svc := New(nil)

	resp, err := svc.ListImages(context.Background(), &imagev1.ListImagesRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.GetImages(), 3)
	assert.Equal(t, int64(3), resp.GetPageMeta().GetTotal())

	resp, err = svc.ListImages(context.Background(), &imagev1.ListImagesRequest{Accelerator: "nvidia"})
	require.NoError(t, err)
	require.Len(t, resp.GetImages(), 1)
	assert.Equal(t, "img-vllm-nvidia", resp.GetImages()[0].GetImageId())
	assert.Equal(t, "v0.6.3", resp.GetImages()[0].GetTag())
}

func TestServiceRegisterImageNotImplemented(t *testing.T) {
	svc := New(nil)
	_, err := svc.RegisterImage(context.Background(), &imagev1.RegisterImageRequest{})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageExists, ae.Code)
}

func TestServiceTriggerWarmup(t *testing.T) {
	seedRegistry(t)
	svc := New(nil)
	ctx := context.Background()

	// Unknown image: 10201.
	_, err := svc.TriggerWarmup(ctx, &imagev1.TriggerWarmupRequest{ImageId: "img-missing"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageNotFound, ae.Code)

	// Known image: warmup not implemented until feature #3.
	_, err = svc.TriggerWarmup(ctx, &imagev1.TriggerWarmupRequest{ImageId: "img-vllm-nvidia"})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeImageWarmupFailed, ae.Code)
}
