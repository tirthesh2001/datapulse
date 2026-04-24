package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"datapulse/internal/config"
	"datapulse/internal/middleware"
)

// GetConfig returns widget JSON for the authenticated principal.
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.cfg.AuthEnabled() {
		info, ok := h.requireProtected(w, r)
		if !ok {
			return
		}
		if info.Kind == middleware.AuthJWT && info.UserID != "" && h.supabase != nil {
			raw, err := h.supabase.LoadUserWidgetConfig(info.UserID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if raw == nil || len(bytes.TrimSpace(raw)) == 0 {
				cfg := config.DefaultWidgetConfig()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(cfg)
				return
			}
			var cfg config.WidgetConfig
			if err := json.Unmarshal(raw, &cfg); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cfg)
			return
		}
	}

	cfg := h.widgets.Get()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

// PostConfig saves widget JSON for JWT users (per-user) or global when using API key.
func (h *Handler) PostConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info, ok := h.requireProtected(w, r)
	if !ok {
		return
	}

	var cfg config.WidgetConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if err := config.ValidateWidgetConfig(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if info.Kind == middleware.AuthJWT && info.UserID != "" && h.supabase != nil {
		data, err := json.Marshal(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := h.supabase.SaveUserWidgetConfig(info.UserID, data); err != nil {
			http.Error(w, fmt.Sprintf("save failed: %v", err), http.StatusInternalServerError)
			return
		}
		h.invalidateCaches()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	if err := h.widgets.Update(cfg); err != nil {
		http.Error(w, fmt.Sprintf("save failed: %v", err), http.StatusInternalServerError)
		return
	}
	h.invalidateCaches()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
