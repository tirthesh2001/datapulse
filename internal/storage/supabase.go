package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type EmailRecord struct {
	ID         int       `json:"id"`
	Filename   string    `json:"filename"`
	ReportDate string    `json:"report_date"`
	Data       string    `json:"data"`
	SizeRaw    int       `json:"size_raw"`
	SizeGz     int       `json:"size_gz"`
	UploadedAt time.Time `json:"uploaded_at"`
}

type WidgetConfigRecord struct {
	ID        int             `json:"id"`
	Config    json.RawMessage `json:"config"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type Supabase struct {
	url     string
	key     string
	client  *http.Client
	timeout time.Duration
}

func NewSupabase(projectURL, serviceKey string, httpTimeout time.Duration) *Supabase {
	if httpTimeout <= 0 {
		httpTimeout = 30 * time.Second
	}
	tr := &http.Transport{
		ResponseHeaderTimeout: httpTimeout,
	}
	return &Supabase{
		url:     strings.TrimRight(projectURL, "/"),
		key:     serviceKey,
		timeout: httpTimeout,
		client:  &http.Client{Transport: tr},
	}
}

func (s *Supabase) request(method, path string, body io.Reader) (*http.Request, error) {
	return s.requestCtx(context.Background(), method, path, body)
}

func (s *Supabase) requestCtx(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := s.url + "/rest/v1/" + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", s.key)
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=representation")
	return req, nil
}

func (s *Supabase) do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.client.Do(req.WithContext(c))
}

// Ping checks REST reachability (readiness).
func (s *Supabase) Ping(ctx context.Context) error {
	req, err := s.requestCtx(ctx, "GET", "emails?select=id&limit=1", nil)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("supabase ping: %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

/* ── Email Operations ────────────────────────────────── */

func Compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func Decompress(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}

func (s *Supabase) SaveEmail(filename, reportDate string, rawData []byte) error {
	compressed, err := Compress(rawData)
	if err != nil {
		return fmt.Errorf("compressing email: %w", err)
	}

	b64 := base64.StdEncoding.EncodeToString(compressed)
	payload := map[string]interface{}{
		"filename":    filename,
		"report_date": reportDate,
		"data":        b64,
		"size_raw":    len(rawData),
		"size_gz":     len(compressed),
	}

	body, _ := json.Marshal(payload)
	req, err := s.request("POST", "emails?on_conflict=report_date", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Prefer", "resolution=merge-duplicates,return=representation")

	resp, err := s.do(context.Background(), req)
	if err != nil {
		return fmt.Errorf("saving email: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("supabase error %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (s *Supabase) UpsertEmail(filename, reportDate string, rawData []byte) error {
	if err := s.DeleteEmail(reportDate); err != nil {
		return fmt.Errorf("deleting old email: %w", err)
	}
	return s.SaveEmail(filename, reportDate, rawData)
}

func (s *Supabase) CheckDateExists(reportDate string) (bool, string, error) {
	path := "emails?report_date=eq." + url.QueryEscape(reportDate) + "&select=filename"
	req, err := s.request("GET", path, nil)
	if err != nil {
		return false, "", err
	}

	resp, err := s.do(context.Background(), req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	var records []struct {
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return false, "", err
	}
	if len(records) > 0 {
		return true, records[0].Filename, nil
	}
	return false, "", nil
}

func (s *Supabase) DeleteEmail(reportDate string) error {
	path := "emails?report_date=eq." + url.QueryEscape(reportDate)
	req, err := s.request("DELETE", path, nil)
	if err != nil {
		return err
	}

	resp, err := s.do(context.Background(), req)
	if err != nil {
		return fmt.Errorf("deleting email: %w", err)
	}
	defer resp.Body.Close()
	return nil
}

func (s *Supabase) LoadAllEmails() (map[string][]byte, error) {
	req, err := s.request("GET", "emails?select=report_date,data", nil)
	if err != nil {
		return nil, err
	}

	bulkTimeout := s.timeout * 4
	if bulkTimeout < 2*time.Minute {
		bulkTimeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	resp, err := s.client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("loading emails: %w", err)
	}
	defer resp.Body.Close()

	var records []struct {
		ReportDate string `json:"report_date"`
		Data       string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, fmt.Errorf("decoding emails: %w", err)
	}

	result := make(map[string][]byte)
	for _, rec := range records {
		compressed, err := base64.StdEncoding.DecodeString(rec.Data)
		if err != nil {
			fmt.Printf("warning: base64 decode failed for %s: %v\n", rec.ReportDate, err)
			continue
		}
		raw, err := Decompress(compressed)
		if err != nil {
			fmt.Printf("warning: decompress failed for %s: %v\n", rec.ReportDate, err)
			continue
		}
		result[rec.ReportDate] = raw
	}
	return result, nil
}

func (s *Supabase) ListDates() ([]string, error) {
	req, err := s.request("GET", "emails?select=report_date&order=report_date.asc", nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.do(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("listing dates: %w", err)
	}
	defer resp.Body.Close()

	var records []struct {
		ReportDate string `json:"report_date"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, err
	}

	var dates []string
	for _, r := range records {
		dates = append(dates, r.ReportDate)
	}
	return dates, nil
}

