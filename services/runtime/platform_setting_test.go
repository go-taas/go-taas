package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	runtimev1 "github.com/go-taas/go-taas/proto/taas/runtime/v1"
)

// TestGetPlatformSettingsDefaults covers the empty installation: no row
// yet, so the read succeeds with https and no hostname, and the composed
// base URL stays empty rather than advertising a gateway that does not
// exist.
func TestGetPlatformSettingsDefaults(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)

	resp, err := svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	require.NoError(t, err)
	settings := resp.GetSettings()
	require.NotNil(t, settings)
	assert.Equal(t, "", settings.GetInferenceGatewayHostname())
	assert.Equal(t, GatewaySchemeHTTPS, settings.GetInferenceGatewayScheme())
	assert.Equal(t, "", settings.GetInferenceBaseUrl())
}

// TestUpdatePlatformSettingsPersists covers the round trip and the
// normalisation: the hostname is lowercased, a trailing dot is dropped,
// and the composed base URL follows the stored scheme.
func TestUpdatePlatformSettingsPersists(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)

	resp, err := svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname: "API.Sudoinfotech.com.",
	})
	require.NoError(t, err)
	assert.Equal(t, "api.sudoinfotech.com", resp.GetSettings().GetInferenceGatewayHostname())
	assert.Equal(t, "https://api.sudoinfotech.com/v1", resp.GetSettings().GetInferenceBaseUrl())

	// The value survives a fresh read.
	got, err := svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	require.NoError(t, err)
	assert.Equal(t, "api.sudoinfotech.com", got.GetSettings().GetInferenceGatewayHostname())

	// A port and an http scheme are kept as given.
	_, err = svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname:    "api.example.com:8443",
		InferenceGatewayScheme:      "http",
		InferenceGatewayInternalUrl: "http://gateway.aibrix-system.svc:80/",
	})
	require.NoError(t, err)
	got, err = svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	require.NoError(t, err)
	assert.Equal(t, "http://api.example.com:8443/v1", got.GetSettings().GetInferenceBaseUrl())
	assert.Equal(t, "http://gateway.aibrix-system.svc:80", got.GetSettings().GetInferenceGatewayInternalUrl())

	// Clearing the hostname drops the public URL.
	_, err = svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{})
	require.NoError(t, err)
	got, err = svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	require.NoError(t, err)
	assert.Equal(t, "", got.GetSettings().GetInferenceBaseUrl())
}

// TestUpdatePlatformSettingsRejectsBadValues keeps a rejected value from
// being stored: the operator sees the reason and the installation keeps
// pointing at the previous gateway.
func TestUpdatePlatformSettingsRejectsBadValues(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	_, err := svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname: "api.example.com",
	})
	require.NoError(t, err)

	cases := []struct {
		name string
		req  *runtimev1.UpdatePlatformSettingsRequest
		want string
	}{
		{
			name: "scheme in the hostname",
			req:  &runtimev1.UpdatePlatformSettingsRequest{InferenceGatewayHostname: "https://api.example.com"},
			want: "must not include a scheme",
		},
		{
			name: "path in the hostname",
			req:  &runtimev1.UpdatePlatformSettingsRequest{InferenceGatewayHostname: "api.example.com/v1"},
			want: "plain host name",
		},
		{
			name: "space in the hostname",
			req:  &runtimev1.UpdatePlatformSettingsRequest{InferenceGatewayHostname: "api example.com"},
			want: "plain host name",
		},
		{
			name: "invalid characters",
			req:  &runtimev1.UpdatePlatformSettingsRequest{InferenceGatewayHostname: "api_!.example.com"},
			want: "not a valid host name",
		},
		{
			name: "unknown scheme",
			req:  &runtimev1.UpdatePlatformSettingsRequest{InferenceGatewayHostname: "api.example.com", InferenceGatewayScheme: "grpc"},
			want: "must be http or https",
		},
		{
			name: "internal URL without a scheme",
			req:  &runtimev1.UpdatePlatformSettingsRequest{InferenceGatewayInternalUrl: "gateway:80"},
			want: "must start with http://",
		},
		{
			name: "internal URL with a path",
			req:  &runtimev1.UpdatePlatformSettingsRequest{InferenceGatewayInternalUrl: "http://gateway:80/v1"},
			want: "without a path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.UpdatePlatformSettings(context.Background(), tc.req)
			require.Error(t, err)
			assert.EqualValues(t, apierrors.CodeRuntimeSettingInvalid, apierrors.CodeOf(err))
			assert.Contains(t, err.Error(), tc.want)

			// The stored value is untouched.
			got, err := svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
			require.NoError(t, err)
			assert.Equal(t, "api.example.com", got.GetSettings().GetInferenceGatewayHostname())
		})
	}
}

// TestUpdatePlatformSettingsIsAudited records who changed the data-plane
// endpoint, because it decides where every tenant's traffic goes.
func TestUpdatePlatformSettingsIsAudited(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	recorder := &fakeAudit{}
	svc.SetAuditRecorder(recorder)
	svc.SetSessionUserResolver(fakeSessionUser{user: "user-1"})

	_, err := svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname: "api.example.com",
	})
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	ev := recorder.events[0]
	assert.Equal(t, "runtime.platform_settings_changed", ev.Action)
	assert.Equal(t, "user-1", ev.ActorUserID)
	assert.Contains(t, ev.Metadata, "api.example.com")
}

// TestGetPlatformSettingsRequiresAdmin keeps the setting operator-only.
func TestGetPlatformSettingsRequiresAdmin(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	svc.SetSessionOrgResolver(fakeSessionOrg{org: "org-a"})
	svc.SetSessionUserResolver(fakeSessionUser{user: "user-1"})

	_, err := svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	_, err = svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname: "api.example.com",
	})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestInferenceGatewaySeam proves the infer module's seam reads the same
// row the console writes.
func TestInferenceGatewaySeam(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	_, err := svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname:    "api.example.com",
		InferenceGatewayScheme:      "https",
		InferenceGatewayInternalUrl: "http://10.43.253.124",
	})
	require.NoError(t, err)

	hostname, scheme, internalURL, err := svc.InferenceGateway(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "api.example.com", hostname)
	assert.Equal(t, "https", scheme)
	assert.Equal(t, "http://10.43.253.124", internalURL)
}
