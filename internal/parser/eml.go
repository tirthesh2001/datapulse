package parser

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

type SectionData struct {
	Headers []string
	Rows    [][]string
}

type DayReport struct {
	Date     time.Time
	Sections map[string]*SectionData
}

// StorageBackend is implemented by supabase storage for production mode.
type StorageBackend interface {
	LoadAllEmails() (map[string][]byte, error)
}

type Store struct {
	mu       sync.RWMutex
	reports  map[string]*DayReport
	emailDir string
	backend  StorageBackend
}

func NewStore(emailDir string) *Store {
	return &Store{
		reports:  make(map[string]*DayReport),
		emailDir: emailDir,
	}
}

func NewStoreWithBackend(backend StorageBackend) *Store {
	return &Store{
		reports: make(map[string]*DayReport),
		backend: backend,
	}
}

func (s *Store) EmailDir() string { return s.emailDir }

func (s *Store) Refresh() error {
	if s.backend != nil {
		return s.refreshFromBackend()
	}
	return s.refreshFromDisk()
}

func (s *Store) refreshFromDisk() error {
	entries, err := os.ReadDir(s.emailDir)
	if err != nil {
		return fmt.Errorf("reading email dir: %w", err)
	}

	newReports := make(map[string]*DayReport)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".eml") {
			continue
		}
		path := filepath.Join(s.emailDir, e.Name())
		report, err := ParseEML(path)
		if err != nil {
			fmt.Printf("warning: skipping %s: %v\n", e.Name(), err)
			continue
		}
		key := report.Date.Format("2006-01-02")
		newReports[key] = report
	}

	s.mu.Lock()
	s.reports = newReports
	s.mu.Unlock()
	return nil
}

func (s *Store) refreshFromBackend() error {
	emails, err := s.backend.LoadAllEmails()
	if err != nil {
		return fmt.Errorf("loading from backend: %w", err)
	}

	newReports := make(map[string]*DayReport)
	for date, raw := range emails {
		report, err := ParseEMLBytes(raw)
		if err != nil {
			fmt.Printf("warning: skipping %s: %v\n", date, err)
			continue
		}
		key := report.Date.Format("2006-01-02")
		newReports[key] = report
	}

	s.mu.Lock()
	s.reports = newReports
	s.mu.Unlock()
	return nil
}

// AddReport parses raw .eml bytes and adds the result to the in-memory store.
func (s *Store) AddReport(raw []byte) (*DayReport, error) {
	report, err := ParseEMLBytes(raw)
	if err != nil {
		return nil, err
	}
	key := report.Date.Format("2006-01-02")
	s.mu.Lock()
	s.reports[key] = report
	s.mu.Unlock()
	return report, nil
}

func (s *Store) AvailableDates() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dates := make([]string, 0, len(s.reports))
	for d := range s.reports {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	return dates
}

func (s *Store) GetReports(dates []string) []*DayReport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*DayReport
	sort.Strings(dates)
	for _, d := range dates {
		if r, ok := s.reports[d]; ok {
			result = append(result, r)
		}
	}
	return result
}

func (s *Store) AvailableSources() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sources := make(map[string][]string)
	for _, rpt := range s.reports {
		for name, sec := range rpt.Sections {
			if _, ok := sources[name]; !ok {
				sources[name] = sec.Headers
			}
		}
	}
	return sources
}

func (s *Store) HasDate(date string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.reports[date]
	return ok
}

/* ── Parsing ─────────────────────────────────────────── */

func ParseEML(path string) (*DayReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	msg, err := mail.ReadMessage(f)
	if err != nil {
		return nil, fmt.Errorf("reading mail: %w", err)
	}

	htmlBody, err := extractHTMLBody(msg)
	if err != nil {
		return nil, fmt.Errorf("extracting HTML: %w", err)
	}

	return parseHTMLReport(htmlBody)
}

func ParseEMLBytes(data []byte) (*DayReport, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("reading mail: %w", err)
	}

	htmlBody, err := extractHTMLBody(msg)
	if err != nil {
		return nil, fmt.Errorf("extracting HTML: %w", err)
	}

	return parseHTMLReport(htmlBody)
}