/* ── Widget Config Operations ────────────────────────── */

func (s *Supabase) LoadWidgetConfig() (json.RawMessage, error) {
	req, err := s.request("GET", "widget_config?id=eq.1", nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.do(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("loading widget config: %w", err)
	}
	defer resp.Body.Close()

	var records []WidgetConfigRecord
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return records[0].Config, nil
}

func (s *Supabase) SaveWidgetConfig(config json.RawMessage) error {
	payload := map[string]interface{}{
		"id":     1,
		"config": config,
	}
	body, _ := json.Marshal(payload)

	req, err := s.request("POST", "widget_config?on_conflict=id", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Prefer", "resolution=merge-duplicates,return=representation")

	resp, err := s.do(context.Background(), req)
	if err != nil {
		return fmt.Errorf("saving widget config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("supabase error %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

/* ── Per-user widget config ───────────────────────────── */

func (s *Supabase) LoadUserWidgetConfig(userID string) (json.RawMessage, error) {
	path := "user_widget_config?user_id=eq." + url.QueryEscape(userID) + "&select=config"
	req, err := s.request("GET", path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("loading user widget config: %w", err)
	}
	defer resp.Body.Close()
	var records []struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return records[0].Config, nil
}

func (s *Supabase) SaveUserWidgetConfig(userID string, config json.RawMessage) error {
	payload := map[string]interface{}{
		"user_id": userID,
		"config":  config,
	}
	body, _ := json.Marshal(payload)
	req, err := s.request("POST", "user_widget_config?on_conflict=user_id", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Prefer", "resolution=merge-duplicates,return=representation")
	resp, err := s.do(context.Background(), req)
	if err != nil {
		return fmt.Errorf("saving user widget config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("supabase error %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

/* ── Upload metadata (history) ─────────────────────────── */

type EmailUploadMeta struct {
	Filename   string    `json:"filename"`
	ReportDate string    `json:"report_date"`
	SizeRaw    int       `json:"size_raw"`
	SizeGz     int       `json:"size_gz"`
	UploadedAt time.Time `json:"uploaded_at"`
}

func (s *Supabase) ListEmailUploads() ([]EmailUploadMeta, error) {
	req, err := s.request("GET", "emails?select=filename,report_date,size_raw,size_gz,uploaded_at&order=uploaded_at.desc", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(context.Background(), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("supabase %d: %s", resp.StatusCode, string(b))
	}
	var out []EmailUploadMeta
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

/* ── Partial email load ────────────────────────────────── */

func (s *Supabase) LoadEmailsForDates(dates []string) (map[string][]byte, error) {
	if len(dates) == 0 {
		return map[string][]byte{}, nil
	}
	in := strings.Join(dates, ",")
	path := "emails?report_date=in.(" + in + ")&select=report_date,data"
	req, err := s.request("GET", path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("loading emails for dates: %w", err)
	}
	defer resp.Body.Close()
	var records []struct {
		ReportDate string `json:"report_date"`
		Data       string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, fmt.Errorf("decoding emails: %w", err)
	}
	result := make(map[string][]byte)
	for _, rec := range records {
		compressed, err := base64.StdEncoding.DecodeString(rec.Data)
		if err != nil {
			continue
		}
		raw, err := Decompress(compressed)
		if err != nil {
			continue
		}
		result[rec.ReportDate] = raw
	}
	return result, nil
}
