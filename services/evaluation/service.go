//nolint:revive // RPC methods implement the generated gRPC service contract.
package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	evaluationv1 "github.com/go-taas/go-taas/proto/taas/evaluation/v1"
	promptv1 "github.com/go-taas/go-taas/proto/taas/prompt/v1"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/tenancy"
)

const ServiceName = "evaluation"

var ErrEvaluationNotFound = apierrors.New(apierrors.CodeEvaluationNotFound)
var ErrEvaluationCaseInvalid = apierrors.New(apierrors.CodeEvaluationCaseInvalid)
var ErrEvaluationLimitExceeded = apierrors.New(apierrors.CodeEvaluationLimitExceeded)
var ErrEvaluationRunNotFound = apierrors.New(apierrors.CodeEvaluationRunNotFound)
var ErrEvaluationRunStateInvalid = apierrors.New(apierrors.CodeEvaluationRunStateInvalid)

type PromptResolver interface {
	ListPromptVersions(context.Context, *promptv1.ListPromptVersionsRequest) (*promptv1.ListPromptVersionsResponse, error)
	GetPrompt(context.Context, *promptv1.GetPromptRequest) (*promptv1.GetPromptResponse, error)
}
type SessionOrgResolver interface {
	SessionActiveOrg(context.Context) (string, error)
}
type SessionUserResolver interface {
	SessionUserID(context.Context) (string, error)
}
type RoleGuard interface {
	RequireRole(context.Context, string, string, string) error
}
type AuditRecorder interface {
	Record(context.Context, *audit.AuditEvent)
}
type APIKeyValidator interface {
	ValidateActiveAPIKey(context.Context, string, string) error
}

// CompletionResult is the outcome of one metered model call executed
// by the CompletionProvider (feature #44, AD5).
type CompletionResult struct {
	Completion       string
	PromptTokens     int64
	CompletionTokens int64
	LatencyMS        int64
	ServiceID        string
}

// CompletionInput is one case execution request: the rendered prompt,
// the model identity and the tenant-owned API key identity. It never
// carries key secret material (AD5).
type CompletionInput struct {
	OrganizationID string
	ModelID        string
	APIKeyID       string
	Prompt         string
	MaxTokens      int32
}

// CompletionProvider executes one real metered model call for a case
// (feature #44, AD5). It is implemented by the infer module and
// injected at wiring time; a nil provider fails runs closed with real
// case errors rather than simulated completions.
type CompletionProvider interface {
	CompleteModelCall(ctx context.Context, in *CompletionInput) (*CompletionResult, error)
}

// MeteringPublisher ingests one token-usage event through the standard
// metering path (feature #44, AD5): the same metering.events subject
// the data-plane gateway publishes, preserving usage, cost, request
// logs and organization/key attribution.
type MeteringPublisher interface {
	PublishMeteringEvent(ctx context.Context, event []byte) error
}

type Service struct {
	evaluationv1.UnimplementedEvaluationServiceServer
	components    server.Components
	repo          *Repository
	prompts       PromptResolver
	sessionOrg    SessionOrgResolver
	sessionUser   SessionUserResolver
	roleGuard     RoleGuard
	auditRecorder AuditRecorder
	apiKeys       APIKeyValidator
	completions   CompletionProvider
	metering      MeteringPublisher
	costs         CostAttributor
}

func (s *Service) SetAPIKeyValidator(v APIKeyValidator) { s.apiKeys = v }

// SetCompletionProvider installs the real metered completion seam
// (feature #44, AD5). Without it CreateEvaluationRun fails closed
// (12806) rather than persisting simulated completions.
func (s *Service) SetCompletionProvider(p CompletionProvider) { s.completions = p }

// SetMeteringPublisher installs the standard metering-event ingestion
// seam (feature #44, AD5).
func (s *Service) SetMeteringPublisher(p MeteringPublisher) { s.metering = p }

// SetCostAttributor installs the per-request cost estimator used to
// fill each case's cost minor units (feature #44, D4).
func (s *Service) SetCostAttributor(c CostAttributor) { s.costs = c }

