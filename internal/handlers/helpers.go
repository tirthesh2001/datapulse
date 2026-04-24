package handlers

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"datapulse/internal/parser"
)

/* ── Shared Helpers ────────────────────────────────────── */

func (h *Handler) parseSelectedReports(r *http.Request) ([]*parser.DayReport, error) {
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("bad request")
	}
	datesParam := r.FormValue("dates")
	if datesParam == "" {
		return nil, fmt.Errorf("no dates selected")
	}
	selectedDates := strings.Split(datesParam, ",")
	reports := h.store.GetReports(selectedDates)
	if len(reports) == 0 {
		return nil, fmt.Errorf("no data for selected dates")
	}
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Date.Before(reports[j].Date)
	})
	return reports, nil
}

func findPreviousReport(dateKey string, allDates []string, store *parser.Store) *parser.DayReport {
	idx := -1
	for i, d := range allDates {
		if d == dateKey {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return nil
	}
	reports := store.GetReports([]string{allDates[idx-1]})
	if len(reports) > 0 {
		return reports[0]
	}
	return nil
}

func pctChange(old, new_ int) float64 {
	if old == 0 {
		return 0
	}
	return (float64(new_-old) / float64(old)) * 100
}

func fmtPctCell(pct float64, up bool) string {
	if pct == 0 {
		return "—"
	}
	arrow := "&#9660;"
	cls := "dn"
	if up {
		arrow = "&#9650;"
		cls = "up"
	}
	return fmt.Sprintf(`<span class="%s">%s %.2f%%</span>`, cls, arrow, math.Abs(pct))
}

func formatK(n int) string {
	if n >= 1000 {
		val := float64(n) / 1000.0
		s := fmt.Sprintf("%.1f", val)
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
		return s + "K"
	}
	return fmt.Sprintf("%d", n)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
