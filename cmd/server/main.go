package main

import (
	"context"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"datapulse"
	"datapulse/internal/cache"
	"datapulse/internal/config"
	"datapulse/internal/handlers"
	"datapulse/internal/parser"
	"datapulse/internal/storage"

	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: .env not loaded: %v", err)
	}
	cfg := config.Load()

	if cfg.RequireAPIKey && cfg.DatapulseAPIKey == "" {
		log.Fatal("REQUIRE_API_KEY is set but DATAPULSE_API_KEY is empty")
	}

	var store *parser.Store
	var sb *storage.Supabase
	var widgetStore *config.WidgetStore

	if cfg.UseSupabase() {
		log.Println("mode: Supabase (cloud storage)")
		sb = storage.NewSupabase(cfg.SupabaseURL, cfg.SupabaseKey, cfg.SupabaseHTTPTimeout)

		store = parser.NewStoreWithBackend(sb)
		if err := store.Refresh(); err != nil {
			log.Printf("warning: initial load from Supabase failed: %v", err)
		}

		var err error
		widgetStore, err = config.NewWidgetStoreWithBackend(sb)
		if err != nil {
			log.Fatalf("failed to load widget config: %v", err)
		}
	} else {
		log.Println("mode: local disk (" + cfg.EmailDir + ")")
		store = parser.NewStore(cfg.EmailDir)
		if err := store.Refresh(); err != nil {
			log.Printf("warning: initial scan failed: %v", err)
		}

		var err error
		widgetStore, err = config.NewWidgetStore("config/widgets.json")
		if err != nil {
			log.Fatalf("failed to load widget config: %v", err)
		}
	}

	tmpl, err := parseTemplatesEmbedded()
	if err != nil {
		log.Fatal(err)
	}

	respCache := cache.NewResponse(cfg.CacheTTL)
	h := handlers.New(store, widgetStore, tmpl, sb, cfg, respCache)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if sb == nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ready": true, "backend": "local"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := sb.Ping(ctx); err != nil {
			http.Error(w, `{"ready":false,"error":"supabase"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ready": true, "backend": "supabase"})
	})
	mux.HandleFunc("/api/bootstrap", h.HealthBootstrap)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		h.Dashboard(w, r)
	})
	mux.HandleFunc("/refresh", h.Refresh)
	mux.HandleFunc("/tables", h.Tables)
	mux.HandleFunc("/insights", h.Insights)
	mux.HandleFunc("/upload", h.Upload)
	mux.HandleFunc("/upload/bulk", h.UploadBulk)
	mux.HandleFunc("/upload/seed", h.Seed)
	mux.HandleFunc("/drilldown", h.Drilldown)

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.GetConfig(w, r)
		case http.MethodPost:
			h.PostConfig(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/sources", h.GetSources)
	mux.HandleFunc("/api/uploads", h.UploadList)

	staticSub, err := fs.Sub(datapulse.StaticFS, "static")
	if err != nil {
		log.Fatalf("embedded static FS: %v", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	addr := ":" + strconv.Itoa(cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Printf("DataPulse listening on http://localhost%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func parseTemplatesEmbedded() (*template.Template, error) {
	funcMap := template.FuncMap{
		"sub":      func(a, b int) int { return a - b },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
	}
	return template.New("").Funcs(funcMap).ParseFS(datapulse.TemplateFS,
		"templates/layout.html",
		"templates/dashboard.html",
		"templates/insights.html",
		"templates/tables.html",
	)
}