func New(components server.Components) *Service { return &Service{components: components} }
func NewForFVT(db *gorm.DB, prompts PromptResolver) *Service {
	return &Service{repo: NewRepository(db), prompts: prompts}
}
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Evaluation{}, &EvaluationCase{}, &EvaluationRun{}, &EvaluationRunCase{})
}
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver)   { s.sessionOrg = r }
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUser = r }
func (s *Service) SetRoleGuard(g RoleGuard)                     { s.roleGuard = g }
func (s *Service) SetAuditRecorder(r AuditRecorder)             { s.auditRecorder = r }
func (s *Service) SetPromptResolver(r PromptResolver)           { s.prompts = r }
func (s *Service) AttachToServer(gs *grpc.Server) {
	evaluationv1.RegisterEvaluationServiceServer(gs, s)
}
func (s *Service) ServiceName() string { return ServiceName }
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return evaluationv1.RegisterEvaluationServiceHandler
}
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.database()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Evaluation{}, &EvaluationCase{}, &EvaluationRun{}, &EvaluationRunCase{})
}

func (s *Service) database() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db, nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.New(apierrors.CodeInternal)
	}
	db, ok := s.components.DB().GormDB().(*gorm.DB)
	if !ok {
		return nil, apierrors.New(apierrors.CodeInternal)
	}
	return db, nil
}
func (s *Service) repository() (*Repository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	s.repo = NewRepository(db)
	return s.repo, nil
}
func (s *Service) resolveOrg(ctx context.Context) (string, error) {
	if s.sessionOrg != nil {
		org, err := s.sessionOrg.SessionActiveOrg(ctx)
		if err != nil {
			return "", err
		}
		if org != "" {
			return org, nil
		}
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md.Get("x-organization-id")) == 0 || strings.TrimSpace(md.Get("x-organization-id")[0]) == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	return strings.TrimSpace(md.Get("x-organization-id")[0]), nil
}
func (s *Service) requireWriteRole(ctx context.Context, orgID string) error {
	if s.roleGuard == nil || s.sessionUser == nil {
		return nil
	}
	userID, err := s.sessionUser.SessionUserID(ctx)
	if err != nil {
		return err
	}
	return s.roleGuard.RequireRole(ctx, orgID, userID, tenancy.RoleMember)
}
func (s *Service) audit(ctx context.Context, action, orgID, id string) {
	if s.auditRecorder != nil {
		s.auditRecorder.Record(ctx, &audit.AuditEvent{OrganizationID: orgID, ActorType: "user", Action: action, ResourceType: "evaluation", ResourceID: id})
	}
}
func (s *Service) promptVersion(ctx context.Context, orgID, promptID string, version int) (string, []string, string, error) {
	if s.prompts == nil {
		return "", nil, "", apierrors.New(apierrors.CodeInternal)
	}
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-organization-id", orgID))
	resp, err := s.prompts.ListPromptVersions(ctx, &promptv1.ListPromptVersionsRequest{PromptId: promptID})
	if err != nil {
		return "", nil, "", apierrors.New(apierrors.CodeEvaluationPromptVersionInvalid)
	}
	for _, v := range resp.GetVersions() {
		if int(v.GetVersion()) != version {
			continue
		}
		variables := v.GetVariables()
		p, err := s.prompts.GetPrompt(ctx, &promptv1.GetPromptRequest{PromptId: promptID})
		if err != nil {
			return "", nil, "", apierrors.New(apierrors.CodeEvaluationPromptVersionInvalid)
		}
		return v.GetContent(), variables, p.GetPrompt().GetName(), nil
	}
	return "", nil, "", apierrors.New(apierrors.CodeEvaluationPromptVersionInvalid)
}

