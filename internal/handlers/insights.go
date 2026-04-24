package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"datapulse/internal/cache"
	"datapulse/internal/middleware"

	"datapulse/internal/parser"
)

// InsightItem is one bullet in the insights panel.
type InsightItem struct {
	Title string
	Body  string
}

// Insights renders HTML insights for POSTed dates. Query/form: template=executive|risk|growth
func (h *Handler) Insights(w http.ResponseWriter, r *http.Request) {
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

	tmplName := strings.ToLower(strings.TrimSpace(r.FormValue("template")))
	if tmplName == "" {
		tmplName = "executive"
	}

	datesParam := r.FormValue("dates")
	cfgHash := widgetConfigHash(h.widgetConfigFromAuth(info))
	var cacheKey string
	if h.respCache != nil && datesParam != "" {
		cacheKey = cache.Key("insights", datesParam, cfgHash, tmplName)
		if body, hit := h.respCache.Get(cacheKey); hit {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(body)
			return
		}
	}

	items := generateInsights(reports, tmplName)
	data := map[string]interface{}{
		"Items": items,
	}
	var buf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&buf, "insights.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if cacheKey != "" && h.respCache != nil {
		h.respCache.Set(cacheKey, buf.Bytes())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func generateInsights(reports []*parser.DayReport, templateName string) []InsightItem {
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

	return orderInsightTemplate(items, templateName)
}

func orderInsightTemplate(items []InsightItem, templateName string) []InsightItem {
	var order []string
	switch templateName {
	case "risk":
		order = []string{
			"Top Failure Reason", "Transaction Success Rate", "Volume Trend",
			"Device Binding", "Journey Distribution",
		}
	case "growth":
		order = []string{
			"Volume Trend", "Transaction Success Rate", "Journey Distribution",
			"Top Failure Reason", "Device Binding",
		}
	default:
		return items
	}
	prio := make(map[string]int)
	for i, t := range order {
		prio[t] = i
	}
	sort.SliceStable(items, func(i, j int) bool {
		pi, okI := prio[items[i].Title]
		pj, okJ := prio[items[j].Title]
		if !okI {
			pi = 99
		}
		if !okJ {
			pj = 99
		}
		return pi < pj
	})
	return items
}

func getSectionVal(rpt *parser.DayReport, section, col string) int {
	sd := rpt.Sections[section]
	if sd == nil || len(sd.Rows) == 0 {
		return 0
	}
	return lookupVal(col, sd.Headers, sd.Rows[0])
}
