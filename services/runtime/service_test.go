package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	runtimev1 "github.com/go-taas/go-taas/proto/taas/runtime/v1"
	"github.com/go-taas/go-taas/services/audit"
)

type fakeAudit struct{ events []*audit.AuditEvent }

func (f *fakeAudit) Record(_ context.Context, ev *audit.AuditEvent) { f.events = append(f.events, ev) }

type fakeSessionUser struct{ user string }

func (f fakeSessionUser) SessionUserID(context.Context) (string, error) { return f.user, nil }

type fakeSessionOrg struct{ org string }

func (f fakeSessionOrg) SessionActiveOrg(context.Context) (string, error) { return f.org, nil }

type fakeRoleGuard struct{ allowed bool }

func (f fakeRoleGuard) RequireRole(context.Context, string, string, string) error {
	if !f.allowed {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func availableCache(scheme string, models ...RegisteredModel) *ProjectionCache {
	cache := NewProjectionCache()
	cache.Replace(&HealthSnapshot{
		Scheme:       scheme,
		Availability: AvailabilityAvailable,
		RuntimeName:  scheme,
		Components: []Component{
			{ComponentID: "aibrix-controller-manager", Name: "aibrix-controller-manager", Kind: KindControlPlane, Status: "ready"},
		},
		Models:     models,
		ReportedAt: time.Now().UTC(),
	})
	return cache
}

func seedScheme(t *testing.T, db *gorm.DB, scheme string) {
	t.Helper()
	require.NoError(t, MigrateSchemaForFVT(db))
	_, err := NewRuntimeRepository(db).UpdateScheme(context.Background(), scheme, "test")
	require.NoError(t, err)
}

func TestGetServingSchemeDefaultsPlain(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewProjectionCache())

	resp, err := svc.GetServingScheme(context.Background(), &runtimev1.GetServingSchemeRequest{})
	require.NoError(t, err)
	assert.Equal(t, SchemePlain, resp.Scheme.GetScheme())
	assert.Equal(t, int64(0), resp.Scheme.GetUpdatedAt())
}

func TestGetRuntimeStatusUnknownWithoutSnapshot(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewProjectionCache())

	resp, err := svc.GetRuntimeStatus(context.Background(), &runtimev1.GetRuntimeStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, SchemePlain, resp.Runtime.GetScheme())
	assert.Equal(t, AvailabilityUnknown, resp.Runtime.GetAvailability())
}

func TestUpdateServingSchemeInvalid(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewProjectionCache())

	_, err := svc.UpdateServingScheme(context.Background(), &runtimev1.UpdateServingSchemeRequest{Scheme: "bogus"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRuntimeSchemeInvalid, apierrors.CodeOf(err))
}

func TestUpdateServingSchemeAibrixUnavailable(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewProjectionCache())

	_, err := svc.UpdateServingScheme(context.Background(), &runtimev1.UpdateServingSchemeRequest{Scheme: SchemeAIBrix})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRuntimeUnavailable, apierrors.CodeOf(err))
}

func TestUpdateServingSchemeIdempotentNoAudit(t *testing.T) {
	db := newTestDB(t)
	rec := &fakeAudit{}
	svc := NewForFVT(db, availableCache(SchemePlain))
	svc.SetAuditRecorder(rec)

	resp, err := svc.UpdateServingScheme(context.Background(), &runtimev1.UpdateServingSchemeRequest{Scheme: SchemePlain})
	require.NoError(t, err)
	assert.Equal(t, SchemePlain, resp.Scheme.GetScheme())
	assert.Empty(t, rec.events)
}

func TestUpdateServingSchemeAuditsChange(t *testing.T) {
	db := newTestDB(t)
	rec := &fakeAudit{}
	svc := NewForFVT(db, availableCache(SchemeAIBrix))
	svc.SetAuditRecorder(rec)
	svc.SetSessionUserResolver(fakeSessionUser{user: "admin-1"})

	resp, err := svc.UpdateServingScheme(context.Background(), &runtimev1.UpdateServingSchemeRequest{Scheme: SchemeAIBrix})
	require.NoError(t, err)
	assert.Equal(t, SchemeAIBrix, resp.Scheme.GetScheme())
	require.Len(t, rec.events, 1)
	assert.Equal(t, "runtime.scheme_changed", rec.events[0].Action)
	assert.Equal(t, "admin-1", rec.events[0].ActorUserID)
}

