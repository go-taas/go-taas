// Engine parameter template RPCs: CRUD, context matching and the portable
// export/import document.

package infer

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"gorm.io/datatypes"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Bounds and constants for the template feature.
const (
	// maxTemplateName bounds a template name.
	maxTemplateName = 128
	// maxTemplateDescription bounds the description.
	maxTemplateDescription = 1024
	// templateExportVersion is the document format version. An import of a
	// document with a different version is refused rather than
	// half-understood.
	templateExportVersion = 1
	// maxTemplateExportBytes bounds an imported document.
	maxTemplateExportBytes = 4 << 20
)

// templateExportDocument is the JSON document Export/Import use.
type templateExportDocument struct {
	Version    int                          `json:"version"`
	ExportedAt string                       `json:"exportedAt,omitempty"`
	Templates  []templateExportDocumentItem `json:"templates"`
}

// templateExportDocumentItem is one template in the export document. It
// carries the same fields the create request does, so a document is portable
// between installations.
type templateExportDocumentItem struct {
	Name            string            `json:"name"`
	Description     string            `json:"description,omitempty"`
	Engine          string            `json:"engine"`
	ModelID         string            `json:"modelId,omitempty"`
	Topology        string            `json:"topology,omitempty"`
	Accelerator     string            `json:"accelerator,omitempty"`
	AcceleratorType string            `json:"acceleratorType,omitempty"`
	EngineArgs      map[string]string `json:"engineArgs,omitempty"`
	ExtraArgs       []string          `json:"extraArgs,omitempty"`
	RoleEngineArgs  []RoleEngineArgs  `json:"roleEngineArgs,omitempty"`
}

// templateRepository lazily resolves the template repository.
func (s *Service) templateRepository() (*EngineParamTemplateRepository, error) {
	if s.templateRepo != nil {
		if err := s.templateRepo.db.DB(context.Background()).AutoMigrate(&EngineParamTemplate{}); err != nil {
			return nil, err
		}
		return s.templateRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&EngineParamTemplate{}); err != nil {
		return nil, err
	}
	s.templateRepo = NewEngineParamTemplateRepository(db)
	return s.templateRepo, nil
}

// requireTemplateAdmin gates the template RPCs on the platform administrator
// role: the fleet's engines are a platform concern.
func (s *Service) requireTemplateAdmin(ctx context.Context) error {
	if s.platformAdminGuard == nil {
		return nil
	}
	return s.platformAdminGuard.RequirePlatformAdmin(ctx)
}

// templateInput is one validated template body.
type templateInput struct {
	Name            string
	Description     string
	Engine          string
	ModelID         string
	Topology        string
	Accelerator     string
	AcceleratorType string
	EngineArgs      datatypes.JSON
	ExtraArgs       datatypes.JSON
	RoleEngineArgs  datatypes.JSON
}

// validateTemplate normalizes and validates the request fields. Nothing is
// written when it fails.
func (s *Service) validateTemplate(
	name, description, engine, modelID, topology, accelerator, acceleratorType string,
	engineArgs map[string]string,
	extraArgs []string,
	roleArgs []*inferv1.RoleEngineArgs,
) (templateInput, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" || len(trimmedName) > maxTemplateName {
		return templateInput{}, apierrors.Newf(apierrors.CodeEngineTemplateInvalid,
			"infer: the template name must be 1-%d characters", maxTemplateName)
	}
	trimmedEngine := strings.ToLower(strings.TrimSpace(engine))
	if trimmedEngine == "" {
		return templateInput{}, apierrors.New(apierrors.CodeEngineTemplateInvalid)
	}
	normalizedTopology := storedTopologyOrEmpty(topology)
	if normalizedTopology != "" &&
		normalizedTopology != TopologySingle &&
		normalizedTopology != TopologyMultiNode &&
		normalizedTopology != TopologyPD {
		return templateInput{}, apierrors.Newf(apierrors.CodeEngineTemplateInvalid,
			"infer: unknown topology %q; use single, multinode or pd", topology)
	}
	normalizedAccelerator := strings.ToLower(strings.TrimSpace(accelerator))
	if normalizedAccelerator != "" && !isValidAcceleratorToken(normalizedAccelerator) {
		return templateInput{}, apierrors.Newf(apierrors.CodeEngineTemplateInvalid,
			"infer: unknown accelerator %q", accelerator)
	}
	trimmedDescription := strings.TrimSpace(description)
	if len(trimmedDescription) > maxTemplateDescription {
		return templateInput{}, apierrors.Newf(apierrors.CodeEngineTemplateInvalid,
			"infer: the description must be at most %d characters", maxTemplateDescription)
	}

	storedArgs, err := normalizeEngineArgs(engineArgs)
	if err != nil {
		return templateInput{}, err
	}
	storedExtra, err := normalizeExtraArgs(extraArgs)
	if err != nil {
		return templateInput{}, err
	}
	storedRoles, err := normalizeRoleEngineArgs(roleArgs)
	if err != nil {
		return templateInput{}, err
	}
	// A template with no parameters records nothing worth loading. The deploy
	// form refuses one before sending it; the API refuses it too, because a
	// client other than the console would otherwise fill the list with empty
	// rows that match everything and change nothing.
	if isEmptyJSONObject(storedArgs) && isEmptyJSONArray(storedExtra) && isEmptyJSONArray(storedRoles) {
		return templateInput{}, apierrors.Newf(apierrors.CodeEngineTemplateInvalid,
			"infer: the template needs at least one engine parameter, extra argument or role override")
	}
	return templateInput{
		Name:            trimmedName,
		Description:     trimmedDescription,
		Engine:          trimmedEngine,
		ModelID:         strings.TrimSpace(modelID),
		Topology:        normalizedTopology,
		Accelerator:     normalizedAccelerator,
		AcceleratorType: strings.TrimSpace(acceleratorType),
		EngineArgs:      storedArgs,
		ExtraArgs:       storedExtra,
		RoleEngineArgs:  storedRoles,
	}, nil
}

