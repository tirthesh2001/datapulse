package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"datapulse/internal/cache"
	"datapulse/internal/config"
	"datapulse/internal/middleware"
	"datapulse/internal/parser"
)

/* ── Dynamic Table Rendering ─────────────────────────── */

type SectionRender struct {
	Label     string
	Source    string // parser section key (for drill-down)
	Headers   []string
	Rows      [][]string
	DrillKeys []string // parallel to Rows; non-empty means row can drill down
	IsSub     bool
	ColCount  int
	SpanWidth int // parent's column count, for sub-section colspan
}

type WidgetRender struct {
	ID       string
	Label    string
	Sections []SectionRender
}

func (h *Handler) Tables(w http.ResponseWriter, r *http.Request) {
	var info middleware.AuthInfo
	if h.cfg.AuthEnabled() {
		var ok bool
		info, ok = h.requireProtected(w, r)
		if !ok {
			return
		}
	}

	reports, err := h.parseSelectedReports(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	datesParam := r.FormValue("dates")
	cfg := h.widgetConfigFromAuth(info)
	cfgHash := widgetConfigHash(cfg)

	var cacheKey string
	if h.respCache != nil && datesParam != "" {
		cacheKey = cache.Key("tables", datesParam, cfgHash)
		if body, hit := h.respCache.Get(cacheKey); hit {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(body)
			return
		}
	}

	allDates := h.store.AvailableDates()

	var widgets []WidgetRender
	for _, widget := range cfg.Widgets {
		if !widget.Visible {
			continue
		}
		wr := WidgetRender{ID: widget.ID, Label: widget.Label}
		parentColCount := 0
		for si, sec := range widget.Sections {
			sr := h.renderSection(sec, reports, allDates, si > 0)
			if si == 0 {
				parentColCount = sr.ColCount
			}
			if si > 0 {
				sr.SpanWidth = parentColCount
			}
			wr.Sections = append(wr.Sections, sr)
		}
		widgets = append(widgets, wr)
	}

	data := map[string]interface{}{
		"Widgets": widgets,
	}
	var buf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&buf, "tables.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if cacheKey != "" && h.respCache != nil {
		h.respCache.Set(cacheKey, buf.Bytes())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) renderSection(sec config.WidgetSection, reports []*parser.DayReport, allDates []string, isSub bool) SectionRender {
	sr := SectionRender{Label: sec.Label, IsSub: isSub}

	var headers []string
	for _, col := range sec.Columns {
		headers = append(headers, col.Label)
	}
	sr.Headers = headers
	sr.ColCount = len(headers)

	sr.Source = sec.Source

	if sec.Aggregate {
		sr.Rows = h.renderAggregated(sec, reports)
		sr.DrillKeys = make([]string, len(sr.Rows))
		return sr
	}

	if sec.Pivot != nil {
		rows, pivotHeaders := h.renderPivoted(sec, reports, allDates)
		sr.Rows = rows
		sr.Headers = pivotHeaders
		sr.ColCount = len(pivotHeaders)
		sr.DrillKeys = make([]string, len(sr.Rows))
		return sr
	}

	sr.Rows, sr.DrillKeys = h.renderFlat(sec, reports, allDates)
	return sr
}

func (h *Handler) renderFlat(sec config.WidgetSection, reports []*parser.DayReport, allDates []string) ([][]string, []string) {
	var rows [][]string
	var keys []string
	for i, rpt := range reports {
		sectionData := rpt.Sections[sec.Source]
		if sectionData == nil {
			continue
		}
		dataRows := sectionData.Rows
		if len(dataRows) == 0 {
			continue
		}

		multiRow := len(dataRows) > 1

		for ri, dataRow := range dataRows {
			var prevRow []string
			if !multiRow {
				if i == 0 {
					prevRpt := findPreviousReport(rpt.Date.Format("2006-01-02"), allDates, h.store)
					if prevRpt != nil {
						if ps := prevRpt.Sections[sec.Source]; ps != nil && len(ps.Rows) > 0 {
							prevRow = ps.Rows[0]
						}
					}
				} else {
					prevRpt := reports[i-1]
					if ps := prevRpt.Sections[sec.Source]; ps != nil && len(ps.Rows) > 0 {
						prevRow = ps.Rows[0]
					}
				}
			}

			row := h.buildRowFromColumns(sec, sectionData, dataRow, prevRow, rpt, ri)
			rows = append(rows, row)
			keys = append(keys, rowKey(dataRow, sectionData.Headers))
		}
	}
	return rows, keys
}

func (h *Handler) renderPivoted(sec config.WidgetSection, reports []*parser.DayReport, allDates []string) ([][]string, []string) {
	pivotLabelIdx := -1
	pivotValueIdx := -1

	var sampleHeaders []string
	for _, rpt := range reports {
		if sd := rpt.Sections[sec.Source]; sd != nil {
			sampleHeaders = sd.Headers
			break
		}
	}
	for i, hdr := range sampleHeaders {
		normHdr := strings.ToUpper(strings.TrimSpace(hdr))
		if normHdr == strings.ToUpper(sec.Pivot.LabelColumn) {
			pivotLabelIdx = i
		}
		if normHdr == strings.ToUpper(sec.Pivot.ValueColumn) {
			pivotValueIdx = i
		}
	}

	var pivotLabels []string
	seenLabels := make(map[string]bool)
	for _, rpt := range reports {
		sd := rpt.Sections[sec.Source]
		if sd == nil {
			continue
		}
		for _, row := range sd.Rows {
			if pivotLabelIdx >= 0 && pivotLabelIdx < len(row) {
				lbl := strings.TrimSpace(row[pivotLabelIdx])
				if !seenLabels[lbl] {
					seenLabels[lbl] = true
					pivotLabels = append(pivotLabels, lbl)
				}
			}
		}
	}

	headers := []string{"Date"}
	for _, lbl := range pivotLabels {
		headers = append(headers, lbl)
	}

	var resultRows [][]string
	for _, rpt := range reports {
		sd := rpt.Sections[sec.Source]
		rowValues := make(map[string]int)
		if sd != nil {
			for _, row := range sd.Rows {
				if pivotLabelIdx >= 0 && pivotLabelIdx < len(row) && pivotValueIdx >= 0 && pivotValueIdx < len(row) {
					lbl := strings.TrimSpace(row[pivotLabelIdx])
					rowValues[lbl] = parser.ParseInt(row[pivotValueIdx])
				}
			}
		}

		resultRow := []string{rpt.Date.Format("2 Jan, 2006")}
		for _, lbl := range pivotLabels {
			val := rowValues[lbl]
			resultRow = append(resultRow, formatK(val))
		}
		resultRows = append(resultRows, resultRow)
	}

	return resultRows, headers
}

type aggEntry struct {
	numValues  map[string]int
	textValues map[string]string
	key        string
}

func (h *Handler) renderAggregated(sec config.WidgetSection, reports []*parser.DayReport) [][]string {
	totals := make(map[string]*aggEntry)
	var order []string

	var sampleHeaders []string
	for _, rpt := range reports {
		if sd := rpt.Sections[sec.Source]; sd != nil {
			sampleHeaders = sd.Headers
			break
		}
	}

	for _, rpt := range reports {
		sd := rpt.Sections[sec.Source]
		if sd == nil {
			continue
		}
		for _, row := range sd.Rows {
			key := rowKey(row, sampleHeaders)
			if existing, ok := totals[key]; ok {
				for i, hdr := range sampleHeaders {
					if i < len(row) {
						existing.numValues[hdr] += parser.ParseInt(row[i])
					}
				}
			} else {
				entry := &aggEntry{
					numValues:  make(map[string]int),
					textValues: make(map[string]string),
					key:        rowKey(row, sampleHeaders),
				}
				for i, hdr := range sampleHeaders {
					if i < len(row) {
						entry.numValues[hdr] = parser.ParseInt(row[i])
						entry.textValues[hdr] = strings.TrimSpace(row[i])
					}
				}
				totals[key] = entry
				order = append(order, key)
			}
		}
	}

	numericKeys := make(map[string]bool)
	for _, col := range sec.Columns {
		if col.Format == "K" {
			numericKeys[strings.ToUpper(strings.TrimSpace(col.Key))] = true
		}
	}

	type sortEntry struct {
		key   string
		total int
		entry *aggEntry
	}
	var sorted []sortEntry
	for _, k := range order {
		e := totals[k]
		t := 0
		for _, hdr := range sampleHeaders {
			normHdr := strings.ToUpper(strings.TrimSpace(hdr))
			if numericKeys[normHdr] {
				t += e.numValues[hdr]
			}
		}
		sorted = append(sorted, sortEntry{key: k, total: t, entry: e})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].total > sorted[j].total })

	limit := len(sorted)
	if sec.Limit > 0 && sec.Limit < limit {
		limit = sec.Limit
	}

	var rows [][]string
	for rank, se := range sorted[:limit] {
		var row []string
		for _, col := range sec.Columns {
			switch col.Key {
			case "_rank":
				row = append(row, fmt.Sprintf("%d", rank+1))
			default:
				if col.Format == "K" || col.Type == "pct" {
					val := resolveAggNumColumn(col.Key, se.entry, sampleHeaders)
					row = append(row, formatK(val))
				} else {
					txt := resolveAggTextColumn(col.Key, se.entry, sampleHeaders)
					if txt == "" {
						txt = se.key
					}
					row = append(row, txt)
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func resolveAggNumColumn(key string, entry *aggEntry, headers []string) int {
	normKey := strings.ToUpper(strings.TrimSpace(key))
	for _, hdr := range headers {
		if strings.ToUpper(strings.TrimSpace(hdr)) == normKey {
			return entry.numValues[hdr]
		}
	}
	return 0
}

func resolveAggTextColumn(key string, entry *aggEntry, headers []string) string {
	normKey := strings.ToUpper(strings.TrimSpace(key))
	for _, hdr := range headers {
		if strings.ToUpper(strings.TrimSpace(hdr)) == normKey {
			return entry.textValues[hdr]
		}
	}
	return ""
}

func rowKey(row []string, headers []string) string {
	for i, hdr := range headers {
		normHdr := strings.ToUpper(strings.TrimSpace(hdr))
		if strings.Contains(normHdr, "MESSAGE") || strings.Contains(normHdr, "REASON") ||
			strings.Contains(normHdr, "CODE") || strings.Contains(normHdr, "NAME") ||
			strings.Contains(normHdr, "TYPE") || strings.Contains(normHdr, "MODE") ||
			strings.Contains(normHdr, "CATEGORY") || strings.Contains(normHdr, "PRODUCT") ||
			strings.Contains(normHdr, "STATUS") {
			if i < len(row) {
				return strings.TrimSpace(row[i])
			}
		}
	}
	if len(row) > 0 {
		return strings.TrimSpace(row[0])
	}
	return ""
}

func (h *Handler) buildRowFromColumns(sec config.WidgetSection, sd *parser.SectionData, dataRow, prevRow []string, rpt *parser.DayReport, _ int) []string {
	var row []string
	for _, col := range sec.Columns {
		switch {
		case col.Key == "_date":
			row = append(row, rpt.Date.Format("2 Jan, 2006"))
		case col.Type == "pct":
			baseKey := strings.TrimSuffix(col.Key, "_pct")
			curVal := lookupVal(baseKey, sd.Headers, dataRow)
			if prevRow != nil {
				prevVal := lookupVal(baseKey, sd.Headers, prevRow)
				pct := pctChange(prevVal, curVal)
				up := curVal >= prevVal
				row = append(row, fmtPctCell(pct, up))
			} else {
				row = append(row, "—")
			}
		default:
			strVal := lookupStr(col.Key, sd.Headers, dataRow)
			if col.Format == "K" {
				val := parser.ParseInt(strVal)
				if val == 0 && strVal != "" && strVal != "0" {
					row = append(row, strVal)
				} else {
					row = append(row, formatK(val))
				}
			} else {
				row = append(row, strVal)
			}
		}
	}
	return row
}

func lookupVal(key string, headers []string, row []string) int {
	normKey := strings.ToUpper(strings.TrimSpace(key))
	for i, hdr := range headers {
		if strings.ToUpper(strings.TrimSpace(hdr)) == normKey {
			if i < len(row) {
				return parser.ParseInt(row[i])
			}
		}
	}
	return 0
}

func lookupStr(key string, headers []string, row []string) string {
	normKey := strings.ToUpper(strings.TrimSpace(key))
	for i, hdr := range headers {
		if strings.ToUpper(strings.TrimSpace(hdr)) == normKey {
			if i < len(row) {
				return strings.TrimSpace(row[i])
			}
		}
	}
	return ""
}
