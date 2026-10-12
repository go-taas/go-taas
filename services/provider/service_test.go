package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	providerv1 "github.com/go-taas/go-taas/proto/taas/provider/v1"
	"github.com/go-taas/go-taas/services/model"
)

// fakeCatalog is an in-memory model-module double implementing the
// ModelCatalog seam.
type fakeCatalog struct {
	byID   map[string]*model.ExternalModel
	byName map[string]*model.ExternalModel
	seq    int
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{byID: map[string]*model.ExternalModel{}, byName: map[string]*model.ExternalModel{}}
}

func (c *fakeCatalog) CreateExternalModel(_ context.Context, name, providerID, upstreamModelID string) (*model.ExternalModel, error) {
	if existing, ok := c.byName[name]; ok {
		if existing.Published {
			return nil, apierrors.New(apierrors.CodeProviderPublishConflict)
		}
		existing.Published = true
		existing.External = true
		existing.ProviderID = providerID
		existing.UpstreamModelID = upstreamModelID
		return existing, nil
	}
	c.seq++
	cm := &model.ExternalModel{
		ID:              fmt.Sprintf("catalog-%d", c.seq),
		Name:            name,
		External:        true,
		Published:       true,
		ProviderID:      providerID,
		UpstreamModelID: upstreamModelID,
	}
	c.byID[cm.ID] = cm
	c.byName[name] = cm
	return cm, nil
}

func (c *fakeCatalog) SetExternalModelPublished(_ context.Context, modelID string, published bool) error {
	cm, ok := c.byID[modelID]
	if !ok {
		return apierrors.New(apierrors.CodeProviderNotPublished)
	}
	cm.Published = published
	return nil
}

func (c *fakeCatalog) LookupExternalModel(_ context.Context, modelID, modelName string) (*model.ExternalModel, error) {
	if modelID != "" {
		if cm, ok := c.byID[modelID]; ok {
			return cm, nil
		}
	}
	if modelName != "" {
		if cm, ok := c.byName[modelName]; ok {
			return cm, nil
		}
	}
	return nil, apierrors.New(apierrors.CodeProviderNotPublished)
}

// newProviderSvc builds a provider service over a fresh in-memory database
// wired with a fake catalog.
func newProviderSvc(t *testing.T) (*Service, *fakeCatalog) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	require.NoError(t, MigrateSchemaForFVT(db))
	svc := NewForFVT(db)
	cat := newFakeCatalog()
	svc.SetModelCatalog(cat)
	return svc, cat
}

// createTestProvider creates a provider and returns its id.
func createTestProvider(t *testing.T, svc *Service, name string) string {
	t.Helper()
	resp, err := svc.CreateProvider(context.Background(), &providerv1.CreateProviderRequest{
		Name:    name,
		Type:    TypeOpenAI,
		BaseUrl: "https://api.example.com",
		ApiKey:  "sk-abcdef123456",
	})
	require.NoError(t, err)
	return resp.GetProvider().GetProviderId()
}

// TestCreateProviderValidation covers AC2/AC8 validation.
func TestCreateProviderValidation(t *testing.T) {
	svc, _ := newProviderSvc(t)
	ctx := context.Background()

	cases := []struct {
		name string
		req  *providerv1.CreateProviderRequest
		code apierrors.Code
	}{
		{"bad name", &providerv1.CreateProviderRequest{Name: "Bad_Name", Type: TypeOpenAI, BaseUrl: "https://api.example.com", ApiKey: "sk-abcdef123456"}, apierrors.CodeProviderNameInvalid},
		{"empty name", &providerv1.CreateProviderRequest{Name: "", Type: TypeOpenAI, BaseUrl: "https://api.example.com", ApiKey: "sk-abcdef123456"}, apierrors.CodeProviderNameInvalid},
		{"bad type", &providerv1.CreateProviderRequest{Name: "acme", Type: "anthropic", BaseUrl: "https://api.example.com", ApiKey: "sk-abcdef123456"}, apierrors.CodeProviderTypeInvalid},
		{"bad url", &providerv1.CreateProviderRequest{Name: "acme", Type: TypeOpenAI, BaseUrl: "not-a-url", ApiKey: "sk-abcdef123456"}, apierrors.CodeProviderBaseURLInvalid},
		{"relative url", &providerv1.CreateProviderRequest{Name: "acme", Type: TypeOpenAI, BaseUrl: "/v1", ApiKey: "sk-abcdef123456"}, apierrors.CodeProviderBaseURLInvalid},
		{"missing key", &providerv1.CreateProviderRequest{Name: "acme", Type: TypeOpenAI, BaseUrl: "https://api.example.com"}, apierrors.CodeProviderAPIKeyRequired},
		{"bad timeout", &providerv1.CreateProviderRequest{Name: "acme", Type: TypeOpenAI, BaseUrl: "https://api.example.com", ApiKey: "sk-abcdef123456", RequestTimeoutSeconds: 500}, apierrors.CodeProviderModelInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateProvider(ctx, tc.req)
			require.Error(t, err)
			assert.Equal(t, tc.code, apierrors.CodeOf(err))
		})
	}
}