func (s *Service) CreateEvaluation(ctx context.Context, req *evaluationv1.CreateEvaluationRequest) (*evaluationv1.EvaluationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireWriteRole(ctx, orgID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" || len(name) > 128 || len(req.GetDescription()) > 500 || req.GetPromptVersion() <= 0 {
		return nil, apierrors.New(apierrors.CodeEvaluationPromptVersionInvalid)
	}
	_, _, _, err = s.promptVersion(ctx, orgID, req.GetPromptId(), int(req.GetPromptVersion()))
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	row := &Evaluation{OrganizationID: orgID, Name: name, Description: req.GetDescription(), PromptID: req.GetPromptId(), PromptVersion: int(req.GetPromptVersion()), CreatedBy: orgID, CreatedAt: now, UpdatedAt: now}
	if err = repo.createSuite(ctx, row); err != nil {
		return nil, apierrors.New(apierrors.CodeEvaluationNameConflict)
	}
	s.audit(ctx, "evaluation.created", orgID, row.EvaluationID)
	return &evaluationv1.EvaluationResponse{Response: okResponse(), Evaluation: suiteToProto(row, nil, "")}, nil
}

func (s *Service) ListEvaluations(ctx context.Context, req *evaluationv1.ListEvaluationsRequest) (*evaluationv1.ListEvaluationsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	offset, limit := pageBounds(req.GetPage())
	rows, total, err := repo.listSuites(ctx, orgID, strings.TrimSpace(req.GetSearch()), offset, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*evaluationv1.Evaluation, 0, len(rows))
	for i := range rows {
		cases, err := repo.cases(ctx, orgID, rows[i].EvaluationID)
		if err != nil {
			return nil, err
		}
		out = append(out, suiteToProto(&rows[i], cases, ""))
	}
	return &evaluationv1.ListEvaluationsResponse{Response: okResponse(), Evaluations: out, PageMeta: pageMeta(offset, limit, total)}, nil
}

func (s *Service) GetEvaluation(ctx context.Context, req *evaluationv1.GetEvaluationRequest) (*evaluationv1.EvaluationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.suite(ctx, orgID, req.GetEvaluationId())
	if err != nil {
		return nil, ErrEvaluationNotFound
	}
	cases, err := repo.cases(ctx, orgID, row.EvaluationID)
	if err != nil {
		return nil, err
	}
	_, _, name, _ := s.promptVersion(ctx, orgID, row.PromptID, row.PromptVersion)
	return &evaluationv1.EvaluationResponse{Response: okResponse(), Evaluation: suiteToProto(row, cases, name)}, nil
}

func (s *Service) CreateEvaluationCase(ctx context.Context, req *evaluationv1.CreateEvaluationCaseRequest) (*evaluationv1.EvaluationCaseResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireWriteRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	suite, err := repo.suite(ctx, orgID, req.GetEvaluationId())
	if err != nil {
		return nil, ErrEvaluationNotFound
	}
	count, err := repo.countCases(ctx, orgID, suite.EvaluationID)
	if err != nil {
		return nil, err
	}
	if count >= 50 {
		return nil, apierrors.New(apierrors.CodeEvaluationCaseInvalid)
	}
	variables, err := validateVariables(ctx, req.GetVariables(), orgID, suite, s)
	if err != nil {
		return nil, err
	}
	checks := checksFromProto(req.GetChecks())
	if err = ValidateChecks(checks); err != nil {
		return nil, apierrors.New(apierrors.CodeEvaluationCheckInvalid)
	}
	checksJSON, _ := json.Marshal(checks)
	name := strings.TrimSpace(req.GetName())
	if name == "" || len(name) > 128 || len(req.GetExpectedOutput()) > 10000 {
		return nil, apierrors.New(apierrors.CodeEvaluationCaseInvalid)
	}
	now := time.Now().UTC()
	row := &EvaluationCase{EvaluationID: suite.EvaluationID, OrganizationID: orgID, Position: int(count), Name: name, Variables: variables, ExpectedOutput: req.GetExpectedOutput(), Checks: string(checksJSON), CreatedAt: now, UpdatedAt: now}
	if err = repo.addCase(ctx, row); err != nil {
		return nil, err
	}
	return &evaluationv1.EvaluationCaseResponse{Response: okResponse(), Case: caseToProto(row)}, nil
}

