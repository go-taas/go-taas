package vgpu

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	vgpuv1 "github.com/go-taas/go-taas/proto/taas/vgpu/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/infer"
)

// fakeSessionOrgResolver returns a fixed org for the session.
type fakeSessionOrgResolver struct{ org string }

func (f fakeSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) { return f.org, nil }

// fakeSessionResolver returns a fixed user id for the session.
type fakeSessionResolver struct{ user string }

func (f fakeSessionResolver) SessionUserID(context.Context) (string, error) { return f.user, nil }

// fakeRoleGuard enforces the admin role.
type fakeRoleGuard struct{ allowed bool }

func (f fakeRoleGuard) RequireRole(context.Context, string, string, string) error {
	if f.allowed {
		return nil
	}
	return apierrors.New(apierrors.CodeForbidden)
}

// fakeGrantChecker grants or denies a pool.
type fakeGrantChecker struct{ granted bool }

func (f fakeGrantChecker) IsPoolGranted(context.Context, string, string) (bool, error) {
	return f.granted, nil
}

// fakeAuditRecorder records audit events.
type fakeAuditRecorder struct{ events []*audit.AuditEvent }

func (f *fakeAuditRecorder) Record(_ context.Context, ev *audit.AuditEvent) {
	f.events = append(f.events, ev)
}

// newTestDB opens a fresh in-memory sqlite database with the vgpu schema.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

// newTestEnv wires a vgpu service with the seams.
func newTestEnv(t *testing.T, roleAllowed bool) *Service {
	t.Helper()
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	svc.SetSessionResolver(fakeSessionResolver{user: "u-admin"})
	svc.SetRoleGuard(fakeRoleGuard{allowed: roleAllowed})
	svc.SetAuditRecorder(&fakeAuditRecorder{})
	return svc
}

// createPool helper creates a pool and returns its id.
func createPool(t *testing.T, svc *Service, name string) string {
	t.Helper()
	resp, err := svc.CreateVGPUPool(context.Background(), &vgpuv1.CreateVGPUPoolRequest{
		Name:            name,
		LabelKey:        "go-taas.io/vgpu-pool",
		LabelValue:      "pool-a",
		PlacementPolicy: PolicyBinpack,
		Templates: []*vgpuv1.SliceTemplate{
			{Name: "small", MemoryGib: 8, ComputePct: 25},
			{Name: "medium", MemoryGib: 16, ComputePct: 50},
		},
	})
	require.NoError(t, err)
	return resp.GetPool().GetPoolId()
}

func TestCreateVGPUPoolValidation(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()

	// Invalid name.
	_, err := svc.CreateVGPUPool(ctx, &vgpuv1.CreateVGPUPoolRequest{
		Name: "Bad Name!", LabelKey: "k", LabelValue: "v",
	})
	assert.EqualValues(t, apierrors.CodeVGPUPoolInvalid, apierrors.CodeOf(err))

	// Missing label.
	_, err = svc.CreateVGPUPool(ctx, &vgpuv1.CreateVGPUPoolRequest{
		Name: "pool-a", LabelKey: "", LabelValue: "",
	})
	assert.EqualValues(t, apierrors.CodeVGPUPoolInvalid, apierrors.CodeOf(err))

	// Invalid policy.
	_, err = svc.CreateVGPUPool(ctx, &vgpuv1.CreateVGPUPoolRequest{
		Name: "pool-a", LabelKey: "k", LabelValue: "v", PlacementPolicy: "bogus",
	})
	assert.EqualValues(t, apierrors.CodeVGPUPoolInvalid, apierrors.CodeOf(err))

	// Invalid template: no templates.
	_, err = svc.CreateVGPUPool(ctx, &vgpuv1.CreateVGPUPoolRequest{
		Name: "pool-a", LabelKey: "k", LabelValue: "v", Templates: []*vgpuv1.SliceTemplate{},
	})
	assert.EqualValues(t, apierrors.CodeVGPUSliceTemplateInvalid, apierrors.CodeOf(err))

	// Invalid template: duplicate name.
	_, err = svc.CreateVGPUPool(ctx, &vgpuv1.CreateVGPUPoolRequest{
		Name: "pool-a", LabelKey: "k", LabelValue: "v",
		Templates: []*vgpuv1.SliceTemplate{
			{Name: "small", MemoryGib: 8, ComputePct: 25},
			{Name: "small", MemoryGib: 16, ComputePct: 50},
		},
	})
	assert.EqualValues(t, apierrors.CodeVGPUSliceTemplateInvalid, apierrors.CodeOf(err))

	// Invalid template: memory < 1.
	_, err = svc.CreateVGPUPool(ctx, &vgpuv1.CreateVGPUPoolRequest{
		Name: "pool-a", LabelKey: "k", LabelValue: "v",
		Templates: []*vgpuv1.SliceTemplate{{Name: "small", MemoryGib: 0, ComputePct: 25}},
	})
	assert.EqualValues(t, apierrors.CodeVGPUSliceTemplateInvalid, apierrors.CodeOf(err))

	// Invalid template: compute out of range.
	_, err = svc.CreateVGPUPool(ctx, &vgpuv1.CreateVGPUPoolRequest{
		Name: "pool-a", LabelKey: "k", LabelValue: "v",
		Templates: []*vgpuv1.SliceTemplate{{Name: "small", MemoryGib: 8, ComputePct: 101}},
	})
	assert.EqualValues(t, apierrors.CodeVGPUSliceTemplateInvalid, apierrors.CodeOf(err))
}

