package handlers

import (
	"encoding/json"
	"net/http"
)

// GetSources returns available data sources and column headers.
func (h *Handler) GetSources(w http.ResponseWriter, r *http.Request) {
	if h.cfg.AuthEnabled() {
		if _, ok := h.requireProtected(w, r); !ok {
			return
		}
	}
	sources := h.store.AvailableSources()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sources)
}

// UploadList returns email upload metadata (Supabase only).
func (h *Handler) UploadList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.requireProtected(w, r); !ok {
		return
	}
	if h.supabase == nil {
		jsonError(w, "upload history requires Supabase mode", http.StatusBadRequest)
		return
	}
	list, err := h.supabase.ListEmailUploads()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}
