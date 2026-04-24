package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"datapulse/internal/parser"
)

// Upload accepts a single .eml file (requires auth when enabled).
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.requireProtected(w, r); !ok {
		return
	}

	maxBytes := h.cfg.MaxUploadBytes
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		jsonError(w, "could not parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "missing file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".eml") {
		jsonError(w, "only .eml files are accepted", http.StatusBadRequest)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		jsonError(w, "reading file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if int64(len(raw)) > maxBytes {
		jsonError(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}

	report, err := parser.ParseEMLBytes(raw)
	if err != nil {
		jsonError(w, "parsing email: "+err.Error(), http.StatusBadRequest)
		return
	}
	dateKey := report.Date.Format("2006-01-02")
	replace := r.FormValue("replace") == "true"

	if h.supabase != nil {
		exists, existingFile, err := h.supabase.CheckDateExists(dateKey)
		if err != nil {
			jsonError(w, "checking duplicates: "+err.Error(), http.StatusInternalServerError)
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
				jsonError(w, "replacing email: "+err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			if err := h.supabase.SaveEmail(header.Filename, dateKey, raw); err != nil {
				jsonError(w, "saving email: "+err.Error(), http.StatusInternalServerError)
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
			_ = os.Remove(existingPath)
		}

		dst := filepath.Join(emailDir, header.Filename)
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			jsonError(w, "writing file: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if _, err := h.store.AddReport(raw); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.invalidateCaches()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"date":   dateKey,
		"file":   header.Filename,
		"dates":  h.store.AvailableDates(),
	})
}

// UploadBulk accepts multiple .eml files (field "files" or "file").
func (h *Handler) UploadBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.requireProtected(w, r); !ok {
		return
	}
	maxBytes := h.cfg.MaxUploadBytes
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		jsonError(w, "could not parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	var headers []*multipart.FileHeader
	if r.MultipartForm != nil {
		headers = append(headers, r.MultipartForm.File["files"]...)
		if len(headers) == 0 {
			headers = r.MultipartForm.File["file"]
		}
	}
	if len(headers) == 0 {
		jsonError(w, "no files", http.StatusBadRequest)
		return
	}
	type itemResult struct {
		File   string `json:"file,omitempty"`
		Date   string `json:"date,omitempty"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	var results []itemResult
	for _, fh := range headers {
		if !strings.HasSuffix(strings.ToLower(fh.Filename), ".eml") {
			results = append(results, itemResult{File: fh.Filename, Status: "skipped", Error: "not .eml"})
			continue
		}
		f, err := fh.Open()
		if err != nil {
			results = append(results, itemResult{File: fh.Filename, Status: "error", Error: err.Error()})
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
		f.Close()
		if err != nil {
			results = append(results, itemResult{File: fh.Filename, Status: "error", Error: err.Error()})
			continue
		}
		if int64(len(raw)) > maxBytes {
			results = append(results, itemResult{File: fh.Filename, Status: "error", Error: "file too large"})
			continue
		}
		report, err := parser.ParseEMLBytes(raw)
		if err != nil {
			results = append(results, itemResult{File: fh.Filename, Status: "error", Error: err.Error()})
			continue
		}
		dateKey := report.Date.Format("2006-01-02")
		if h.supabase != nil {
			exists, _, err := h.supabase.CheckDateExists(dateKey)
			if err != nil {
				results = append(results, itemResult{File: fh.Filename, Status: "error", Error: err.Error()})
				continue
			}
			if exists {
				results = append(results, itemResult{File: fh.Filename, Date: dateKey, Status: "conflict"})
				continue
			}
			if err := h.supabase.SaveEmail(fh.Filename, dateKey, raw); err != nil {
				results = append(results, itemResult{File: fh.Filename, Status: "error", Error: err.Error()})
				continue
			}
		} else {
			dst := filepath.Join(h.store.EmailDir(), fh.Filename)
			if err := os.WriteFile(dst, raw, 0o644); err != nil {
				results = append(results, itemResult{File: fh.Filename, Status: "error", Error: err.Error()})
				continue
			}
		}
		if _, err := h.store.AddReport(raw); err != nil {
			results = append(results, itemResult{File: fh.Filename, Status: "error", Error: err.Error()})
			continue
		}
		results = append(results, itemResult{File: fh.Filename, Date: dateKey, Status: "ok"})
	}
	h.invalidateCaches()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
		"dates":   h.store.AvailableDates(),
	})
}

// Seed bulk-uploads all .eml files from a local directory into Supabase (API key / admin).
func (h *Handler) Seed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.requireProtected(w, r); !ok {
		return
	}
	if h.supabase == nil {
		jsonError(w, "seeding requires Supabase mode", http.StatusBadRequest)
		return
	}

	dir := r.FormValue("dir")
	if dir == "" {
		dir = "UPI Email Reports"
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		jsonError(w, "reading directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var uploaded, skipped, failed int
	var errs []string

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".eml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: read error: %v", e.Name(), err))
			failed++
			continue
		}

		report, err := parser.ParseEMLBytes(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: parse error: %v", e.Name(), err))
			failed++
			continue
		}
		dateKey := report.Date.Format("2006-01-02")

		exists, _, err := h.supabase.CheckDateExists(dateKey)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: check error: %v", e.Name(), err))
			failed++
			continue
		}
		if exists {
			skipped++
			continue
		}

		if err := h.supabase.SaveEmail(e.Name(), dateKey, raw); err != nil {
			errs = append(errs, fmt.Sprintf("%s: save error: %v", e.Name(), err))
			failed++
			continue
		}
		if _, err := h.store.AddReport(raw); err != nil {
			errs = append(errs, fmt.Sprintf("%s: store: %v", e.Name(), err))
			failed++
			continue
		}
		uploaded++
	}

	h.invalidateCaches()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"uploaded": uploaded,
		"skipped":  skipped,
		"failed":   failed,
		"errors":   errs,
		"dates":    h.store.AvailableDates(),
	})
}