func (s *Service) UpdateEvaluationCase(ctx context.Context, req *evaluationv1.UpdateEvaluationCaseRequest) (*evaluationv1.EvaluationCaseResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireWriteRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	suite, err := repo.suite(ctx, orgID, req.GetEvaluationId())
	if err != nil {
		return nil, ErrEvaluationNotFound
	}
	row, err := repo.findCase(ctx, orgID, suite.EvaluationID, req.GetCaseId())
	if err != nil {
		return nil, ErrEvaluationNotFound
	}
	variables, err := validateVariables(ctx, req.GetVariables(), orgID, suite, s)
	if err != nil {
		return nil, err
	}
	checks := checksFromProto(req.GetChecks())
	if err = ValidateChecks(checks); err != nil {
		return nil, apierrors.New(apierrors.CodeEvaluationCheckInvalid)
	}
	row.Name = strings.TrimSpace(req.GetName())
	row.Variables = variables
	row.ExpectedOutput = req.GetExpectedOutput()
	raw, _ := json.Marshal(checks)
	row.Checks = string(raw)
	if row.Name == "" || len(row.Name) > 128 || len(row.ExpectedOutput) > 10000 {
		return nil, apierrors.New(apierrors.CodeEvaluationCaseInvalid)
	}
	if err = repo.updateCase(ctx, row); err != nil {
		return nil, err
	}
	return &evaluationv1.EvaluationCaseResponse{Response: okResponse(), Case: caseToProto(row)}, nil
}

func (s *Service) DeleteEvaluationCase(ctx context.Context, req *evaluationv1.DeleteEvaluationCaseRequest) (*evaluationv1.DeleteEvaluationCaseResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireWriteRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err = repo.suite(ctx, orgID, req.GetEvaluationId()); err != nil {
		return nil, ErrEvaluationNotFound
	}
	if err = repo.deleteCase(ctx, orgID, req.GetEvaluationId(), req.GetCaseId()); err != nil {
		return nil, err
	}
	return &evaluationv1.DeleteEvaluationCaseResponse{Response: okResponse()}, nil
}