// TestCreateProviderDuplicateName covers AC2: a duplicate name conflicts.
func TestCreateProviderDuplicateName(t *testing.T) {
	svc, _ := newProviderSvc(t)
	ctx := context.Background()
	createTestProvider(t, svc, "dup")
	_, err := svc.CreateProvider(ctx, &providerv1.CreateProviderRequest{Name: "dup", Type: TypeOpenAI, BaseUrl: "https://api.example.com", ApiKey: "sk-abcdef123456"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderNameConflict, apierrors.CodeOf(err))
}

// TestWriteOnlyKey covers AC3: the key is never returned by a read, only the
// masked hint.
func TestWriteOnlyKey(t *testing.T) {
	svc, _ := newProviderSvc(t)
	ctx := context.Background()
	id := createTestProvider(t, svc, "secretprov")

	resp, err := svc.GetProvider(ctx, &providerv1.GetProviderRequest{ProviderId: id})
	require.NoError(t, err)
	assert.Equal(t, "••••3456", resp.GetProvider().GetApiKeyHint())

	body, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "sk-abcdef123456")
	assert.NotContains(t, string(body), "Ciphertext")

	// Update with an empty key keeps the credential and hint.
	_, err = svc.UpdateProvider(ctx, &providerv1.UpdateProviderRequest{ProviderId: id, Name: "secretprov", Type: TypeOpenAI, BaseUrl: "https://api.example.com"})
	require.NoError(t, err)
	after, err := svc.GetProvider(ctx, &providerv1.GetProviderRequest{ProviderId: id})
	require.NoError(t, err)
	assert.Equal(t, "••••3456", after.GetProvider().GetApiKeyHint())

	// Update with a non-empty key rotates the credential and hint.
	_, err = svc.UpdateProvider(ctx, &providerv1.UpdateProviderRequest{ProviderId: id, Name: "secretprov", Type: TypeOpenAI, BaseUrl: "https://api.example.com", ApiKey: "sk-rotated9999"})
	require.NoError(t, err)
	rotated, err := svc.GetProvider(ctx, &providerv1.GetProviderRequest{ProviderId: id})
	require.NoError(t, err)
	assert.Equal(t, "••••9999", rotated.GetProvider().GetApiKeyHint())
}

// TestEnableBlockedWhenUnauthorized covers AC8: a provider whose last probe
// is unauthorized cannot be enabled.
func TestEnableBlockedWhenUnauthorized(t *testing.T) {
	svc, _ := newProviderSvc(t)
	ctx := context.Background()
	id := createTestProvider(t, svc, "guardprov")

	repo, err := svc.providers()
	require.NoError(t, err)
	require.NoError(t, repo.UpdateProbe(ctx, id, ProbeResult{Status: ProbeUnauthorized, HTTPStatus: 401, Message: "HTTP 401 — credentials rejected"}))

	_, err = svc.UpdateProvider(ctx, &providerv1.UpdateProviderRequest{ProviderId: id, Name: "guardprov", Type: TypeOpenAI, BaseUrl: "https://api.example.com", Enabled: true})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderUpstreamUnauthorized, apierrors.CodeOf(err))

	// Enabling with an unreachable last probe is allowed.
	require.NoError(t, repo.UpdateProbe(ctx, id, ProbeResult{Status: ProbeUnreachable}))
	_, err = svc.UpdateProvider(ctx, &providerv1.UpdateProviderRequest{ProviderId: id, Name: "guardprov", Type: TypeOpenAI, BaseUrl: "https://api.example.com", Enabled: true})
	require.NoError(t, err)
}