func TestRegisterRuntimeModelValidation(t *testing.T) {
	cases := []struct {
		name     string
		req      *runtimev1.RegisterRuntimeModelRequest
		wantCode apierrors.Code
	}{
		{"bad name", &runtimev1.RegisterRuntimeModelRequest{ModelName: "Bad Name", Engine: "vllm", Replicas: 1, ArtifactUrl: "/w"}, apierrors.CodeRuntimeModelNameInvalid},
		{"bad engine", &runtimev1.RegisterRuntimeModelRequest{ModelName: "m1", Engine: "tgi", Replicas: 1, ArtifactUrl: "/w"}, apierrors.CodeRuntimeEngineUnsupported},
		{"bad replicas", &runtimev1.RegisterRuntimeModelRequest{ModelName: "m1", Engine: "vllm", Replicas: 99, ArtifactUrl: "/w"}, apierrors.CodeRuntimeReplicasInvalid},
		{"missing artifact", &runtimev1.RegisterRuntimeModelRequest{ModelName: "m1", Engine: "vllm", Replicas: 1}, apierrors.CodeRuntimeModelNameInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			svc := NewForFVT(db, availableCache(SchemeAIBrix))
			_, err := svc.RegisterRuntimeModel(context.Background(), tc.req)
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, apierrors.CodeOf(err))
		})
	}
}

func TestRegisterRuntimeModelRequiresActiveAIBrix(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, availableCache(SchemePlain))
	_, err := svc.RegisterRuntimeModel(context.Background(), &runtimev1.RegisterRuntimeModelRequest{
		ModelName: "m1", Engine: "vllm", Replicas: 1, ArtifactUrl: "/w",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRuntimeUnavailable, apierrors.CodeOf(err))
}

