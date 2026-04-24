package handlers

import (
	"fmt"
	"html"
	"net/http"
	"strings"

	"datapulse/internal/parser"
)

// Drilldown returns HTML rows for a given section and label key across selected dates.
func (h *Handler) Drilldown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.cfg.AuthEnabled() {
		if _, ok := h.requireProtected(w, r); !ok {
			return
		}
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	section := strings.TrimSpace(r.FormValue("section"))
	key := strings.TrimSpace(r.FormValue("key"))
	datesParam := r.FormValue("dates")
	if section == "" || key == "" || datesParam == "" {
		http.Error(w, "missing section, key, or dates", http.StatusBadRequest)
		return
	}

	selectedDates := strings.Split(datesParam, ",")
	reports := h.store.GetReports(selectedDates)

	var b strings.Builder
	b.WriteString(`<div class="drilldown-wrap"><table class="drilldown-table"><thead><tr><th>Date</th><th>Detail</th><th>Count</th></tr></thead><tbody>`)

	for _, rpt := range reports {
		sd := rpt.Sections[section]
		if sd == nil {
			continue
		}
		for _, row := range sd.Rows {
			rkey := rowKey(row, sd.Headers)
			if rkey != key {
				continue
			}
			line := strings.Join(rowCells(sd, row), " · ")
			cnt := lookupVal("TOTAL", sd.Headers, row)
			if cnt == 0 {
				cnt = lookupVal("COUNT", sd.Headers, row)
			}
			b.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td>%s</td></tr>`,
				html.EscapeString(rpt.Date.Format("2 Jan, 2006")),
				html.EscapeString(line),
				html.EscapeString(formatK(cnt)),
			))
		}
	}

	b.WriteString(`</tbody></table></div>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func rowCells(sd *parser.SectionData, row []string) []string {
	var out []string
	for i, h := range sd.Headers {
		if i >= len(row) {
			break
		}
		out = append(out, h+": "+strings.TrimSpace(row[i]))
	}
	return out
}
