package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"time"
)

// Dashboard renders the main HTML shell.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	dates := h.store.AvailableDates()
	boot := map[string]interface{}{
		"supabaseURL":    h.cfg.SupabaseURL,
		"supabaseAnon":   h.cfg.SupabaseAnonKey,
		"authEnabled":    h.cfg.AuthEnabled(),
		"reportCount":    len(dates),
		"serverTime":     time.Now().UTC().Format(time.RFC3339),
	}
	bootJSON, _ := json.Marshal(boot)
	datesJSON, _ := json.Marshal(dates)
	data := map[string]interface{}{
		"AvailableDatesJSON": template.JS(datesJSON),
		"BootJSON":           template.JS(bootJSON),
	}
	if err := h.tmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// Refresh reloads report data from disk or Supabase. Optional JSON body:
// {"partial":true,"dates":["2026-01-01"]} merges only those dates (Supabase).
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var partial struct {
		Partial bool     `json:"partial"`
		Dates   []string `json:"dates"`
	}
	if err := json.NewDecoder(r.Body).Decode(&partial); err != nil && err != io.EOF {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	var err error
	if partial.Partial && len(partial.Dates) > 0 && h.cfg.UseSupabase() {
		err = h.store.MergeFromBackendDates(partial.Dates)
	} else {
		err = h.store.Refresh()
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("refresh failed: %v", err), http.StatusInternalServerError)
		return
	}
	h.invalidateCaches()
	dates := h.store.AvailableDates()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"dates": dates,
		"at":    time.Now().UTC().Format(time.RFC3339),
	})
}

// HealthBootstrap returns JSON for client-side status (optional GET).
func (h *Handler) HealthBootstrap(w http.ResponseWriter, r *http.Request) {
	dates := h.store.AvailableDates()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":          true,
		"reportCount": len(dates),
		"authEnabled": h.cfg.AuthEnabled(),
		"supabase":    h.cfg.UseSupabase(),
	})
}
