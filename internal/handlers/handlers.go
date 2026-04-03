package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"datapulse/internal/config"
	"datapulse/internal/parser"
	"datapulse/internal/storage"
)

type Handler struct {
	store    *parser.Store
	widgets  *config.WidgetStore
	tmpl     *template.Template
	supabase *storage.Supabase
}

func New(store *parser.Store, widgets *config.WidgetStore, tmpl *template.Template, sb *storage.Supabase) *Handler {
	return &Handler{store: store, widgets: widgets, tmpl: tmpl, supabase: sb}
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	dates := h.store.AvailableDates()
	data := map[string]interface{}{
		"AvailableDates": dates,
	}
	if err := h.tmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Refresh(); err != nil {
		http.Error(w, fmt.Sprintf("refresh failed: %v", err), 500)
		return
	}
	dates := h.store.AvailableDates()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"dates": dates,
	})
}

/* ── Config API ──────────────────────────────────────── */

func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := h.widgets.Get()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

func (h *Handler) PostConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.WidgetConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), 400)
		return
	}
	if err := h.widgets.Update(cfg); err != nil {
		http.Error(w, fmt.Sprintf("save failed: %v", err), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) GetSources(w http.ResponseWriter, r *http.Request) {
	sources := h.store.AvailableSources()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sources)
}

/* ── Upload Handler ──────────────────────────────────── */

func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		jsonError(w, "could not parse form: "+err.Error(), 400)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "missing file", 400)
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".eml") {
		jsonError(w, "only .eml files are accepted", 400)
		return
	}

	raw, err := io.ReadAll(file)
	if err != nil {
		jsonError(w, "reading file: "+err.Error(), 500)
		return
	}

	report, err := parser.ParseEMLBytes(raw)
	if err != nil {
		jsonError(w, "parsing email: "+err.Error(), 400)
		return
	}
	dateKey := report.Date.Format("2006-01-02")

	replace := r.FormValue("replace") == "true"

	if h.supabase != nil {
		exists, existingFile, err := h.supabase.CheckDateExists(dateKey)
		if err != nil {
			jsonError(w, "checking duplicates: "+err.Error(), 500)
			return
		}
		if exists && !replace {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"conflict":     true,
				"date":         dateKey,
				"existingFile": existingFile,
				"newFile":      header.Filename,
			})
			return
		}
		if exists {
			if err := h.supabase.UpsertEmail(header.Filename, dateKey, raw); err != nil {
				jsonError(w, "replacing email: "+err.Error(), 500)
				return
			}
		} else {
			if err := h.supabase.SaveEmail(header.Filename, dateKey, raw); err != nil {
				jsonError(w, "saving email: "+err.Error(), 500)
				return
			}
		}
	} else {
		emailDir := h.store.EmailDir()
		existingPath := ""
		entries, _ := os.ReadDir(emailDir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			p := filepath.Join(emailDir, e.Name())
			rpt, err := parser.ParseEML(p)
			if err != nil {
				continue
			}
			if rpt.Date.Format("2006-01-02") == dateKey {
				existingPath = p
				break
			}
		}

		if existingPath != "" && !replace {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"conflict":     true,
				"date":         dateKey,
				"existingFile": filepath.Base(existingPath),
				"newFile":      header.Filename,
			})
			return
		}

		if existingPath != "" {
			os.Remove(existingPath)
		}

		dst := filepath.Join(emailDir, header.Filename)
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			jsonError(w, "writing file: "+err.Error(), 500)
			return
		}
	}

	h.store.AddReport(raw)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"date":   dateKey,
		"file":   header.Filename,
		"dates":  h.store.AvailableDates(),
	})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Seed bulk-uploads all .eml files from a local directory into Supabase.
// Only works when Supabase is configured.
func (h *Handler) Seed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if h.supabase == nil {
		jsonError(w, "seeding requires Supabase mode", 400)
		return
	}

	dir := r.FormValue("dir")
	if dir == "" {
		dir = "UPI Email Reports"
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		jsonError(w, "reading directory: "+err.Error(), 500)
		return
	}

	var uploaded, skipped, failed int
	var errors []string

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".eml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: read error: %v", e.Name(), err))
			failed++
			continue
		}

		report, err := parser.ParseEMLBytes(raw)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: parse error: %v", e.Name(), err))
			failed++
			continue
		}
		dateKey := report.Date.Format("2006-01-02")

		exists, _, err := h.supabase.CheckDateExists(dateKey)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: check error: %v", e.Name(), err))
			failed++
			continue
		}
		if exists {
			skipped++
			continue
		}

		if err := h.supabase.SaveEmail(e.Name(), dateKey, raw); err != nil {
			errors = append(errors, fmt.Sprintf("%s: save error: %v", e.Name(), err))
			failed++
			continue
		}
		h.store.AddReport(raw)
		uploaded++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"uploaded": uploaded,
		"skipped":  skipped,
		"failed":   failed,
		"errors":   errors,
		"dates":    h.store.AvailableDates(),
	})
}

