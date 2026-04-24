package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Column struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Format string `json:"format,omitempty"`
	Type   string `json:"type,omitempty"`
}

type Pivot struct {
	LabelColumn string `json:"labelColumn"`
	ValueColumn string `json:"valueColumn"`
}

type WidgetSection struct {
	Source    string   `json:"source"`
	Label    string   `json:"label,omitempty"`
	Columns  []Column `json:"columns"`
	Limit    int      `json:"limit,omitempty"`
	Aggregate bool   `json:"aggregate,omitempty"`
	Pivot    *Pivot   `json:"pivot,omitempty"`
}

type Widget struct {
	ID       string          `json:"id"`
	Label    string          `json:"label"`
	Visible  bool            `json:"visible"`
	Sections []WidgetSection `json:"sections"`
}

type WidgetConfig struct {
	Widgets []Widget `json:"widgets"`
}

// WidgetBackend allows persistence to Supabase instead of local files.
type WidgetBackend interface {
	LoadWidgetConfig() (json.RawMessage, error)
	SaveWidgetConfig(json.RawMessage) error
}

type WidgetStore struct {
	mu       sync.RWMutex
	config   WidgetConfig
	filePath string
	backend  WidgetBackend
}

func NewWidgetStore(filePath string) (*WidgetStore, error) {
	ws := &WidgetStore{filePath: filePath}
	if err := ws.load(); err != nil {
		return nil, err
	}
	return ws, nil
}

func NewWidgetStoreWithBackend(backend WidgetBackend) (*WidgetStore, error) {
	ws := &WidgetStore{backend: backend}
	if err := ws.load(); err != nil {
		return nil, err
	}
	return ws, nil
}

func (ws *WidgetStore) load() error {
	if ws.backend != nil {
		return ws.loadFromBackend()
	}
	return ws.loadFromFile()
}

func (ws *WidgetStore) loadFromFile() error {
	data, err := os.ReadFile(ws.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			ws.config = defaultWidgetConfig()
			return ws.save()
		}
		return fmt.Errorf("reading widget config: %w", err)
	}
	if err := json.Unmarshal(data, &ws.config); err != nil {
		return fmt.Errorf("parsing widget config: %w", err)
	}
	return nil
}

func (ws *WidgetStore) loadFromBackend() error {
	raw, err := ws.backend.LoadWidgetConfig()
	if err != nil {
		return fmt.Errorf("loading widget config from backend: %w", err)
	}
	if raw == nil {
		ws.config = defaultWidgetConfig()
		return ws.save()
	}
	if err := json.Unmarshal(raw, &ws.config); err != nil {
		return fmt.Errorf("parsing widget config: %w", err)
	}
	return nil
}

func (ws *WidgetStore) save() error {
	if ws.backend != nil {
		return ws.saveToBackend()
	}
	return ws.saveToFile()
}

func (ws *WidgetStore) saveToFile() error {
	data, err := json.MarshalIndent(ws.config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling widget config: %w", err)
	}

	dir := filepath.Dir(ws.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	tmp := ws.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := os.Rename(tmp, ws.filePath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming config: %w", err)
	}
	return nil
}

func (ws *WidgetStore) saveToBackend() error {
	data, err := json.Marshal(ws.config)
	if err != nil {
		return fmt.Errorf("marshaling widget config: %w", err)
	}
	return ws.backend.SaveWidgetConfig(json.RawMessage(data))
}

func (ws *WidgetStore) Get() WidgetConfig {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.config
}

func (ws *WidgetStore) Update(cfg WidgetConfig) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.config = cfg
	return ws.save()
}

// ValidateWidgetConfig checks widget JSON invariants.
func ValidateWidgetConfig(cfg WidgetConfig) error {
	return validateConfig(cfg)
}

func validateConfig(cfg WidgetConfig) error {
	ids := make(map[string]bool)
	for _, w := range cfg.Widgets {
		if w.ID == "" {
			return fmt.Errorf("widget missing id")
		}
		if ids[w.ID] {
			return fmt.Errorf("duplicate widget id: %s", w.ID)
		}
		ids[w.ID] = true
		if w.Label == "" {
			return fmt.Errorf("widget %s missing label", w.ID)
		}
		if len(w.Sections) == 0 {
			return fmt.Errorf("widget %s has no sections", w.ID)
		}
		for _, sec := range w.Sections {
			if sec.Source == "" {
				return fmt.Errorf("widget %s has section with empty source", w.ID)
			}
			if len(sec.Columns) == 0 {
				return fmt.Errorf("widget %s section %q has no columns", w.ID, sec.Source)
			}
		}
	}
	return nil
}

// DefaultWidgetConfig returns the built-in dashboard layout (used for new users / empty DB).
func DefaultWidgetConfig() WidgetConfig {
	return defaultWidgetConfig()
}

func defaultWidgetConfig() WidgetConfig {
	return WidgetConfig{
		Widgets: []Widget{
			{
				ID: "upi-txn", Label: "UPI Transaction Details", Visible: true,
				Sections: []WidgetSection{
					{
						Source: "UPI TRANSACTION DETAILS",
						Columns: []Column{
							{Key: "_date", Label: "Date"},
							{Key: "SUCCESS", Label: "TXN Success", Format: "K"},
							{Key: "SUCCESS_pct", Label: "% Change (Success)", Type: "pct"},
							{Key: "FAILURE", Label: "TXN Failure", Format: "K"},
							{Key: "FAILURE_pct", Label: "% Change (Failure)", Type: "pct"},
							{Key: "TOTAL_TRANSACTIONS", Label: "Total TXN", Format: "K"},
							{Key: "TOTAL_TRANSACTIONS_pct", Label: "% Change Total TXN", Type: "pct"},
						},
					},
					{
						Source: "TOP 25 UPI TRANSACTION FAILURES", Label: "Top Reasons for Failure",
						Columns: []Column{
							{Key: "_rank", Label: "Sr. No."},
							{Key: "ERROR_MESSAGE", Label: "Error Description"},
							{Key: "TOTAL", Label: "Total Count", Format: "K"},
						},
						Limit: 5, Aggregate: true,
					},
				},
			},
			{
				ID: "active-users", Label: "Active Users (JFS App)", Visible: true,
				Sections: []WidgetSection{
					{
						Source: "DEVICE BINDING COUNT",
						Columns: []Column{
							{Key: "_date", Label: "Date"},
							{Key: "DEVICE BINDED USER", Label: "Binding Success", Format: "K"},
							{Key: "LOGGED IN BUT NOT DEVICE BINDED USER", Label: "Binding Pending", Format: "K"},
						},
					},
				},
			},
			{
				ID: "journey-wise", Label: "Journey Wise Transactions", Visible: true,
				Sections: []WidgetSection{
					{
						Source: "JOURNEY WISE SUCCESS AND FAILURE COUNT",
						Columns: []Column{
							{Key: "_date", Label: "Date"},
							{Key: "TOTAL_TRANSACTIONS", Label: "Total TXN", Format: "K"},
						},
						Pivot: &Pivot{LabelColumn: "TRANSACTION_TYPE", ValueColumn: "TOTAL_TRANSACTIONS"},
					},
				},
			},
		},
	}
}
