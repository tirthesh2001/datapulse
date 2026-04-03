package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port        int
	EmailDir    string
	SupabaseURL string
	SupabaseKey string
}

func Load() *Config {
	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}
	emailDir := "UPI Email Reports"
	if d := os.Getenv("EMAIL_DIR"); d != "" {
		emailDir = d
	}
	return &Config{
		Port:        port,
		EmailDir:    emailDir,
		SupabaseURL: os.Getenv("SUPABASE_URL"),
		SupabaseKey: os.Getenv("SUPABASE_KEY"),
	}
}

func (c *Config) UseSupabase() bool {
	return c.SupabaseURL != "" && c.SupabaseKey != ""
}