// isEmptyJSONObject reports whether a stored JSON document holds no entries.
// The encoded form is what the row carries, so the emptiness check looks at
// the document rather than at the request that produced it.
func isEmptyJSONObject(doc datatypes.JSON) bool {
	return isEmptyJSON(doc, "{}")
}

// isEmptyJSONArray is the list counterpart of isEmptyJSONObject.
func isEmptyJSONArray(doc datatypes.JSON) bool {
	return isEmptyJSON(doc, "[]")
}

// isEmptyJSON compares a stored document against an empty shape, tolerating
// the null an absent value encodes to.
func isEmptyJSON(doc datatypes.JSON, empty string) bool {
	trimmed := strings.TrimSpace(string(doc))
	return trimmed == "" || trimmed == "null" || trimmed == empty
}

// isValidAcceleratorToken accepts the CPU marker or a vendor token, the same
// shape the image catalog stores.
func isValidAcceleratorToken(value string) bool {
	if value == "cpu" {
		return true
	}
	if len(value) > 32 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// CreateEngineParamTemplate saves a template.
func (s *Service) CreateEngineParamTemplate(ctx context.Context, req *inferv1.CreateEngineParamTemplateRequest) (*inferv1.CreateEngineParamTemplateResponse, error) {
	if err := s.requireTemplateAdmin(ctx); err != nil {
		return nil, err
	}
	in, err := s.validateTemplate(req.GetName(), req.GetDescription(), req.GetEngine(), req.GetModelId(),
		req.GetTopology(), req.GetAccelerator(), req.GetAcceleratorType(),
		req.GetEngineArgs(), req.GetExtraArgs(), req.GetRoleEngineArgs())
	if err != nil {
		return nil, err
	}
	repo, err := s.templateRepository()
	if err != nil {
		return nil, err
	}
	row := &EngineParamTemplate{
		ID:              NewServiceID(),
		Name:            in.Name,
		Description:     in.Description,
		Engine:          in.Engine,
		ModelID:         in.ModelID,
		Topology:        in.Topology,
		Accelerator:     in.Accelerator,
		AcceleratorType: in.AcceleratorType,
		EngineArgs:      in.EngineArgs,
		ExtraArgs:       in.ExtraArgs,
		RoleEngineArgs:  in.RoleEngineArgs,
		UpdatedBy:       s.templateActor(ctx),
	}
	if err := repo.Create(ctx, row); err != nil {
		return nil, err
	}
	return &inferv1.CreateEngineParamTemplateResponse{
		Response: okResponse(),
		Template: s.templateSummaries(ctx, []*EngineParamTemplate{row})[0],
	}, nil
}

// ListEngineParamTemplates lists the templates, narrowed and ordered by the
// match context when one is given.
func (s *Service) ListEngineParamTemplates(ctx context.Context, req *inferv1.ListEngineParamTemplatesRequest) (*inferv1.ListEngineParamTemplatesResponse, error) {
	if err := s.requireTemplateAdmin(ctx); err != nil {
		return nil, err
	}
	repo, err := s.templateRepository()
	if err != nil {
		return nil, err
	}
	matchCtx := templateMatchContext{
		Engine:          strings.ToLower(strings.TrimSpace(req.GetEngine())),
		ModelID:         strings.TrimSpace(req.GetModelId()),
		Topology:        storedTopologyOrEmpty(req.GetTopology()),
		Accelerator:     strings.ToLower(strings.TrimSpace(req.GetAccelerator())),
		AcceleratorType: strings.TrimSpace(req.GetAcceleratorType()),
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.List(ctx, TemplateFilter{
		Engine:          matchCtx.Engine,
		ModelID:         matchCtx.ModelID,
		Topology:        matchCtx.Topology,
		Accelerator:     matchCtx.Accelerator,
		AcceleratorType: matchCtx.AcceleratorType,
		Search:          req.GetSearch(),
	}, offset, limit)
	if err != nil {
		return nil, err
	}
	// Score every row against the same context and order best-first: the
	// deploy form shows the most specific match first.
	if matchCtx.hasContext() {
		sort.SliceStable(rows, func(i, j int) bool {
			return matchCtx.score(rows[i]) > matchCtx.score(rows[j])
		})
	}
	templates := s.templateSummaries(ctx, rows)
	for i, row := range rows {
		templates[i].MatchScore = clampToInt32(matchCtx.score(row))
	}
	return &inferv1.ListEngineParamTemplatesResponse{
		Response:  okResponse(),
		Templates: templates,
		PageMeta:  &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// GetEngineParamTemplate returns one template.
func (s *Service) GetEngineParamTemplate(ctx context.Context, req *inferv1.GetEngineParamTemplateRequest) (*inferv1.GetEngineParamTemplateResponse, error) {
	if err := s.requireTemplateAdmin(ctx); err != nil {
		return nil, err
	}
	repo, err := s.templateRepository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindByID(ctx, req.GetTemplateId())
	if err != nil {
		return nil, err
	}
	return &inferv1.GetEngineParamTemplateResponse{
		Response: okResponse(),
		Template: s.templateSummaries(ctx, []*EngineParamTemplate{row})[0],
	}, nil
}

// UpdateEngineParamTemplate replaces a template's content.
func (s *Service) UpdateEngineParamTemplate(ctx context.Context, req *inferv1.UpdateEngineParamTemplateRequest) (*inferv1.UpdateEngineParamTemplateResponse, error) {
	if err := s.requireTemplateAdmin(ctx); err != nil {
		return nil, err
	}
	in, err := s.validateTemplate(req.GetName(), req.GetDescription(), req.GetEngine(), req.GetModelId(),
		req.GetTopology(), req.GetAccelerator(), req.GetAcceleratorType(),
		req.GetEngineArgs(), req.GetExtraArgs(), req.GetRoleEngineArgs())
	if err != nil {
		return nil, err
	}
	repo, err := s.templateRepository()
	if err != nil {
		return nil, err
	}
	if _, err := repo.FindByID(ctx, req.GetTemplateId()); err != nil {
		return nil, err
	}
	row := &EngineParamTemplate{
		ID:              req.GetTemplateId(),
		Name:            in.Name,
		Description:     in.Description,
		Engine:          in.Engine,
		ModelID:         in.ModelID,
		Topology:        in.Topology,
		Accelerator:     in.Accelerator,
		AcceleratorType: in.AcceleratorType,
		EngineArgs:      in.EngineArgs,
		ExtraArgs:       in.ExtraArgs,
		RoleEngineArgs:  in.RoleEngineArgs,
		UpdatedBy:       s.templateActor(ctx),
		UpdatedAt:       time.Now().UTC(),
	}
	if err := repo.Update(ctx, row); err != nil {
		return nil, err
	}
	stored, err := repo.FindByID(ctx, req.GetTemplateId())
	if err != nil {
		return nil, err
	}
	return &inferv1.UpdateEngineParamTemplateResponse{
		Response: okResponse(),
		Template: s.templateSummaries(ctx, []*EngineParamTemplate{stored})[0],
	}, nil
}

// DeleteEngineParamTemplate removes a template.
func (s *Service) DeleteEngineParamTemplate(ctx context.Context, req *inferv1.DeleteEngineParamTemplateRequest) (*inferv1.DeleteEngineParamTemplateResponse, error) {
	if err := s.requireTemplateAdmin(ctx); err != nil {
		return nil, err
	}
	repo, err := s.templateRepository()
	if err != nil {
		return nil, err
	}
	if err := repo.Delete(ctx, req.GetTemplateId()); err != nil {
		return nil, err
	}
	return &inferv1.DeleteEngineParamTemplateResponse{Response: okResponse()}, nil
}

// ExportEngineParamTemplates renders every template as a portable document.
func (s *Service) ExportEngineParamTemplates(ctx context.Context, _ *inferv1.ExportEngineParamTemplatesRequest) (*inferv1.ExportEngineParamTemplatesResponse, error) {
	if err := s.requireTemplateAdmin(ctx); err != nil {
		return nil, err
	}
	repo, err := s.templateRepository()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	doc := templateExportDocument{
		Version:    templateExportVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Templates:  make([]templateExportDocumentItem, 0, len(rows)),
	}
	for _, row := range rows {
		doc.Templates = append(doc.Templates, templateExportDocumentItem{
			Name:            row.Name,
			Description:     row.Description,
			Engine:          row.Engine,
			ModelID:         row.ModelID,
			Topology:        row.Topology,
			Accelerator:     row.Accelerator,
			AcceleratorType: row.AcceleratorType,
			EngineArgs:      EngineArgsMap(row.EngineArgs),
			ExtraArgs:       ExtraArgsList(row.ExtraArgs),
			RoleEngineArgs:  RoleEngineArgsList(row.RoleEngineArgs),
		})
	}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, apierrors.New(apierrors.CodeInternal)
	}
	return &inferv1.ExportEngineParamTemplatesResponse{
		Response: okResponse(),
		Payload:  string(payload),
		Count:    clampToInt32(len(doc.Templates)),
	}, nil
}

// ImportEngineParamTemplates loads an exported document. strategy is "skip"
// (default) or "overwrite" for a name that already exists.
func (s *Service) ImportEngineParamTemplates(ctx context.Context, req *inferv1.ImportEngineParamTemplatesRequest) (*inferv1.ImportEngineParamTemplatesResponse, error) {
	if err := s.requireTemplateAdmin(ctx); err != nil {
		return nil, err
	}
	payload := strings.TrimSpace(req.GetPayload())
	if payload == "" || len(payload) > maxTemplateExportBytes {
		return nil, apierrors.Newf(apierrors.CodeEngineTemplateInvalid,
			"infer: the import document must be 1-%d bytes", maxTemplateExportBytes)
	}
	overwrite := strings.EqualFold(strings.TrimSpace(req.GetStrategy()), "overwrite")

	var doc templateExportDocument
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return nil, apierrors.Newf(apierrors.CodeEngineTemplateInvalid,
			"infer: the import document is not valid JSON: %v", err)
	}
	if doc.Version != templateExportVersion {
		return nil, apierrors.Newf(apierrors.CodeEngineTemplateInvalid,
			"infer: unsupported document version %d (this platform reads %d)", doc.Version, templateExportVersion)
	}
	if len(doc.Templates) == 0 {
		return nil, apierrors.Newf(apierrors.CodeEngineTemplateInvalid,
			"infer: the import document contains no templates")
	}

	repo, err := s.templateRepository()
	if err != nil {
		return nil, err
	}
	created, overwritten, skipped := 0, 0, 0
	for _, entry := range doc.Templates {
		roleArgs := make([]*inferv1.RoleEngineArgs, 0, len(entry.RoleEngineArgs))
		for _, r := range entry.RoleEngineArgs {
			roleArgs = append(roleArgs, &inferv1.RoleEngineArgs{
				Role: r.Role, EngineArgs: r.EngineArgs, ExtraArgs: r.ExtraArgs,
			})
		}
		in, err := s.validateTemplate(entry.Name, entry.Description, entry.Engine, entry.ModelID,
			entry.Topology, entry.Accelerator, entry.AcceleratorType,
			entry.EngineArgs, entry.ExtraArgs, roleArgs)
		if err != nil {
			// One malformed entry must not abandon the whole document after
			// earlier entries were written: count it and keep going.
			skipped++
			continue
		}
		existing, err := repo.FindByName(ctx, in.Name)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if !overwrite {
				skipped++
				continue
			}
			existing.Description = in.Description
			existing.Engine = in.Engine
			existing.ModelID = in.ModelID
			existing.Topology = in.Topology
			existing.Accelerator = in.Accelerator
			existing.AcceleratorType = in.AcceleratorType
			existing.EngineArgs = in.EngineArgs
			existing.ExtraArgs = in.ExtraArgs
			existing.RoleEngineArgs = in.RoleEngineArgs
			existing.UpdatedBy = s.templateActor(ctx)
			existing.UpdatedAt = time.Now().UTC()
			if err := repo.Update(ctx, existing); err != nil {
				return nil, err
			}
			overwritten++
			continue
		}
		if err := repo.Create(ctx, &EngineParamTemplate{
			ID:              NewServiceID(),
			Name:            in.Name,
			Description:     in.Description,
			Engine:          in.Engine,
			ModelID:         in.ModelID,
			Topology:        in.Topology,
			Accelerator:     in.Accelerator,
			AcceleratorType: in.AcceleratorType,
			EngineArgs:      in.EngineArgs,
			ExtraArgs:       in.ExtraArgs,
			RoleEngineArgs:  in.RoleEngineArgs,
			UpdatedBy:       s.templateActor(ctx),
		}); err != nil {
			return nil, err
		}
		created++
	}
	return &inferv1.ImportEngineParamTemplatesResponse{
		Response:    okResponse(),
		Created:     clampToInt32(created),
		Overwritten: clampToInt32(overwritten),
		Skipped:     clampToInt32(skipped),
	}, nil
}

// templateMatchContext is the deployment context a list request carries.
type templateMatchContext struct {
	Engine          string
	ModelID         string
	Topology        string
	Accelerator     string
	AcceleratorType string
}

// hasContext reports whether the query asked for matching at all.
func (c templateMatchContext) hasContext() bool {
	return c.Engine != "" || c.ModelID != "" || c.Topology != "" || c.Accelerator != "" || c.AcceleratorType != ""
}

// score ranks how specifically a template matches the context: a field that
// equals the context is worth more than one that matches anything, and the
// model is the strongest signal.
func (c templateMatchContext) score(row *EngineParamTemplate) int {
	score := 0
	if row.ModelID != "" && row.ModelID == c.ModelID {
		score += 8
	}
	if row.Topology != "" && c.Topology != "" && storedTopologyName(row.Topology) == c.Topology {
		score += 4
	}
	if row.AcceleratorType != "" && c.AcceleratorType != "" && row.AcceleratorType == c.AcceleratorType {
		score += 2
	}
	if row.Accelerator != "" && c.Accelerator != "" && row.Accelerator == c.Accelerator {
		score += 2
	}
	return score
}

// templateActor resolves the acting administrator. The id is what the row
// keeps: infer has no user-directory seam of its own, and the audit trail
// records the same identifier.
func (s *Service) templateActor(ctx context.Context) string {
	userID, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return ""
	}
	return userID
}

