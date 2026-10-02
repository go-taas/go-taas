package account

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
)

// renderJSON renders the export as a structured JSON bundle (AD5).
func renderJSON(exportType string, rows []map[string]any) (string, int64, error) {
	bundle := map[string]any{exportType: rows}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", 0, err
	}
	return string(raw), int64(len(rows)), nil
}

// renderCSV renders the export as a UTF-8-BOM, quoted-field CSV with a
// header row (AD5). The rows are maps; the header is the union of keys
// in first-seen order.
func renderCSV(rows []map[string]any) (string, int64, error) {
	if len(rows) == 0 {
		return "\ufeff", 0, nil
	}
	// Build the header from the first row's keys.
	header := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		header = append(header, k)
	}
	var sb strings.Builder
	sb.WriteString("\ufeff") // UTF-8 BOM
	w := csv.NewWriter(&sb)
	if err := w.Write(header); err != nil {
		return "", 0, err
	}
	for _, row := range rows {
		record := make([]string, len(header))
		for i, h := range header {
			record[i] = fmt.Sprintf("%v", row[h])
		}
		if err := w.Write(record); err != nil {
			return "", 0, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", 0, err
	}
	return sb.String(), int64(len(rows)), nil
}