func (s *Service) UpdateEvaluation(ctx context.Context, req *evaluationv1.UpdateEvaluationRequest) (*evaluationv1.EvaluationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireWriteRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.suite(ctx, orgID, req.GetEvaluationId())
	if err != nil {
		return nil, ErrEvaluationNotFound
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" || len(name) > 128 || len(req.GetDescription()) > 500 {
		return nil, apierrors.New(apierrors.CodeEvaluationCaseInvalid)
	}
	// The prompt binding only changes when a new promptId/version pair
	// is supplied; a name/description-only update keeps the existing
	// binding (FR2.2).
	promptID, promptVersion := row.PromptID, row.PromptVersion
	if req.GetPromptVersion() > 0 || req.GetPromptId() != "" {
		if req.GetPromptVersion() <= 0 || req.GetPromptId() == "" {
			return nil, apierrors.New(apierrors.CodeEvaluationCaseInvalid)
		}
		if _, _, _, err = s.promptVersion(ctx, orgID, req.GetPromptId(), int(req.GetPromptVersion())); err != nil {
			return nil, err
		}
		promptID, promptVersion = req.GetPromptId(), int(req.GetPromptVersion())
	}
	row.Name, row.Description, row.PromptID, row.PromptVersion = name, req.GetDescription(), promptID, promptVersion
	if err = repo.updateSuite(ctx, row); err != nil {
		return nil, err
	}
	cases, err := repo.cases(ctx, orgID, row.EvaluationID)
	if err != nil {
		return nil, err
	}
	_, _, promptName, _ := s.promptVersion(ctx, orgID, row.PromptID, row.PromptVersion)
	s.audit(ctx, "evaluation.updated", orgID, row.EvaluationID)
	return &evaluationv1.EvaluationResponse{Response: okResponse(), Evaluation: suiteToProto(row, cases, promptName)}, nil
}
func (s *Service) DeleteEvaluation(ctx context.Context, req *evaluationv1.DeleteEvaluationRequest) (*evaluationv1.DeleteEvaluationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireWriteRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if err = repo.softDeleteSuite(ctx, orgID, req.GetEvaluationId()); err != nil {
		return nil, ErrEvaluationNotFound
	}
	s.audit(ctx, "evaluation.deleted", orgID, req.GetEvaluationId())
	return &evaluationv1.DeleteEvaluationResponse{Response: okResponse()}, nil
}
func (s *Service) ReorderEvaluationCases(ctx context.Context, req *evaluationv1.ReorderEvaluationCasesRequest) (*evaluationv1.EvaluationResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireWriteRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.suite(ctx, orgID, req.GetEvaluationId())
	if err != nil {
		return nil, ErrEvaluationNotFound
	}
	if err = repo.reorderCases(ctx, orgID, row.EvaluationID, req.GetCaseIds()); err != nil {
		return nil, apierrors.New(apierrors.CodeEvaluationCaseInvalid)
	}
	cases, err := repo.cases(ctx, orgID, row.EvaluationID)
	if err != nil {
		return nil, err
	}
	return &evaluationv1.EvaluationResponse{Response: okResponse(), Evaluation: suiteToProto(row, cases, "")}, nil
}
func (s *Service) CreateEvaluationRun(ctx context.Context, req *evaluationv1.CreateEvaluationRunRequest) (*evaluationv1.EvaluationRunResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireWriteRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	suite, err := repo.suite(ctx, orgID, req.GetEvaluationId())
	if err != nil {
		return nil, ErrEvaluationNotFound
	}
	if int(req.GetPromptVersion()) != suite.PromptVersion || strings.TrimSpace(req.GetModelId()) == "" || strings.TrimSpace(req.GetApiKeyId()) == "" {
		return nil, apierrors.New(apierrors.CodeEvaluationRunStateInvalid)
	}
	if s.apiKeys != nil {
		if err = s.apiKeys.ValidateActiveAPIKey(ctx, orgID, req.GetApiKeyId()); err != nil {
			return nil, apierrors.New(apierrors.CodeAPIKeyNotFound)
		}
	}
	content, variables, _, err := s.promptVersion(ctx, orgID, suite.PromptID, suite.PromptVersion)
	if err != nil {
		return nil, err
	}
	cases, err := repo.cases(ctx, orgID, suite.EvaluationID)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool)
	for _, id := range req.GetCaseIds() {
		if wanted[id] {
			return nil, apierrors.New(apierrors.CodeEvaluationCaseInvalid)
		}
		wanted[id] = true
	}
	selected := make([]EvaluationRunCase, 0, len(cases))
	for _, item := range cases {
		if len(wanted) != 0 && !wanted[item.CaseID] {
			continue
		}
		caseID := item.CaseID
		selected = append(selected, EvaluationRunCase{ResultID: uuid.NewString(), CaseID: &caseID, CasePosition: item.Position, CaseName: item.Name, Variables: item.Variables, ExpectedOutput: item.ExpectedOutput, Checks: item.Checks, Status: CasePending, CreatedAt: time.Now().UTC()})
	}
	if len(wanted) > 0 && len(selected) != len(wanted) {
		return nil, ErrEvaluationNotFound
	}
	if len(selected) == 0 || len(selected) > 50 {
		return nil, apierrors.New(apierrors.CodeEvaluationLimitExceeded)
	}
	// The completion seam must be wired before a run can be accepted
	// (feature #44, AD5): a run without a real metered executor would
	// necessarily persist simulated results, so it fails closed.
	if s.completions == nil {
		return nil, apierrors.New(apierrors.CodeEvaluationRunStateInvalid)
	}
	varsJSON, _ := json.Marshal(variables)
	now := time.Now().UTC()
	run := &EvaluationRun{RunID: uuid.NewString(), EvaluationID: suite.EvaluationID, OrganizationID: orgID, Status: RunPending, PromptID: suite.PromptID, PromptVersion: suite.PromptVersion, PromptContentSnapshot: content, PromptVariables: string(varsJSON), ModelID: req.GetModelId(), APIKeyID: req.GetApiKeyId(), CaseCount: len(selected), CreatedBy: orgID, CreatedAt: now, RetainedUntil: now.Add(2160 * time.Hour), Currency: "USD"}
	if err = repo.createRunSnapshot(ctx, run, selected, config.GetConfig().Evaluation.MaxInFlightRunsPerOrg); err != nil {
		return nil, mapRunError(err)
	}
	// The EvaluationRunner (a server.Runner) claims the pending run via
	// a database lease and executes every case through the real metered
	// completion path (AD5); CreateEvaluationRun only snapshots and
	// enqueues (architecture Section 7).
	stored, err := repo.run(ctx, orgID, suite.EvaluationID, run.RunID)
	if err != nil {
		return nil, err
	}
	resultCases, err := repo.runCases(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	stored.CaseCount = run.CaseCount
	return &evaluationv1.EvaluationRunResponse{Response: okResponse(), Run: runToProto(stored, resultCases)}, nil
}
func (s *Service) ListEvaluationRuns(ctx context.Context, req *evaluationv1.ListEvaluationRunsRequest) (*evaluationv1.ListEvaluationRunsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err = repo.suite(ctx, orgID, req.GetEvaluationId()); err != nil {
		return nil, ErrEvaluationNotFound
	}
	offset, limit := pageBounds(req.GetPage())
	rows, total, err := repo.listRuns(ctx, orgID, req.GetEvaluationId(), req.GetStatus(), offset, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*evaluationv1.EvaluationRun, 0, len(rows))
	for i := range rows {
		out = append(out, runToProto(&rows[i], nil))
	}
	return &evaluationv1.ListEvaluationRunsResponse{Response: okResponse(), Runs: out, PageMeta: pageMeta(offset, limit, total)}, nil
}
func (s *Service) GetEvaluationRun(ctx context.Context, req *evaluationv1.GetEvaluationRunRequest) (*evaluationv1.EvaluationRunResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err = repo.suite(ctx, orgID, req.GetEvaluationId()); err != nil {
		return nil, ErrEvaluationNotFound
	}
	run, err := repo.run(ctx, orgID, req.GetEvaluationId(), req.GetRunId())
	if err != nil {
		return nil, ErrEvaluationRunNotFound
	}
	cases, err := repo.runCases(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	return &evaluationv1.EvaluationRunResponse{Response: okResponse(), Run: runToProto(run, cases)}, nil
}
func (s *Service) CancelEvaluationRun(ctx context.Context, req *evaluationv1.CancelEvaluationRunRequest) (*evaluationv1.EvaluationRunResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireWriteRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	run, err := repo.run(ctx, orgID, req.GetEvaluationId(), req.GetRunId())
	if err != nil {
		return nil, ErrEvaluationRunNotFound
	}
	if err = repo.requestCancel(ctx, run); err != nil {
		return nil, ErrEvaluationRunStateInvalid
	}
	run, err = repo.run(ctx, orgID, req.GetEvaluationId(), req.GetRunId())
	if err != nil {
		return nil, err
	}
	cases, err := repo.runCases(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	return &evaluationv1.EvaluationRunResponse{Response: okResponse(), Run: runToProto(run, cases)}, nil
}
func (s *Service) CompareEvaluationRuns(ctx context.Context, req *evaluationv1.CompareEvaluationRunsRequest) (*evaluationv1.CompareEvaluationRunsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err = repo.suite(ctx, orgID, req.GetEvaluationId()); err != nil {
		return nil, ErrEvaluationNotFound
	}
	left, err := repo.run(ctx, orgID, req.GetEvaluationId(), req.GetLeftRunId())
	if err != nil {
		return nil, ErrEvaluationRunNotFound
	}
	right, err := repo.run(ctx, orgID, req.GetEvaluationId(), req.GetRightRunId())
	if err != nil {
		return nil, ErrEvaluationRunNotFound
	}
	if left.Status == RunPending || left.Status == RunRunning || right.Status == RunPending || right.Status == RunRunning {
		return nil, ErrEvaluationRunStateInvalid
	}
	lc, err := repo.runCases(ctx, left.RunID)
	if err != nil {
		return nil, err
	}
	rc, err := repo.runCases(ctx, right.RunID)
	if err != nil {
		return nil, err
	}
	leftByID := map[string]EvaluationRunCase{}
	rightByID := map[string]EvaluationRunCase{}
	for _, c := range lc {
		if c.CaseID != nil {
			leftByID[*c.CaseID] = c
		}
	}
	for _, c := range rc {
		if c.CaseID != nil {
			rightByID[*c.CaseID] = c
		}
	}
	ids := map[string]bool{}
	for id := range leftByID {
		ids[id] = true
	}
	for id := range rightByID {
		ids[id] = true
	}
	out := &evaluationv1.CompareEvaluationRunsResponse{Response: okResponse(), Left: runToProto(left, lc), Right: runToProto(right, rc), CostDeltaMinor: right.TotalCostMinor - left.TotalCostMinor, MedianLatencyDeltaMs: right.MedianLatencyMS - left.MedianLatencyMS}
	for id := range ids {
		l, lok := leftByID[id]
		r, rok := rightByID[id]
		item := &evaluationv1.EvaluationRunCaseComparison{CaseId: id}
		if lok {
			item.CaseName = l.CaseName
			item.Left = runCaseToProto(&l)
		}
		if rok {
			item.CaseName = r.CaseName
			item.Right = runCaseToProto(&r)
		}
		item.OnlyInLeft = lok && !rok
		item.OnlyInRight = rok && !lok
		out.Cases = append(out.Cases, item)
	}
	return out, nil
}

func validateVariables(ctx context.Context, values map[string]string, orgID string, suite *Evaluation, s *Service) (string, error) {
	if values == nil {
		return "", apierrors.New(apierrors.CodeEvaluationCaseInvalid)
	}
	_, variables, _, err := s.promptVersion(ctx, orgID, suite.PromptID, suite.PromptVersion)
	if err != nil {
		return "", err
	}
	declared := make(map[string]bool, len(variables))
	for _, variable := range variables {
		key := strings.TrimSuffix(strings.TrimPrefix(variable, "${"), "}")
		declared[key] = true
		if strings.TrimSpace(values[key]) == "" {
			return "", apierrors.New(apierrors.CodeEvaluationCaseInvalid)
		}
	}
	for key, value := range values {
		if !declared[key] || strings.TrimSpace(value) == "" || len(value) > 10000 {
			return "", apierrors.New(apierrors.CodeEvaluationCaseInvalid)
		}
	}
	encoded, err := json.Marshal(values)
	return string(encoded), err
}
func checksFromProto(in []*evaluationv1.EvaluationCheck) []Check {
	out := make([]Check, 0, len(in))
	for _, c := range in {
		if c != nil {
			out = append(out, Check{Type: c.GetType(), Expected: c.GetExpected()})
		}
	}
	return out
}
func checksToProto(raw string) []*evaluationv1.EvaluationCheck {
	var checks []Check
	_ = json.Unmarshal([]byte(raw), &checks)
	out := make([]*evaluationv1.EvaluationCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, &evaluationv1.EvaluationCheck{Type: c.Type, Expected: c.Expected})
	}
	return out
}
func suiteToProto(row *Evaluation, cases []EvaluationCase, promptName string) *evaluationv1.Evaluation {
	out := &evaluationv1.Evaluation{EvaluationId: row.EvaluationID, Name: row.Name, Description: row.Description, PromptId: row.PromptID, PromptVersion: clampInt32(row.PromptVersion), PromptName: promptName, CaseCount: clampInt32(len(cases)), CreatedAt: row.CreatedAt.Format(time.RFC3339), UpdatedAt: row.UpdatedAt.Format(time.RFC3339)}
	for i := range cases {
		out.Cases = append(out.Cases, caseToProto(&cases[i]))
	}
	return out
}
func caseToProto(row *EvaluationCase) *evaluationv1.EvaluationCase {
	return &evaluationv1.EvaluationCase{CaseId: row.CaseID, EvaluationId: row.EvaluationID, Position: clampInt32(row.Position), Name: row.Name, VariablesJson: row.Variables, ExpectedOutput: row.ExpectedOutput, Checks: checksToProto(row.Checks), CreatedAt: row.CreatedAt.Format(time.RFC3339), UpdatedAt: row.UpdatedAt.Format(time.RFC3339)}
}
func okResponse() *commonv1.Response { return &commonv1.Response{Code: 0, Message: "OK"} }
func pageBounds(page *commonv1.PageRequest) (int, int) {
	offset, limit := 0, 20
	if page != nil {
		if page.Offset > 0 {
			offset = int(page.Offset)
		}
		if page.Limit > 0 {
			limit = int(page.Limit)
		}
		if limit > 100 {
			limit = 100
		}
	}
	return offset, limit
}
func pageMeta(offset, limit int, total int64) *commonv1.PageMeta {
	return &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)}
}

