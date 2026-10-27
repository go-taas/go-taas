package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
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

// fakeUserLabels resolves ids like the auth module does.
type fakeUserLabels map[string]string

func (f fakeUserLabels) ResolveUserLabels(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if label, ok := f[id]; ok {
			out[id] = label
		}
	}
	return out, nil
}

// TestPlatformSettingsShowsTheActorName keeps the settings page showing a
// person rather than a UUID, and falls back to the id when the user is gone.
func TestPlatformSettingsShowsTheActorName(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	svc.SetSessionUserResolver(fakeSessionUser{user: "user-1"})
	svc.SetUserLabelResolver(fakeUserLabels{"user-1": "Alice Admin"})

	resp, err := svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname: "api.example.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "Alice Admin", resp.GetSettings().GetUpdatedBy())

	got, err := svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	require.NoError(t, err)
	assert.Equal(t, "Alice Admin", got.GetSettings().GetUpdatedBy())

	// Without a resolver the raw id is still reported, never an empty
	// string that looks like "nobody".
	bare := NewForFVT(db, nil)
	got, err = bare.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	require.NoError(t, err)
	assert.Equal(t, "user-1", got.GetSettings().GetUpdatedBy())
}

// fakeDataPlaneRouter records what the settings update asked the cluster to
// publish, and can fail on demand.
type fakeDataPlaneRouter struct {
	err       error
	applied   []string
	hostnames []string
}

func (f *fakeDataPlaneRouter) Ensure(_ context.Context, hostname string, _ config.RuntimeDataPlaneConfig) error {
	if f.err != nil {
		return f.err
	}
	f.applied = append(f.applied, hostname)
	return nil
}

func (f *fakeDataPlaneRouter) ServedHostnames(context.Context, string) ([]string, error) {
	return f.hostnames, nil
}

// TestUpdatePlatformSettingsPublishesTheHostname is the whole point of the
// setting: saving a hostname must make the data-plane route serve it,
// before anything is persisted.
func TestUpdatePlatformSettingsPublishesTheHostname(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	enableDataPlane(t)
	router := &fakeDataPlaneRouter{hostnames: []string{"api.example.com"}}
	svc.SetDataPlaneRouter(router)

	resp, err := svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname: "api.example.com",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"api.example.com"}, router.applied)
	assert.Equal(t, []string{"api.example.com"}, resp.GetSettings().GetServedHostnames())

	// Clearing the hostname must not take the data plane offline.
	router.applied = nil
	_, err = svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{})
	require.NoError(t, err)
	assert.Empty(t, router.applied, "an empty hostname is a no-op")
}

// TestUpdatePlatformSettingsFailsWhenTheRouteCannotBePublished keeps the
// console from advertising an endpoint that answers 404: the update is
// rejected and the previous value survives.
func TestUpdatePlatformSettingsFailsWhenTheRouteCannotBePublished(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	enableDataPlane(t)
	router := &fakeDataPlaneRouter{}
	svc.SetDataPlaneRouter(router)

	_, err := svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname: "api.example.com",
	})
	require.NoError(t, err)

	router.err = assert.AnError
	_, err = svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname: "llm.example.com",
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeRuntimeRouteNotPublished, apierrors.CodeOf(err))
	assert.Contains(t, err.Error(), "llm.example.com")

	got, err := svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	require.NoError(t, err)
	assert.Equal(t, "api.example.com", got.GetSettings().GetInferenceGatewayHostname(),
		"a hostname that could not be published must not be stored")
}

// enableDataPlane turns publishing on for a test and restores the previous
// configuration afterwards.
func enableDataPlane(t *testing.T) {
	t.Helper()
	previous := config.GetConfig()
	cfg := &config.Configuration{}
	cfg.Runtime.DataPlane.Enabled = true
	cfg.Runtime.DataPlane.RouteNamespace = "taas"
	cfg.Runtime.DataPlane.ParentRefName = "gw"
	cfg.Runtime.DataPlane.ParentRefNamespace = "gw-ns"
	cfg.Runtime.DataPlane.BackendName = "inference-gateway"
	cfg.Runtime.DataPlane.BackendNamespace = "gw-ns"
	cfg.Runtime.DataPlane.BackendPort = 80
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(previous) })
}

// TestEnsureDataPlaneUsesTheConfiguredInitialHostname covers a fresh
// install: nothing is saved yet, so the chart's value is what gets
// published — and what the console reports.
func TestEnsureDataPlaneUsesTheConfiguredInitialHostname(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	enableDataPlane(t)
	cfg := config.GetConfig()
	cfg.Runtime.DataPlane.Hostname = "api.example.com"
	config.SetConfigForTest(cfg)

	router := &fakeDataPlaneRouter{hostnames: []string{"api.example.com"}}
	svc.SetDataPlaneRouter(router)

	require.NoError(t, svc.EnsureDataPlane(context.Background()))
	assert.Equal(t, []string{"api.example.com"}, router.applied)

	got, err := svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	require.NoError(t, err)
	assert.Equal(t, "api.example.com", got.GetSettings().GetInferenceGatewayHostname())
	assert.Equal(t, "https://api.example.com/v1", got.GetSettings().GetInferenceBaseUrl())

	// A saved value wins over the configured one.
	router.applied = nil
	_, err = svc.UpdatePlatformSettings(context.Background(), &runtimev1.UpdatePlatformSettingsRequest{
		InferenceGatewayHostname: "llm.example.com",
	})
	require.NoError(t, err)
	require.NoError(t, svc.EnsureDataPlane(context.Background()))
	assert.NotEmpty(t, router.applied)
	assert.Equal(t, "llm.example.com", router.applied[len(router.applied)-1],
		"the saved hostname wins over the configured initial one")
}

// TestEnsureDataPlaneIsSkippedWhenDisabled keeps a chart without dataPlane
// from talking to the cluster at all.
func TestEnsureDataPlaneIsSkippedWhenDisabled(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	config.SetConfigForTest(&config.Configuration{})
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	router := &fakeDataPlaneRouter{}
	svc.SetDataPlaneRouter(router)

	require.NoError(t, svc.EnsureDataPlane(context.Background()))
	assert.Empty(t, router.applied)
}

type fakePlatformGuard struct{ platform bool }

func (f fakePlatformGuard) RequirePlatformAdmin(context.Context) error {
	if !f.platform {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

// TestPlatformSettingsPlatformAdminOnly verifies the serving runtime is a
// platform-owned asset: an organization administrator cannot read or
// change the platform settings, and a platform administrator can.
func TestPlatformSettingsPlatformAdminOnly(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, nil)
	// The org administrator passes the org role guard; the platform gate
	// must still refuse them.
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	svc.SetSessionOrgResolver(fakeSessionOrg{org: "org-a"})
	svc.SetSessionUserResolver(fakeSessionUser{user: "user-1"})
	svc.SetPlatformAdminGuard(fakePlatformGuard{platform: false})

	_, err := svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	svc.SetPlatformAdminGuard(fakePlatformGuard{platform: true})
	_, err = svc.GetPlatformSettings(context.Background(), &runtimev1.GetPlatformSettingsRequest{})
	require.NoError(t, err)
}
