package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"

	"datapulse/internal/cache"
	"datapulse/internal/config"
	"datapulse/internal/middleware"
	"datapulse/internal/parser"
	"datapulse/internal/storage"
)

// Handler serves HTTP routes for DataPulse.
type Handler struct {
	store     *parser.Store
	widgets   *config.WidgetStore
	tmpl      *template.Template
	supabase  *storage.Supabase
	cfg       *config.Config
	respCache *cache.Response
}

// New constructs the handler bundle.
func New(store *parser.Store, widgets *config.WidgetStore, tmpl *template.Template, sb *storage.Supabase, cfg *config.Config, respCache *cache.Response) *Handler {
	if cfg == nil {
		cfg = config.Load()
	}
	return &Handler{store: store, widgets: widgets, tmpl: tmpl, supabase: sb, cfg: cfg, respCache: respCache}
}

func (h *Handler) authDisabled() bool {
	return !h.cfg.AuthEnabled()
}

func (h *Handler) requireProtected(w http.ResponseWriter, r *http.Request) (middleware.AuthInfo, bool) {
	return middleware.CheckProtected(w, r, h.authDisabled(), h.cfg.DatapulseAPIKey, h.cfg.JWTSecret)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (h *Handler) invalidateCaches() {
	if h.respCache != nil {
		h.respCache.InvalidateAll()
	}
}

// widgetConfigFromAuth returns the dashboard layout for the authenticated principal.
func (h *Handler) widgetConfigFromAuth(info middleware.AuthInfo) config.WidgetConfig {
	if !h.cfg.AuthEnabled() {
		return h.widgets.Get()
	}
	if info.Kind == middleware.AuthJWT && info.UserID != "" && h.supabase != nil {
		raw, err := h.supabase.LoadUserWidgetConfig(info.UserID)
		if err != nil || raw == nil || len(bytes.TrimSpace(raw)) == 0 {
			return config.DefaultWidgetConfig()
		}
		var wc config.WidgetConfig
		if err := json.Unmarshal(raw, &wc); err != nil || len(wc.Widgets) == 0 {
			return config.DefaultWidgetConfig()
		}
		return wc
	}
	return h.widgets.Get()
}

func widgetConfigHash(wc config.WidgetConfig) string {
	b, _ := json.Marshal(wc)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