func clampInt32(value int) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	return int32(value)
}

func runToProto(row *EvaluationRun, cases []EvaluationRunCase) *evaluationv1.EvaluationRun {
	out := &evaluationv1.EvaluationRun{RunId: row.RunID, EvaluationId: row.EvaluationID, Status: row.Status, PromptId: row.PromptID, PromptVersion: clampInt32(row.PromptVersion), PromptContent: row.PromptContentSnapshot, ModelId: row.ModelID, ApiKeyId: row.APIKeyID, CaseCount: clampInt32(row.CaseCount), CompletedCount: clampInt32(row.CompletedCount), PassedCount: clampInt32(row.PassedCount), FailedCount: clampInt32(row.FailedCount), ErrorCount: clampInt32(row.ErrorCount), CancelledCount: clampInt32(row.CancelledCount), TotalCostMinor: row.TotalCostMinor, Currency: row.Currency, MedianLatencyMs: row.MedianLatencyMS, CreatedAt: row.CreatedAt.Format(time.RFC3339)}
	_ = json.Unmarshal([]byte(row.PromptVariables), &out.PromptVariables)
	if row.StartedAt != nil {
		out.StartedAt = row.StartedAt.Format(time.RFC3339)
	}
	if row.CompletedAt != nil {
		out.CompletedAt = row.CompletedAt.Format(time.RFC3339)
	}
	for i := range cases {
		out.Cases = append(out.Cases, runCaseToProto(&cases[i]))
	}
	return out
}