/* ── Dynamic Table Rendering ─────────────────────────── */

type SectionRender struct {
	Label     string
	Headers   []string
	Rows      [][]string
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
	reports, err := h.parseSelectedReports(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	cfg := h.widgets.Get()
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
	w.Header().Set("Content-Type", "text/html")
	if err := h.tmpl.ExecuteTemplate(w, "tables.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (h *Handler) renderSection(sec config.WidgetSection, reports []*parser.DayReport, allDates []string, isSub bool) SectionRender {
	sr := SectionRender{Label: sec.Label, IsSub: isSub}

	var headers []string
	for _, col := range sec.Columns {
		headers = append(headers, col.Label)
	}
	sr.Headers = headers
	sr.ColCount = len(headers)

	if sec.Aggregate {
		sr.Rows = h.renderAggregated(sec, reports)
		return sr
	}

	if sec.Pivot != nil {
		rows, pivotHeaders := h.renderPivoted(sec, reports, allDates)
		sr.Rows = rows
		sr.Headers = pivotHeaders
		sr.ColCount = len(pivotHeaders)
		return sr
	}

	sr.Rows = h.renderFlat(sec, reports, allDates)
	return sr
}

func (h *Handler) renderFlat(sec config.WidgetSection, reports []*parser.DayReport, allDates []string) [][]string {
	var rows [][]string
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
		}
	}
	return rows
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

func extractPivotValues(rpt *parser.DayReport, source string, labelIdx, valueIdx int) map[string]int {
	vals := make(map[string]int)
	sd := rpt.Sections[source]
	if sd == nil {
		return vals
	}
	for _, row := range sd.Rows {
		if labelIdx >= 0 && labelIdx < len(row) && valueIdx >= 0 && valueIdx < len(row) {
			lbl := strings.TrimSpace(row[labelIdx])
			vals[lbl] = parser.ParseInt(row[valueIdx])
		}
	}
	return vals
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

/* ── Insights Handler ──────────────────────────────────── */

type InsightItem struct {
	Title string
	Body  string
}

func (h *Handler) Insights(w http.ResponseWriter, r *http.Request) {
	reports, err := h.parseSelectedReports(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	items := generateInsights(reports)

	data := map[string]interface{}{
		"Items": items,
	}
	w.Header().Set("Content-Type", "text/html")
	if err := h.tmpl.ExecuteTemplate(w, "insights.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func generateInsights(reports []*parser.DayReport) []InsightItem {
	var items []InsightItem
	n := len(reports)

	totalSuccess, totalFailure, totalTxn := 0, 0, 0
	for _, r := range reports {
		sd := r.Sections["UPI TRANSACTION DETAILS"]
		if sd == nil || len(sd.Rows) == 0 {
			continue
		}
		row := sd.Rows[0]
		s := lookupVal("SUCCESS", sd.Headers, row)
		f := lookupVal("FAILURE", sd.Headers, row)
		t := lookupVal("TOTAL_TRANSACTIONS", sd.Headers, row)
		totalSuccess += s
		totalFailure += f
		totalTxn += t
	}

	if totalTxn > 0 {
		successRate := float64(totalSuccess) / float64(totalTxn) * 100
		failureRate := float64(totalFailure) / float64(totalTxn) * 100
		items = append(items, InsightItem{
			Title: "Transaction Success Rate",
			Body: fmt.Sprintf("Across %d day(s), the overall success rate is %.1f%% with a failure rate of %.1f%%. Total volume: %s transactions.",
				n, successRate, failureRate, formatK(totalTxn)),
		})
	}

	if n >= 2 {
		first := reports[0]
		last := reports[n-1]
		firstTxn := getSectionVal(first, "UPI TRANSACTION DETAILS", "TOTAL_TRANSACTIONS")
		lastTxn := getSectionVal(last, "UPI TRANSACTION DETAILS", "TOTAL_TRANSACTIONS")
		if firstTxn > 0 {
			change := float64(lastTxn-firstTxn) / float64(firstTxn) * 100
			direction := "increased"
			if change < 0 {
				direction = "decreased"
				change = -change
			}
			items = append(items, InsightItem{
				Title: "Volume Trend",
				Body: fmt.Sprintf("Transaction volume %s by %.1f%% from %s (%s) to %s (%s).",
					direction, change,
					first.Date.Format("2 Jan"), formatK(firstTxn),
					last.Date.Format("2 Jan"), formatK(lastTxn)),
			})
		}
	}

	// Top failure insight
	type failAgg struct {
		msg   string
		total int
	}
	failTotals := make(map[string]*failAgg)
	for _, r := range reports {
		sd := r.Sections["TOP 25 UPI TRANSACTION FAILURES"]
		if sd == nil {
			continue
		}
		for _, row := range sd.Rows {
			msg := lookupStr("ERROR_MESSAGE", sd.Headers, row)
			if msg == "" && len(row) > 1 {
				msg = strings.TrimSpace(row[1])
			}
			total := lookupVal("TOTAL", sd.Headers, row)
			if existing, ok := failTotals[msg]; ok {
				existing.total += total
			} else {
				failTotals[msg] = &failAgg{msg: msg, total: total}
			}
		}
	}
	var topFail *failAgg
	for _, fa := range failTotals {
		if topFail == nil || fa.total > topFail.total {
			topFail = fa
		}
	}
	if topFail != nil {
		pct := float64(0)
		if totalFailure > 0 {
			pct = float64(topFail.total) / float64(totalFailure) * 100
		}
		items = append(items, InsightItem{
			Title: "Top Failure Reason",
			Body: fmt.Sprintf("\"%s\" accounts for %s failures (%.1f%% of all failures).",
				truncate(topFail.msg, 80), formatK(topFail.total), pct),
		})
	}

	// Binding insight
	peakBinding, peakDate := 0, ""
	totalBound, totalPending := 0, 0
	for _, r := range reports {
		sd := r.Sections["DEVICE BINDING COUNT"]
		if sd == nil || len(sd.Rows) == 0 {
			continue
		}
		bound := lookupVal("DEVICE BINDED USER", sd.Headers, sd.Rows[0])
		pending := lookupVal("LOGGED IN BUT NOT DEVICE BINDED USER", sd.Headers, sd.Rows[0])
		totalBound += bound
		totalPending += pending
		if bound > peakBinding {
			peakBinding = bound
			peakDate = r.Date.Format("2 Jan")
		}
	}
	if peakBinding > 0 {
		items = append(items, InsightItem{
			Title: "Device Binding",
			Body: fmt.Sprintf("Peak binding of %s occurred on %s. Across the period: %s successful bindings, %s pending.",
				formatK(peakBinding), peakDate, formatK(totalBound), formatK(totalPending)),
		})
	}

	// Journey distribution
	totalP2P, totalP2M := 0, 0
	for _, r := range reports {
		sd := r.Sections["JOURNEY WISE SUCCESS AND FAILURE COUNT"]
		if sd == nil {
			continue
		}
		for _, row := range sd.Rows {
			label := ""
			if len(row) > 0 {
				label = strings.ToUpper(strings.TrimSpace(row[0]))
			}
			total := lookupVal("TOTAL_TRANSACTIONS", sd.Headers, row)
			switch {
			case strings.Contains(label, "P2M") && !strings.Contains(label, "HERO"):
				totalP2M += total
			case strings.Contains(label, "P2P"):
				totalP2P += total
			}
		}
	}
	if totalP2P+totalP2M > 0 {
		p2pPct := float64(totalP2P) / float64(totalP2P+totalP2M) * 100
		items = append(items, InsightItem{
			Title: "Journey Distribution",
			Body: fmt.Sprintf("P2P transactions make up %.1f%% and P2M makes up %.1f%% of the combined P2P+P2M volume (%s total).",
				p2pPct, 100-p2pPct, formatK(totalP2P+totalP2M)),
		})
	}

	return items
}

func getSectionVal(rpt *parser.DayReport, section, col string) int {
	sd := rpt.Sections[section]
	if sd == nil || len(sd.Rows) == 0 {
		return 0
	}
	return lookupVal(col, sd.Headers, sd.Rows[0])
}

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
