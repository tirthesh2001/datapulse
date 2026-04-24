package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port        int
	EmailDir    string
	SupabaseURL string
	SupabaseKey string // service role — server only
	SupabaseAnonKey string
	JWTSecret   string // Supabase JWT secret (Settings → API)
	DatapulseAPIKey string
	RequireAPIKey bool
	MaxUploadBytes int64
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration
	ShutdownTimeout time.Duration
	SupabaseHTTPTimeout time.Duration
	CacheTTL       time.Duration
}

func Load() *Config {
	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}
	maxUp := int64(32 << 20) // 32 MiB
	if s := os.Getenv("MAX_UPLOAD_BYTES"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
			maxUp = v
		}
	}
	require := strings.EqualFold(os.Getenv("REQUIRE_API_KEY"), "true") ||
		strings.EqualFold(os.Getenv("REQUIRE_API_KEY"), "1")

	return &Config{
		Port:        port,
		EmailDir:    getEnvDefault("EMAIL_DIR", "UPI Email Reports"),
		SupabaseURL: os.Getenv("SUPABASE_URL"),
		SupabaseKey: os.Getenv("SUPABASE_KEY"),
		SupabaseAnonKey: os.Getenv("SUPABASE_ANON_KEY"),
		JWTSecret:       strings.TrimSpace(os.Getenv("SUPABASE_JWT_SECRET")),
		DatapulseAPIKey: strings.TrimSpace(os.Getenv("DATAPULSE_API_KEY")),
		RequireAPIKey:   require,
		MaxUploadBytes:  maxUp,
		ReadTimeout:     getEnvDuration("HTTP_READ_TIMEOUT", 30*time.Second),
		WriteTimeout:    getEnvDuration("HTTP_WRITE_TIMEOUT", 120*time.Second),
		IdleTimeout:     getEnvDuration("HTTP_IDLE_TIMEOUT", 120*time.Second),
		ShutdownTimeout: getEnvDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
		SupabaseHTTPTimeout: getEnvDuration("SUPABASE_HTTP_TIMEOUT", 25*time.Second),
		CacheTTL:        getEnvDuration("RESPONSE_CACHE_TTL", 60*time.Second),
	}
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

func (c *Config) UseSupabase() bool {
	return c.SupabaseURL != "" && c.SupabaseKey != ""
}

// AuthEnabled means API key and/or JWT verification is required for protected routes.
func (c *Config) AuthEnabled() bool {
	return c.DatapulseAPIKey != "" || c.JWTSecret != ""
}
