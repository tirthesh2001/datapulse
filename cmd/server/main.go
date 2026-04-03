package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

	var store *parser.Store
	var sb *storage.Supabase
	var widgetStore *config.WidgetStore

	if cfg.UseSupabase() {
		log.Println("mode: Supabase (cloud storage)")
		sb = storage.NewSupabase(cfg.SupabaseURL, cfg.SupabaseKey)

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

	tmpl, err := parseTemplates("templates")
	if err != nil {
		log.Fatal(err)
	}

	h := handlers.New(store, widgetStore, tmpl, sb)

	mux := http.NewServeMux()
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
	mux.HandleFunc("/upload/seed", h.Seed)

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.GetConfig(w, r)
		case http.MethodPost:
			h.PostConfig(w, r)
		default:
			http.Error(w, "method not allowed", 405)
		}
	})
	mux.HandleFunc("/api/sources", h.GetSources)

	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	addr := ":" + strconv.Itoa(cfg.Port)
	log.Printf("DataPulse listening on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func parseTemplates(dir string) (*template.Template, error) {
	funcMap := template.FuncMap{
		"sub":      func(a, b int) int { return a - b },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
	}

	var files []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".html") {
			files = append(files, path)
		}
		return nil
	})
	if len(files) == 0 {
		return nil, nil
	}
	return template.New(filepath.Base(files[0])).Funcs(funcMap).ParseFiles(files...)
}