func runCaseToProto(row *EvaluationRunCase) *evaluationv1.EvaluationRunCase {
	out := &evaluationv1.EvaluationRunCase{ResultId: row.ResultID, CasePosition: clampInt32(row.CasePosition), CaseName: row.CaseName, VariablesJson: row.Variables, ExpectedOutput: row.ExpectedOutput, Checks: checksToProto(row.Checks), Status: row.Status, Completion: row.Completion, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, LatencyMs: row.LatencyMS, CostMinor: row.CostMinor, Currency: row.Currency, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage}
	if row.CaseID != nil {
		out.CaseId = *row.CaseID
	}
	var checkResults []CheckResult
	_ = json.Unmarshal([]byte(row.CheckResults), &checkResults)
	for _, result := range checkResults {
		out.CheckResults = append(out.CheckResults, &evaluationv1.EvaluationCheckResult{Type: result.Type, Expected: result.Expected, Passed: result.Passed, Message: result.Message})
	}
	if row.CompletedAt != nil {
		out.CompletedAt = row.CompletedAt.Format(time.RFC3339)
	}
	return out
}

func mapRunError(err error) error {
	switch {
	case errors.Is(err, ErrEvaluationLimitExceeded):
		return ErrEvaluationLimitExceeded
	case errors.Is(err, ErrEvaluationCaseInvalid):
		return ErrEvaluationCaseInvalid
	default:
		return err
	}
}