func TestRegisterRuntimeModelDuplicate(t *testing.T) {
	db := newTestDB(t)
	seedScheme(t, db, SchemeAIBrix)
	svc := NewForFVT(db, availableCache(SchemeAIBrix, RegisteredModel{ModelName: "m1", Engine: "vllm"}))
	_, err := svc.RegisterRuntimeModel(context.Background(), &runtimev1.RegisterRuntimeModelRequest{
		ModelName: "m1", Engine: "vllm", Replicas: 1, ArtifactUrl: "/w",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRuntimeModelExists, apierrors.CodeOf(err))
}

func TestRegisterRuntimeModelPublishesChange(t *testing.T) {
	db := newTestDB(t)
	seedScheme(t, db, SchemeAIBrix)
	svc := NewForFVT(db, availableCache(SchemeAIBrix))
	rec := &fakeAudit{}
	svc.SetAuditRecorder(rec)
	client := mq.NewFake()
	svc.SetPublisher(client)

	var got []ModelChangeMessage
	require.NoError(t, client.Subscribe(context.Background(), mq.DefaultSubjects().RuntimeModelChanges, func(msg mq.Message) error {
		var change ModelChangeMessage
		_ = json.Unmarshal(msg.Body, &change)
		got = append(got, change)
		return nil
	}))

	resp, err := svc.RegisterRuntimeModel(context.Background(), &runtimev1.RegisterRuntimeModelRequest{
		ModelName: "qwen2", Engine: "sglang", Replicas: 2, ArtifactUrl: "/data/w",
	})
	require.NoError(t, err)
	assert.Equal(t, "qwen2", resp.Model.GetModelName())
	require.Len(t, got, 1)
	assert.Equal(t, ModelEventUpsert, got[0].EventType)
	assert.Equal(t, "sglang", got[0].Engine)
	require.Len(t, rec.events, 1)
	assert.Equal(t, "runtime.model_registered", rec.events[0].Action)
}

func TestDeregisterRuntimeModel(t *testing.T) {
	db := newTestDB(t)
	seedScheme(t, db, SchemeAIBrix)
	svc := NewForFVT(db, availableCache(SchemeAIBrix, RegisteredModel{ModelName: "m1", Engine: "vllm"}))
	client := mq.NewFake()
	svc.SetPublisher(client)

	_, err := svc.DeregisterRuntimeModel(context.Background(), &runtimev1.DeregisterRuntimeModelRequest{ModelName: "missing"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRuntimeModelNotFound, apierrors.CodeOf(err))

	_, err = svc.DeregisterRuntimeModel(context.Background(), &runtimev1.DeregisterRuntimeModelRequest{ModelName: "m1"})
	require.NoError(t, err)
}

func TestDeregisterRuntimeModelRequiresActiveAIBrix(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, availableCache(SchemePlain, RegisteredModel{ModelName: "m1"}))
	_, err := svc.DeregisterRuntimeModel(context.Background(), &runtimev1.DeregisterRuntimeModelRequest{ModelName: "m1"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRuntimeUnavailable, apierrors.CodeOf(err))
}

func TestListRuntimeModelsFiltersAndPaginates(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, availableCache(SchemeAIBrix,
		RegisteredModel{ModelName: "alpha", Engine: "vllm", Status: "ready"},
		RegisteredModel{ModelName: "beta", Engine: "sglang", Status: "progressing"},
		RegisteredModel{ModelName: "gamma", Engine: "vllm", Status: "ready"},
	))

	resp, err := svc.ListRuntimeModels(context.Background(), &runtimev1.ListRuntimeModelsRequest{
		Page: &commonv1.PageRequest{Limit: 1}, Engine: "vllm",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), resp.PageMeta.GetTotal())
	require.Len(t, resp.Models, 1)
	assert.Equal(t, "alpha", resp.Models[0].GetModelName())

	resp, err = svc.ListRuntimeModels(context.Background(), &runtimev1.ListRuntimeModelsRequest{Name: "amm"})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	assert.Equal(t, "gamma", resp.Models[0].GetModelName())
}

func TestRequireAdminRoleDenied(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db, NewProjectionCache())
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	svc.SetSessionOrgResolver(fakeSessionOrg{org: "org-1"})
	svc.SetSessionUserResolver(fakeSessionUser{user: "user-1"})

	_, err := svc.GetRuntimeStatus(context.Background(), &runtimev1.GetRuntimeStatusRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestHealthConsumerAppliesSnapshot(t *testing.T) {
	cache := NewProjectionCache()
	client := mq.NewFake()
	consumer := NewHealthConsumer(client, cache)

	snap := HealthSnapshot{Scheme: SchemeAIBrix, Availability: AvailabilityAvailable, Components: []Component{{ComponentID: "c1"}}}
	body, _ := json.Marshal(snap)
	require.NoError(t, consumer.handle(mq.Message{Subject: mq.DefaultSubjects().RuntimeHealth, Body: body}))

	got := cache.Snapshot()
	require.NotNil(t, got)
	assert.Equal(t, AvailabilityAvailable, got.Availability)
	assert.Len(t, got.Components, 1)

	// A malformed snapshot is skipped without panicking.
	require.NoError(t, consumer.handle(mq.Message{Body: []byte("not-json")}))
	assert.Equal(t, AvailabilityAvailable, cache.Availability())
}

func TestSchemeBroadcasterPublishesPersistedScheme(t *testing.T) {
	db := newTestDB(t)
	repo := NewRuntimeRepository(db)
	_, err := repo.UpdateScheme(context.Background(), SchemeAIBrix, "admin")
	require.NoError(t, err)

	client := mq.NewFake()
	b := NewSchemeBroadcaster(repo, client, time.Minute)

	var got []SchemeMessage
	require.NoError(t, client.Subscribe(context.Background(), mq.DefaultSubjects().RuntimeScheme, func(msg mq.Message) error {
		var m SchemeMessage
		_ = json.Unmarshal(msg.Body, &m)
		got = append(got, m)
		return nil
	}))

	b.publish(context.Background())
	require.Len(t, got, 1)
	assert.Equal(t, SchemeAIBrix, got[0].Scheme)
	assert.Equal(t, int64(1), got[0].Revision)
}

func TestProjectionCacheComponentsFilter(t *testing.T) {
	cache := NewProjectionCache()
	cache.Replace(&HealthSnapshot{Components: []Component{
		{ComponentID: "a", Name: "a", Kind: KindControlPlane, Status: "ready"},
		{ComponentID: "b", Name: "b", Kind: KindGateway, Status: "degraded"},
	}})
	assert.Len(t, cache.Components("", ""), 2)
	require.Len(t, cache.Components(KindGateway, ""), 1)
	assert.Equal(t, "b", cache.Components(KindGateway, "")[0].Name)
	assert.Empty(t, cache.Components(KindControlPlane, "degraded"))
}