func TestCreateVGPUPoolSuccessAndAudit(t *testing.T) {
	svc := newTestEnv(t, true)
	rec := svc.auditRecorder.(*fakeAuditRecorder)

	poolID := createPool(t, svc, "pool-a")
	assert.NotEmpty(t, poolID)
	assert.Len(t, rec.events, 1)
	assert.Equal(t, "pool.created", rec.events[0].Action)
}

func TestCreateVGPUPoolRoleDenied(t *testing.T) {
	svc := newTestEnv(t, false)
	_, err := svc.CreateVGPUPool(context.Background(), &vgpuv1.CreateVGPUPoolRequest{
		Name: "pool-a", LabelKey: "k", LabelValue: "v",
		Templates: []*vgpuv1.SliceTemplate{{Name: "small", MemoryGib: 8, ComputePct: 25}},
	})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestListVGPUPools(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	createPool(t, svc, "pool-a")
	createPool(t, svc, "pool-b")

	resp, err := svc.ListVGPUPools(ctx, &vgpuv1.ListVGPUPoolsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 20},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetPools(), 2)
	assert.Equal(t, int64(2), resp.GetPageMeta().GetTotal())

	// Name search.
	resp, err = svc.ListVGPUPools(ctx, &vgpuv1.ListVGPUPoolsRequest{Name: "pool-a"})
	require.NoError(t, err)
	assert.Len(t, resp.GetPools(), 1)
	assert.Equal(t, "pool-a", resp.GetPools()[0].GetName())

	// Invalid policy filter.
	_, err = svc.ListVGPUPools(ctx, &vgpuv1.ListVGPUPoolsRequest{PlacementPolicy: "bogus"})
	assert.EqualValues(t, apierrors.CodeVGPUPoolInvalid, apierrors.CodeOf(err))
}

func TestGetVGPUPoolNotFound(t *testing.T) {
	svc := newTestEnv(t, true)
	_, err := svc.GetVGPUPool(context.Background(), &vgpuv1.GetVGPUPoolRequest{PoolId: "missing"})
	assert.EqualValues(t, apierrors.CodeVGPUPoolNotFound, apierrors.CodeOf(err))
}

func TestUpdateVGPUPool(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	poolID := createPool(t, svc, "pool-a")

	// No change returns the current pool without a write.
	resp, err := svc.UpdateVGPUPool(ctx, &vgpuv1.UpdateVGPUPoolRequest{PoolId: poolID})
	require.NoError(t, err)
	assert.Equal(t, PolicyBinpack, resp.GetPool().GetPlacementPolicy())

	// Change the policy.
	resp, err = svc.UpdateVGPUPool(ctx, &vgpuv1.UpdateVGPUPoolRequest{
		PoolId: poolID, PlacementPolicy: &policySpread,
	})
	require.NoError(t, err)
	assert.Equal(t, PolicySpread, resp.GetPool().GetPlacementPolicy())

	// Invalid policy.
	_, err = svc.UpdateVGPUPool(ctx, &vgpuv1.UpdateVGPUPoolRequest{
		PoolId: poolID, PlacementPolicy: &policyBogus,
	})
	assert.EqualValues(t, apierrors.CodeVGPUPoolInvalid, apierrors.CodeOf(err))
}

var (
	policySpread = PolicySpread
	policyBogus  = "bogus"
)