// TestUpstreamModelCuration covers AC5.
func TestUpstreamModelCuration(t *testing.T) {
	svc, _ := newProviderSvc(t)
	ctx := context.Background()
	id := createTestProvider(t, svc, "curate")
	enabled := true

	add, err := svc.AddProviderModel(ctx, &providerv1.AddProviderModelRequest{ProviderId: id, UpstreamModelId: "gpt-4o", Enabled: &enabled})
	require.NoError(t, err)
	rowID := add.GetModel().GetModelId()
	assert.True(t, add.GetModel().GetEnabled())

	// Duplicate upstream id -> 13009.
	_, err = svc.AddProviderModel(ctx, &providerv1.AddProviderModelRequest{ProviderId: id, UpstreamModelId: "gpt-4o", Enabled: &enabled})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderModelExists, apierrors.CodeOf(err))

	// Missing upstream id -> 13010.
	_, err = svc.AddProviderModel(ctx, &providerv1.AddProviderModelRequest{ProviderId: id, UpstreamModelId: ""})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderModelInvalid, apierrors.CodeOf(err))

	// Update (omitted enabled keeps the current value).
	_, err = svc.UpdateProviderModel(ctx, &providerv1.UpdateProviderModelRequest{ProviderId: id, ModelId: rowID, DisplayName: "GPT-4o", ContextWindow: 128000})
	require.NoError(t, err)
	updated, err := svc.ListProviderModels(ctx, &providerv1.ListProviderModelsRequest{ProviderId: id})
	require.NoError(t, err)
	require.Len(t, updated.GetModels(), 1)
	assert.Equal(t, "GPT-4o", updated.GetModels()[0].GetDisplayName())
	assert.True(t, updated.GetModels()[0].GetEnabled())

	// Unknown row -> 13008.
	_, err = svc.UpdateProviderModel(ctx, &providerv1.UpdateProviderModelRequest{ProviderId: id, ModelId: "nope"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderModelNotFound, apierrors.CodeOf(err))
}