func extractHTMLBody(msg *mail.Message) (string, error) {
	contentType := msg.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("parsing content-type: %w", err)
	}

	if strings.HasPrefix(mediaType, "text/html") {
		body, err := io.ReadAll(msg.Body)
		if err != nil {
			return "", err
		}
		return decodeQuotedPrintable(string(body)), nil
	}

	if !strings.HasPrefix(mediaType, "multipart/") {
		body, err := io.ReadAll(msg.Body)
		if err != nil {
			return "", err
		}
		text := decodeQuotedPrintable(string(body))
		if strings.Contains(text, "<html") || strings.Contains(text, "<table") {
			return text, nil
		}
		return "", fmt.Errorf("no HTML body found, content-type: %s", mediaType)
	}

	boundary := params["boundary"]
	if boundary == "" {
		return "", fmt.Errorf("no boundary in multipart")
	}

	reader := multipart.NewReader(msg.Body, boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		partCT := part.Header.Get("Content-Type")
		if strings.Contains(partCT, "text/html") {
			body, err := io.ReadAll(part)
			if err != nil {
				return "", err
			}
			return decodeQuotedPrintable(string(body)), nil
		}
	}
	return "", fmt.Errorf("no text/html part found")
}

func decodeQuotedPrintable(s string) string {
	s = strings.ReplaceAll(s, "=\r\n", "")
	s = strings.ReplaceAll(s, "=\n", "")
	s = strings.ReplaceAll(s, "=3D", "=")
	s = strings.ReplaceAll(s, "=20", " ")
	return s
}

var datePattern = regexp.MustCompile(`(?i)DAILY\s+REPORT\s+FOR:\s*(\d{1,2}-[A-Z]{3}-\d{2,4})`)

func parseHTMLReport(htmlBody string) (*DayReport, error) {
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return nil, fmt.Errorf("parsing HTML: %w", err)
	}

	tables := extractTables(doc)
	report := &DayReport{
		Sections: make(map[string]*SectionData),
	}

	dateStr := extractDate(htmlBody)
	if dateStr == "" {
		return nil, fmt.Errorf("could not find report date")
	}
	t, err := parseReportDate(dateStr)
	if err != nil {
		return nil, fmt.Errorf("parsing date %q: %w", dateStr, err)
	}
	report.Date = t

	for i := 0; i < len(tables); i++ {
		t := tables[i]
		if isSingleCellTable(t) {
			label := strings.TrimSpace(t.rows[0][0])
			if label == "" {
				continue
			}
			if i+1 < len(tables) {
				next := tables[i+1]
				if isSingleCellTable(next) {
					continue
				}
				report.Sections[label] = &SectionData{
					Headers: next.headers,
					Rows:    next.rows,
				}
				i++
			}
		}
	}

	return report, nil
}

func extractDate(body string) string {
	m := datePattern.FindStringSubmatch(body)
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func parseReportDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "-")
	if len(parts) == 3 {
		low := strings.ToLower(parts[1])
		if len(low) > 0 {
			parts[1] = strings.ToUpper(low[:1]) + low[1:]
		}
		s = strings.Join(parts, "-")
	}
	formats := []string{
		"2-Jan-06",
		"02-Jan-06",
		"2-Jan-2006",
		"02-Jan-2006",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date format: %s", s)
}

type tableData struct {
	headers []string
	rows    [][]string
}

func extractTables(node *html.Node) []tableData {
	var tables []tableData
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" {
			td := parseTable(n)
			tables = append(tables, td)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)
	return tables
}

func parseTable(tableNode *html.Node) tableData {
	var rows [][]string
	var walkTR func(*html.Node)
	walkTR = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			var cells []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
					cells = append(cells, strings.TrimSpace(textContent(c)))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkTR(c)
		}
	}
	walkTR(tableNode)

	td := tableData{}
	if len(rows) == 0 {
		return td
	}

	isHeader := true
	for _, cell := range rows[0] {
		if _, err := strconv.Atoi(strings.ReplaceAll(cell, " ", "")); err == nil {
			isHeader = false
			break
		}
	}
	if isHeader && len(rows) > 1 {
		td.headers = rows[0]
		td.rows = rows[1:]
	} else {
		td.rows = rows
	}
	return td
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(textContent(c))
	}
	return sb.String()
}

func isSingleCellTable(t tableData) bool {
	return len(t.rows) == 1 && len(t.rows[0]) == 1 && len(t.headers) == 0
}

func ParseInt(s string) int {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	v, _ := strconv.Atoi(s)
	return v
}
