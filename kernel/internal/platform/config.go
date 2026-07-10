// Package platform holds cross-cutting kernel infrastructure: configuration,
// logging, health, and storage connection management.
package platform

import (
	"encoding/json"
	"os"
	"strconv"

	"github.com/m7mdhka/llmobs/kernel/pkg/brand"
)

// Config is the kernel daemon configuration. 12-factor: defaults, then an
// optional JSON file (LLMOBS_CONFIG_FILE), then environment overrides. Every env
// var is LLMOBS_-prefixed via the brand constant (D15).
type Config struct {
	DatabaseURL       string `json:"database_url"`
	OTLPHTTPAddr      string `json:"otlp_http_addr"`
	OTLPGRPCAddr      string `json:"otlp_grpc_addr"`
	APIAddr           string `json:"api_addr"`
	LogLevel          string `json:"log_level"`
	LogFormat         string `json:"log_format"`
	MigrateOnBoot     bool   `json:"migrate_on_boot"`
	BootstrapProject  string `json:"bootstrap_project"`
	BootstrapAPIKey   string `json:"bootstrap_api_key"`
	BootstrapAdminEml string `json:"bootstrap_admin_email"`
	BootstrapAdminPwd string `json:"bootstrap_admin_password"`
	QueryMaxWindow    string `json:"query_max_window"` // e.g. "720h"; LLMOBS_QUERY_MAX_WINDOW
	CookieSecure      bool   `json:"cookie_secure"`    // set Secure on session cookies
}

func defaults() Config {
	return Config{
		DatabaseURL:      "postgres://llmobs:llmobs@localhost:5432/llmobs?sslmode=disable",
		OTLPHTTPAddr:     ":4318",
		OTLPGRPCAddr:     ":4317",
		APIAddr:          ":8080",
		LogLevel:         "info",
		LogFormat:        "json",
		MigrateOnBoot:    true,
		BootstrapProject: "default",
		QueryMaxWindow:   "720h",
	}
}

// LoadConfig builds the configuration from defaults, an optional JSON file, and
// environment overrides.
func LoadConfig() (Config, error) {
	c := defaults()
	if p := os.Getenv(brand.Env("CONFIG_FILE")); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return c, err
		}
		if err := json.Unmarshal(b, &c); err != nil {
			return c, err
		}
	}
	envStr(brand.Env("DATABASE_URL"), &c.DatabaseURL)
	envStr(brand.Env("OTLP_HTTP_ADDR"), &c.OTLPHTTPAddr)
	envStr(brand.Env("OTLP_GRPC_ADDR"), &c.OTLPGRPCAddr)
	envStr(brand.Env("API_ADDR"), &c.APIAddr)
	envStr(brand.Env("LOG_LEVEL"), &c.LogLevel)
	envStr(brand.Env("LOG_FORMAT"), &c.LogFormat)
	envStr(brand.Env("BOOTSTRAP_PROJECT"), &c.BootstrapProject)
	envStr(brand.Env("BOOTSTRAP_API_KEY"), &c.BootstrapAPIKey)
	envStr(brand.Env("BOOTSTRAP_ADMIN_EMAIL"), &c.BootstrapAdminEml)
	envStr(brand.Env("BOOTSTRAP_ADMIN_PASSWORD"), &c.BootstrapAdminPwd)
	envStr(brand.Env("QUERY_MAX_WINDOW"), &c.QueryMaxWindow)
	envBool(brand.Env("MIGRATE_ON_BOOT"), &c.MigrateOnBoot)
	envBool(brand.Env("COOKIE_SECURE"), &c.CookieSecure)
	return c, nil
}

func envStr(key string, dst *string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func envBool(key string, dst *bool) {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		}
	}
}