func TestDeleteVGPUPoolBlocked(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	poolID := createPool(t, svc, "pool-a")

	// No services use it: delete succeeds.
	_, err := svc.DeleteVGPUPool(ctx, &vgpuv1.DeleteVGPUPoolRequest{PoolId: poolID})
	require.NoError(t, err)

	// Recreate and seed a service using the pool.
	poolID = createPool(t, svc, "pool-b")
	db := svc.repo.db.DB(ctx)
	require.NoError(t, db.Exec(`INSERT INTO inference_services (id, organization_id, name, model_id, model_version, image_id, replicas, accelerator, accelerator_type, state, pool_id, slice_template_id, slice_gpu_index) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"11111111-1111-1111-1111-111111111111", "org-a", "svc-1", "m1", "v1", "img1", 1, "nvidia", "A800", "running", poolID, "small", -1).Error)

	_, err = svc.DeleteVGPUPool(ctx, &vgpuv1.DeleteVGPUPoolRequest{PoolId: poolID})
	assert.EqualValues(t, apierrors.CodeVGPUPoolInUse, apierrors.CodeOf(err))
}

func TestListTenantVGPUPoolsGrantFiltered(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	createPool(t, svc, "pool-a")
	createPool(t, svc, "pool-b")

	// Nil grant checker: everything granted.
	resp, err := svc.ListTenantVGPUPools(ctx, &vgpuv1.ListTenantVGPUPoolsRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.GetPools(), 2)

	// Grant checker denies everything.
	svc.SetGrantChecker(fakeGrantChecker{granted: false})
	resp, err = svc.ListTenantVGPUPools(ctx, &vgpuv1.ListTenantVGPUPoolsRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.GetPools(), 0)
}

func TestValidateSlice(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	poolID := createPool(t, svc, "pool-a")

	// Valid slice.
	info, err := svc.ValidateSlice(ctx, "org-a", poolID, "small")
	require.NoError(t, err)
	assert.Equal(t, int64(8), info.MemoryGiB)
	assert.Equal(t, int64(25), info.ComputePct)

	// Unknown pool.
	_, err = svc.ValidateSlice(ctx, "org-a", "missing", "small")
	assert.EqualValues(t, apierrors.CodeVGPUSliceUnavailable, apierrors.CodeOf(err))

	// Unknown template.
	_, err = svc.ValidateSlice(ctx, "org-a", poolID, "bogus")
	assert.EqualValues(t, apierrors.CodeVGPUSliceUnavailable, apierrors.CodeOf(err))

	// Ungranted pool.
	svc.SetGrantChecker(fakeGrantChecker{granted: false})
	_, err = svc.ValidateSlice(ctx, "org-a", poolID, "small")
	assert.EqualValues(t, apierrors.CodeVGPUSliceUnavailable, apierrors.CodeOf(err))
}

func TestMembershipProjection(t *testing.T) {
	proj := NewMembershipProjection()
	proj.Replace(map[string]*acceleratorNode{
		"n1": {
			NodeID: "n1", Name: "node-a",
			Labels: map[string]string{"go-taas.io/vgpu-pool": "pool-a"},
			GPUs: []acceleratorGPU{{
				Index: 0, Model: "A800",
				Slices: []sliceAllocation{{TemplateName: "small", ServiceID: "s1", MemoryGiB: 8, ComputePct: 25}},
			}},
		},
		"n2": {NodeID: "n2", Name: "node-b", Labels: map[string]string{}},
	})

	assert.Equal(t, int64(1), proj.NodeCount("pool-a", "go-taas.io/vgpu-pool", "pool-a"))
	assert.Len(t, proj.Nodes("pool-a", "go-taas.io/vgpu-pool", "pool-a"), 1)
	agg := proj.TemplateAllocation("pool-a", "go-taas.io/vgpu-pool", "pool-a")
	assert.Equal(t, int64(1), agg["small"].Allocated)
}

func TestGetVGPUPoolWithProjection(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	poolID := createPool(t, svc, "pool-a")

	// Seed the membership projection with a member node.
	svc.SetMembership(NewMembershipProjection())
	svc.membership.Replace(map[string]*acceleratorNode{
		"n1": {
			NodeID: "n1", Name: "node-a", Health: "healthy",
			Labels: map[string]string{"go-taas.io/vgpu-pool": "pool-a"},
			GPUs: []acceleratorGPU{{
				Index: 0, Model: "A800", Allocated: true, Free: false,
				Slices: []sliceAllocation{{TemplateName: "small", ServiceID: "s1", MemoryGiB: 8, ComputePct: 25}},
			}},
		},
	})

	resp, err := svc.GetVGPUPool(ctx, &vgpuv1.GetVGPUPoolRequest{PoolId: poolID})
	require.NoError(t, err)
	assert.Equal(t, poolID, resp.GetPool().GetPoolId())
	assert.Equal(t, int64(1), resp.GetPool().GetNodeCount())
	assert.Len(t, resp.GetNodes(), 1)
	assert.Equal(t, "node-a", resp.GetNodes()[0].GetName())
	assert.Len(t, resp.GetNodes()[0].GetGpus(), 1)
	assert.Equal(t, int64(0), resp.GetNodes()[0].GetGpus()[0].GetIndex())
	assert.Len(t, resp.GetNodes()[0].GetGpus()[0].GetSlices(), 1)
	assert.Equal(t, "small", resp.GetNodes()[0].GetGpus()[0].GetSlices()[0].GetTemplateName())
}

func TestUpdateVGPUPoolLabelAndTemplates(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	poolID := createPool(t, svc, "pool-a")

	// Change the label selector.
	labelKey := "go-taas.io/vgpu-pool"
	labelValue := "pool-b"
	resp, err := svc.UpdateVGPUPool(ctx, &vgpuv1.UpdateVGPUPoolRequest{
		PoolId: poolID, LabelKey: &labelKey, LabelValue: &labelValue,
	})
	require.NoError(t, err)
	assert.Equal(t, "pool-b", resp.GetPool().GetLabelValue())

	// Replace the templates.
	resp, err = svc.UpdateVGPUPool(ctx, &vgpuv1.UpdateVGPUPoolRequest{
		PoolId: poolID,
		Templates: []*vgpuv1.SliceTemplate{
			{Name: "large", MemoryGib: 32, ComputePct: 100},
		},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetPool().GetTemplates(), 1)
	assert.Equal(t, "large", resp.GetPool().GetTemplates()[0].GetName())

	// Invalid label.
	_, err = svc.UpdateVGPUPool(ctx, &vgpuv1.UpdateVGPUPoolRequest{
		PoolId: poolID, LabelKey: &emptyStr, LabelValue: &emptyStr,
	})
	assert.EqualValues(t, apierrors.CodeVGPUPoolInvalid, apierrors.CodeOf(err))

	// Invalid templates.
	_, err = svc.UpdateVGPUPool(ctx, &vgpuv1.UpdateVGPUPoolRequest{
		PoolId: poolID,
		Templates: []*vgpuv1.SliceTemplate{{Name: "small", MemoryGib: 0, ComputePct: 25}},
	})
	assert.EqualValues(t, apierrors.CodeVGPUSliceTemplateInvalid, apierrors.CodeOf(err))
}

var emptyStr = ""

func TestListTenantVGPUPoolsNoOrg(t *testing.T) {
	svc := newTestEnv(t, true)
	// No session org resolver and no header -> unauthorized.
	svc.SetSessionOrgResolver(nil)
	_, err := svc.ListTenantVGPUPools(context.Background(), &vgpuv1.ListTenantVGPUPoolsRequest{})
	assert.EqualValues(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}

func TestServicePlumbing(t *testing.T) {
	svc := New(nil)
	assert.Equal(t, ServiceName, svc.ServiceName())
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())

	// Migrate on a fresh DB creates the vgpu_pools table.
	db := newTestDB(t)
	svc2 := NewForFVT(db)
	require.NoError(t, svc2.Migrate(context.Background()))

	// AttachToServer registers on a gRPC server.
	grpcServer := grpc.NewServer()
	svc2.AttachToServer(grpcServer)

	// A service without a database component fails closed on a read.
	svc3 := New(nil)
	_, err := svc3.ListVGPUPools(context.Background(), &vgpuv1.ListVGPUPoolsRequest{})
	assert.Error(t, err)
}

func TestTemplatesFromJSON(t *testing.T) {
	raw, err := json.Marshal([]SliceTemplate{{Name: "small", MemoryGiB: 8, ComputePct: 25}})
	require.NoError(t, err)
	templates, err := templatesFromJSON(raw)
	require.NoError(t, err)
	assert.Len(t, templates, 1)
	assert.Equal(t, "small", templates[0].Name)

	empty, err := templatesFromJSON(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}