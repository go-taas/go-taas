package audit

import (
	"encoding/csv"
	"encoding/json"
	"strings"
)

// exportRow is the flat projection of an audit event used by the CSV
// serializer. Column order is the CSV contract.
type exportRow struct {
	AuditEventID   string
	OrganizationID string
	ActorUserID    string
	ActorType      string
	Action         string
	ResourceType   string
	ResourceID     string
	Result         string
	IPAddress      string
	UserAgent      string
	Metadata       string
	CreatedAt      string
}

// toExportRow projects an audit event to the flat export row.
func toExportRow(ev *AuditEvent) exportRow {
	return exportRow{
		AuditEventID:   ev.ID,
		OrganizationID: ev.OrganizationID,
		ActorUserID:    ev.ActorUserID,
		ActorType:      ev.ActorType,
		Action:         ev.Action,
		ResourceType:   ev.ResourceType,
		ResourceID:     ev.ResourceID,
		Result:         ev.Result,
		IPAddress:      ev.IPAddress,
		UserAgent:      ev.UserAgent,
		Metadata:       ev.Metadata,
		CreatedAt:      ev.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// renderCSV serializes the events as CSV with a header row (AD7).
func renderCSV(events []*AuditEvent) (string, error) {
	var buf strings.Builder
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{
		"audit_event_id", "organization_id", "actor_user_id", "actor_type",
		"action", "resource_type", "resource_id", "result", "ip_address",
		"user_agent", "metadata", "created_at",
	}); err != nil {
		return "", err
	}
	for _, ev := range events {
		row := toExportRow(ev)
		if err := w.Write([]string{
			row.AuditEventID, row.OrganizationID, row.ActorUserID, row.ActorType,
			row.Action, row.ResourceType, row.ResourceID, row.Result, row.IPAddress,
			row.UserAgent, row.Metadata, row.CreatedAt,
		}); err != nil {
			return "", err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// renderJSON serializes the events as a JSON array of flat objects
// (AD7).
func renderJSON(events []*AuditEvent) (string, error) {
	rows := make([]exportRow, 0, len(events))
	for _, ev := range events {
		rows = append(rows, toExportRow(ev))
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// applyExportCap returns the first maxRows events and whether the set
// was truncated (AD7). A non-positive maxRows means no cap.
func applyExportCap(events []*AuditEvent, maxRows int) ([]*AuditEvent, bool) {
	if maxRows <= 0 || len(events) <= maxRows {
		return events, false
	}
	return events[:maxRows], true
}
