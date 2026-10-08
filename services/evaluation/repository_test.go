package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPurgeExpiredRunSnapshots(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	repo := NewRepository(db)
	now := time.Now().UTC()
	run := &EvaluationRun{RunID: "11111111-1111-4111-8111-111111111111", EvaluationID: "22222222-2222-4222-8222-222222222222", OrganizationID: "org-a", Status: RunFailed, PromptID: "33333333-3333-4333-8333-333333333333", PromptVersion: 1, PromptContentSnapshot: "private prompt", PromptVariables: "[]", ModelID: "model-a", APIKeyID: "44444444-4444-4444-8444-444444444444", CaseCount: 1, CreatedBy: "user-a", CreatedAt: now.Add(-48 * time.Hour), RetainedUntil: now.Add(-time.Hour), Currency: "USD"}
	require.NoError(t, db.Create(run).Error)
	caseID := "55555555-5555-4555-8555-555555555555"
	result := &EvaluationRunCase{ResultID: "66666666-6666-4666-8666-666666666666", RunID: run.RunID, CaseID: &caseID, CasePosition: 0, CaseName: "old", Variables: `{}`, Checks: `[]`, Status: CaseError, CreatedAt: now.Add(-48 * time.Hour)}
	require.NoError(t, db.Create(result).Error)
	removed, err := repo.purgeExpired(context.Background(), now, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, removed)
	var runs, cases int64
	require.NoError(t, db.Model(&EvaluationRun{}).Count(&runs).Error)
	require.NoError(t, db.Model(&EvaluationRunCase{}).Count(&cases).Error)
	require.Zero(t, runs)
	require.Zero(t, cases)
}