// TestPublishConflictAndIdempotency covers AC6/AC7.
func TestPublishConflictAndIdempotency(t *testing.T) {
	svc, cat := newProviderSvc(t)
	ctx := context.Background()
	id := createTestProvider(t, svc, "pubprov")
	enabled := true
	add, err := svc.AddProviderModel(ctx, &providerv1.AddProviderModelRequest{ProviderId: id, UpstreamModelId: "gpt-4o", Enabled: &enabled})
	require.NoError(t, err)
	rowID := add.GetModel().GetModelId()

	pub, err := svc.PublishProviderModel(ctx, &providerv1.PublishProviderModelRequest{ProviderId: id, ModelId: rowID, TenantFacingName: "gpt-4o"})
	require.NoError(t, err)
	assert.Equal(t, "catalog-1", pub.GetCatalogModelId())

	// Idempotent re-publish.
	pub2, err := svc.PublishProviderModel(ctx, &providerv1.PublishProviderModelRequest{ProviderId: id, ModelId: rowID, TenantFacingName: "gpt-4o"})
	require.NoError(t, err)
	assert.Equal(t, "catalog-1", pub2.GetCatalogModelId())

	// A live name conflict -> 13014.
	add2, err := svc.AddProviderModel(ctx, &providerv1.AddProviderModelRequest{ProviderId: id, UpstreamModelId: "gpt-4o-mini", Enabled: &enabled})
	require.NoError(t, err)
	_, err = svc.PublishProviderModel(ctx, &providerv1.PublishProviderModelRequest{ProviderId: id, ModelId: add2.GetModel().GetModelId(), TenantFacingName: "gpt-4o"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderPublishConflict, apierrors.CodeOf(err))

	// Unpublish is idempotent and marks the catalog model unpublished.
	_, err = svc.UnpublishProviderModel(ctx, &providerv1.UnpublishProviderModelRequest{ProviderId: id, ModelId: rowID})
	require.NoError(t, err)
	assert.False(t, cat.byID["catalog-1"].Published)
	_, err = svc.UnpublishProviderModel(ctx, &providerv1.UnpublishProviderModelRequest{ProviderId: id, ModelId: rowID})
	require.NoError(t, err)

	// Re-publish re-enables the same catalog model.
	_, err = svc.PublishProviderModel(ctx, &providerv1.PublishProviderModelRequest{ProviderId: id, ModelId: rowID, TenantFacingName: "gpt-4o"})
	require.NoError(t, err)
	assert.True(t, cat.byID["catalog-1"].Published)
}

// TestPublishDisabledRowRejected covers AC6: a disabled row cannot be
// published (13010).
func TestPublishDisabledRowRejected(t *testing.T) {
	svc, _ := newProviderSvc(t)
	ctx := context.Background()
	id := createTestProvider(t, svc, "disprov")
	disabled := false
	add, err := svc.AddProviderModel(ctx, &providerv1.AddProviderModelRequest{ProviderId: id, UpstreamModelId: "m", Enabled: &disabled})
	require.NoError(t, err)
	_, err = svc.PublishProviderModel(ctx, &providerv1.PublishProviderModelRequest{ProviderId: id, ModelId: add.GetModel().GetModelId(), TenantFacingName: "m"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderModelInvalid, apierrors.CodeOf(err))
}

// TestInUseGuards covers AC8: delete/remove are blocked while published.
func TestInUseGuards(t *testing.T) {
	svc, _ := newProviderSvc(t)
	ctx := context.Background()
	enabled := true

	// No published models: delete is allowed.
	id := createTestProvider(t, svc, "inuseprov")
	_, err := svc.AddProviderModel(ctx, &providerv1.AddProviderModelRequest{ProviderId: id, UpstreamModelId: "m", Enabled: &enabled})
	require.NoError(t, err)
	_, err = svc.DeleteProvider(ctx, &providerv1.DeleteProviderRequest{ProviderId: id})
	require.NoError(t, err)

	// With a published model: remove and delete are blocked with 13016.
	id = createTestProvider(t, svc, "inuseprov2")
	add, err := svc.AddProviderModel(ctx, &providerv1.AddProviderModelRequest{ProviderId: id, UpstreamModelId: "m", Enabled: &enabled})
	require.NoError(t, err)
	rowID := add.GetModel().GetModelId()
	_, err = svc.PublishProviderModel(ctx, &providerv1.PublishProviderModelRequest{ProviderId: id, ModelId: rowID, TenantFacingName: "m"})
	require.NoError(t, err)

	_, err = svc.RemoveProviderModel(ctx, &providerv1.RemoveProviderModelRequest{ProviderId: id, ModelId: rowID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderInUse, apierrors.CodeOf(err))

	_, err = svc.DeleteProvider(ctx, &providerv1.DeleteProviderRequest{ProviderId: id})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderInUse, apierrors.CodeOf(err))
}

// TestResolveFailClosed covers AC10: disabled, unpublished and undecryptable
// resolve all fail closed with distinct codes.
func TestResolveFailClosed(t *testing.T) {
	svc, _ := newProviderSvc(t)
	ctx := context.Background()
	enabled := true
	id := createTestProvider(t, svc, "resolveprov")
	add, err := svc.AddProviderModel(ctx, &providerv1.AddProviderModelRequest{ProviderId: id, UpstreamModelId: "gpt-4o", Enabled: &enabled})
	require.NoError(t, err)
	rowID := add.GetModel().GetModelId()
	pub, err := svc.PublishProviderModel(ctx, &providerv1.PublishProviderModelRequest{ProviderId: id, ModelId: rowID, TenantFacingName: "gpt-4o"})
	require.NoError(t, err)
	catalogID := pub.GetCatalogModelId()

	// Provider disabled -> 13011.
	_, err = svc.ResolveExternalModel(ctx, &providerv1.ResolveExternalModelRequest{ModelId: catalogID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderDisabled, apierrors.CodeOf(err))

	// Enable, then corrupt the ciphertext -> 13007.
	repo, err := svc.providers()
	require.NoError(t, err)
	require.NoError(t, repo.UpdateProbe(ctx, id, ProbeResult{Status: ProbeOK}))
	_, err = svc.UpdateProvider(ctx, &providerv1.UpdateProviderRequest{ProviderId: id, Name: "resolveprov", Type: TypeOpenAI, BaseUrl: "https://api.example.com", Enabled: true})
	require.NoError(t, err)
	require.NoError(t, repo.db.DB(ctx).Model(&Provider{}).Where("id = ?", id).Update("api_key_ciphertext", []byte("not-a-ciphertext")).Error)
	_, err = svc.ResolveExternalModel(ctx, &providerv1.ResolveExternalModelRequest{ModelId: catalogID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderSecretUnavailable, apierrors.CodeOf(err))

	// Restore the key and unpublish -> 13015.
	orig, err := svc.encryptSecret("sk-abcdef123456")
	require.NoError(t, err)
	require.NoError(t, repo.db.DB(ctx).Model(&Provider{}).Where("id = ?", id).Update("api_key_ciphertext", orig).Error)
	_, err = svc.UnpublishProviderModel(ctx, &providerv1.UnpublishProviderModelRequest{ProviderId: id, ModelId: rowID})
	require.NoError(t, err)
	_, err = svc.ResolveExternalModel(ctx, &providerv1.ResolveExternalModelRequest{ModelId: catalogID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderNotPublished, apierrors.CodeOf(err))
}

// TestUnknownProvider covers 13001.
func TestUnknownProvider(t *testing.T) {
	svc, _ := newProviderSvc(t)
	_, err := svc.GetProvider(context.Background(), &providerv1.GetProviderRequest{ProviderId: "missing"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderNotFound, apierrors.CodeOf(err))
}
