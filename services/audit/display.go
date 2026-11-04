package audit

import (
	"context"
	"fmt"
	"strings"

	auditv1 "github.com/go-taas/go-taas/proto/taas/audit/v1"
)

// displaySource describes how to turn an audit event's resource id into a
// human-readable label for one resource type. Table, id column and name
// columns are compile-time constants owned by this file, never caller
// input, so the query is built safely.
type displaySource struct {
	table string
	// idColumn is the column the event's resource_id matches.
	idColumn string
	// nameColumns are tried in order; the first non-empty value wins.
	// Every candidate is a human-readable label (a name, title or display
	// name), never an id.
	nameColumns []string
	// linkColumn, when set, names a column on the resource row that points
	// at an organization: the label is that organization's display name
	// (an account is best identified by its tenant).
	linkColumn string
}

// resourceDisplaySources maps an audit resource_type to the table that
// owns it. Types without a human-readable label (session, runtime
// setting, notification preferences, singleton policy rows) are absent on
// purpose: the console then shows the friendly type label plus the id.
var resourceDisplaySources = map[string]displaySource{
	"organization":      {table: "organizations", idColumn: "id", nameColumns: []string{"display_name"}},
	"user":              {table: "users", idColumn: "id", nameColumns: []string{"display_name", "username"}},
	"project":           {table: "projects", idColumn: "id", nameColumns: []string{"display_name"}},
	"model":             {table: "models", idColumn: "id", nameColumns: []string{"name"}},
	"runtime_model":     {table: "models", idColumn: "id", nameColumns: []string{"name"}},
	"inference_service": {table: "inference_services", idColumn: "id", nameColumns: []string{"name"}},
	"api_key":           {table: "api_keys", idColumn: "id", nameColumns: []string{"name"}},
	"image":             {table: "images", idColumn: "id", nameColumns: []string{"name"}},
	"webhook":           {table: "webhooks", idColumn: "webhook_id", nameColumns: []string{"name"}},
	"evaluation":        {table: "evaluations", idColumn: "evaluation_id", nameColumns: []string{"name"}},
	"prompt":            {table: "prompts", idColumn: "prompt_id", nameColumns: []string{"name"}},
	"routing_group":     {table: "routing_groups", idColumn: "id", nameColumns: []string{"name"}},
	// A routing policy is keyed by the model it configures, so the model
	// name is the label the operator recognises.
	"routing_policy":         {table: "models", idColumn: "id", nameColumns: []string{"name"}},
	"vgpu_pool":              {table: "vgpu_pools", idColumn: "id", nameColumns: []string{"name"}},
	"report":                 {table: "billing_reports", idColumn: "id", nameColumns: []string{"name"}},
	"schedule":               {table: "billing_report_schedules", idColumn: "id", nameColumns: []string{"name"}},
	"incident":               {table: "incidents", idColumn: "id", nameColumns: []string{"target_name"}},
	"batch_job":              {table: "batch_jobs", idColumn: "batch_id", nameColumns: []string{"model"}},
	"account":                {table: "accounts", idColumn: "id", linkColumn: "organization_id"},
	"notification_threshold": {table: "notification_thresholds", idColumn: "threshold_id", nameColumns: []string{"name"}},
	// A membership row's resource id is the member's user id.
	"member":         {table: "users", idColumn: "id", nameColumns: []string{"display_name", "username"}},
	"project_member": {table: "users", idColumn: "id", nameColumns: []string{"display_name", "username"}},
}

// resolveOrgLabels maps organization ids to display names. A read failure
// yields no labels (the console falls back to the id).
func (r *Repository) resolveOrgLabels(ctx context.Context, ids []string) map[string]string {
	ids = uniqueNonEmpty(ids)
	if len(ids) == 0 {
		return nil
	}
	var rows []struct {
		ID          string
		DisplayName string
	}
	if err := r.db.DB(ctx).Table("organizations").
		Select("id, display_name").
		Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.DisplayName != "" {
			out[row.ID] = row.DisplayName
		}
	}
	return out
}

// resolveResourceLabels maps (resource_type, resource_id) pairs to display
// names for the types that carry a human-readable label. A miss or a read
// error simply leaves the id to be shown.
func (r *Repository) resolveResourceLabels(ctx context.Context, rows []*AuditEvent) map[string]string {
	idsByType := map[string][]string{}
	for _, row := range rows {
		if row.ResourceID == "" {
			continue
		}
		if _, ok := resourceDisplaySources[row.ResourceType]; !ok {
			continue
		}
		idsByType[row.ResourceType] = append(idsByType[row.ResourceType], row.ResourceID)
	}
	if len(idsByType) == 0 {
		return nil
	}
	out := map[string]string{}
	for resourceType, originalIDs := range idsByType {
		src := resourceDisplaySources[resourceType]
		// One event id may map to several lookup candidates (a membership
		// row stores "project/user"), so keep the reverse mapping to put
		// the resolved label back under the stored id.
		originalsByCandidate := map[string][]string{}
		for _, id := range originalIDs {
			for _, candidate := range candidateIDs(resourceType, id) {
				originalsByCandidate[candidate] = append(originalsByCandidate[candidate], id)
			}
		}
		candidates := make([]string, 0, len(originalsByCandidate))
		for candidate := range originalsByCandidate {
			candidates = append(candidates, candidate)
		}
		labels := r.labelsForSource(ctx, src, candidates)
		for candidate, label := range labels {
			for _, original := range originalsByCandidate[candidate] {
				out[labelKey(resourceType, original)] = label
			}
		}
	}
	return out
}