// templateSummaries maps rows to the wire message, resolving model display
// names in one batch.
func (s *Service) templateSummaries(ctx context.Context, rows []*EngineParamTemplate) []*inferv1.EngineParamTemplate {
	names := map[string]string{}
	modelIDs := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if row.ModelID != "" && !seen[row.ModelID] {
			seen[row.ModelID] = true
			modelIDs = append(modelIDs, row.ModelID)
		}
	}
	if len(modelIDs) > 0 {
		if repo, err := s.modelRepository(); err == nil {
			for _, id := range modelIDs {
				if m, err := repo.GetModel(ctx, id); err == nil && m != nil {
					names[id] = m.Name
				}
			}
		}
	}
	out := make([]*inferv1.EngineParamTemplate, 0, len(rows))
	for _, row := range rows {
		out = append(out, &inferv1.EngineParamTemplate{
			TemplateId:      row.ID,
			Name:            row.Name,
			Description:     row.Description,
			Engine:          row.Engine,
			ModelId:         row.ModelID,
			ModelName:       names[row.ModelID],
			Topology:        row.Topology,
			Accelerator:     row.Accelerator,
			AcceleratorType: row.AcceleratorType,
			EngineArgs:      EngineArgsMap(row.EngineArgs),
			ExtraArgs:       ExtraArgsList(row.ExtraArgs),
			RoleEngineArgs:  roleArgsToProto(RoleEngineArgsList(row.RoleEngineArgs)),
			CreatedAt:       row.CreatedAt.Unix(),
			UpdatedAt:       row.UpdatedAt.Unix(),
			UpdatedBy:       row.UpdatedBy,
		})
	}
	return out
}

// roleArgsToProto maps the stored per-role overrides onto the wire message.
func roleArgsToProto(roles []RoleEngineArgs) []*inferv1.RoleEngineArgs {
	out := make([]*inferv1.RoleEngineArgs, 0, len(roles))
	for _, r := range roles {
		out = append(out, &inferv1.RoleEngineArgs{
			Role: r.Role, EngineArgs: r.EngineArgs, ExtraArgs: r.ExtraArgs,
		})
	}
	return out
}

// storedTopologyOrEmpty normalizes a topology name for filtering, keeping ""
// as "any".
func storedTopologyOrEmpty(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case TopologySingle:
		return TopologySingle
	case TopologyMultiNode, legacyTopologyDistributed:
		return TopologyMultiNode
	case TopologyPD:
		return TopologyPD
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}