// candidateIDs returns the ids to look up for one event's resource id.
// A membership event stores "project/user", and the stored composite is
// not a valid key: query with the trailing user id only.
func candidateIDs(resourceType, id string) []string {
	if resourceType != "member" && resourceType != "project_member" {
		return []string{id}
	}
	if _, rest, ok := strings.Cut(id, "/"); ok && rest != "" {
		return []string{rest}
	}
	return []string{id}
}

// plausibleIDs drops values that cannot be a primary key. The id columns
// are a mix of uuid and text, and PostgreSQL rejects the whole IN list
// when one value cannot cast to uuid, so a malformed resource id must
// never reach the query.
func plausibleIDs(ids []string) []string {
	out := ids[:0]
	for _, id := range ids {
		if strings.ContainsAny(id, "/ \t\n") {
			continue
		}
		out = append(out, id)
	}
	return out
}

// labelsForSource resolves the label of each id from one display source.
func (r *Repository) labelsForSource(ctx context.Context, src displaySource, ids []string) map[string]string {
	ids = plausibleIDs(uniqueNonEmpty(ids))
	if len(ids) == 0 {
		return nil
	}
	if src.linkColumn != "" {
		// The label belongs to the linked row (an account's organization),
		// so resolve the link first and the organization labels second.
		var links []struct {
			RowID string
			Link  string
		}
		if err := r.db.DB(ctx).Table(src.table).
			Select(src.idColumn+" AS row_id, "+src.linkColumn+" AS link").
			Where(src.idColumn+" IN ?", ids).Find(&links).Error; err != nil {
			return nil
		}
		linkValues := make([]string, 0, len(links))
		for _, link := range links {
			linkValues = append(linkValues, link.Link)
		}
		orgLabels := r.resolveOrgLabels(ctx, linkValues)
		out := make(map[string]string, len(links))
		for _, link := range links {
			if label := orgLabels[link.Link]; label != "" {
				out[link.RowID] = label
			}
		}
		return out
	}
	selects := make([]string, 0, len(src.nameColumns)+1)
	selects = append(selects, src.idColumn+" AS row_id")
	for i, col := range src.nameColumns {
		selects = append(selects, fmt.Sprintf("%s AS name_%d", col, i))
	}
	var scanned []map[string]any
	if err := r.db.DB(ctx).Table(src.table).
		Select(selects).
		Where(src.idColumn+" IN ?", ids).
		Find(&scanned).Error; err != nil {
		return nil
	}
	out := make(map[string]string, len(scanned))
	for _, row := range scanned {
		id, _ := row["row_id"].(string)
		if id == "" {
			continue
		}
		for i := range src.nameColumns {
			name, ok := row[fmt.Sprintf("name_%d", i)].(string)
			if ok && name != "" {
				out[id] = name
				break
			}
		}
	}
	return out
}

// labelKey encodes a (resource type, id) pair as one map key.
func labelKey(resourceType, id string) string { return resourceType + "\x00" + id }

// decorateEvents summarizes stored rows and fills the display-only
// labels: actor, organization and resource names. The console then never
// renders a raw id where a human-readable label exists, and the labels
// are resolved here (not per row) so a page costs a fixed number of
// queries.
func (s *Service) decorateEvents(ctx context.Context, repo *Repository, rows []*AuditEvent) []*auditv1.AuditEvent {
	labels := s.actorLabels(ctx, rows)
	orgNames := repo.resolveOrgLabels(ctx, orgIDsOf(rows))
	resourceNames := repo.resolveResourceLabels(ctx, rows)
	events := make([]*auditv1.AuditEvent, 0, len(rows))
	for _, row := range rows {
		ev := summarizeEvent(row)
		ev.ActorDisplayName = labels[row.ActorUserID]
		ev.OrganizationDisplayName = orgNames[row.OrganizationID]
		ev.ResourceDisplayName = resourceNames[labelKey(row.ResourceType, row.ResourceID)]
		events = append(events, ev)
	}
	return events
}

// orgIDsOf collects the distinct non-empty organization ids of rows.
func orgIDsOf(rows []*AuditEvent) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.OrganizationID != "" {
			ids = append(ids, row.OrganizationID)
		}
	}
	return ids
}

// uniqueNonEmpty drops blanks and duplicates while preserving order.
func uniqueNonEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
