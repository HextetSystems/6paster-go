package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestLoadDefaultsAndBaseURL(t *testing.T) {
	name := writeConfig(t, `
server:
  public_url: https://paste.example
  base_path: /tools/paste
database:
  driver: sqlite
  sqlite:
    path: paste.db
`)
	cfg, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":8080" || cfg.Paste.MaximumBytes != 300*1024 || !cfg.Paste.AllowImages {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if got := cfg.PublicBaseURL(); got != "https://paste.example/tools/paste/" {
		t.Fatalf("PublicBaseURL() = %q", got)
	}
}

func TestLoadDisablesImages(t *testing.T) {
	name := writeConfig(t, `
server:
  public_url: https://paste.example
database:
  driver: sqlite
  sqlite: {path: paste.db}
paste:
  allow_images: false
`)
	cfg, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paste.AllowImages {
		t.Fatal("allow_images = true, want false")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	name := writeConfig(t, `
server:
  public_url: https://paste.example
  surprise: true
database:
  driver: sqlite
  sqlite: {path: paste.db}
`)
	_, err := Load(name)
	if err == nil || !strings.Contains(err.Error(), "field surprise not found") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestLoadOptionalFromEnvironment(t *testing.T) {
	env := map[string]string{
		"SIXPASTER_SERVER_LISTEN":           ":9090",
		"SIXPASTER_SERVER_PUBLIC_URL":       "https://paste.example.test",
		"SIXPASTER_SERVER_BASE_PATH":        "/paste",
		"SIXPASTER_SERVER_REQUIRE_HTTPS":    "true",
		"SIXPASTER_SERVER_PAGE_TITLE":       "Environment Paste",
		"SIXPASTER_SERVER_TRUSTED_PROXIES":  "10.0.0.0/8, 2001:db8::/32",
		"SIXPASTER_DATABASE_DRIVER":         "mysql",
		"SIXPASTER_DATABASE_SQLITE_PATH":    "/unused/pastes.db",
		"SIXPASTER_DATABASE_MYSQL_HOST":     "database",
		"SIXPASTER_DATABASE_MYSQL_PORT":     "3307",
		"SIXPASTER_DATABASE_MYSQL_USER":     "paster",
		"SIXPASTER_DATABASE_MYSQL_PASSWORD": "secret",
		"SIXPASTER_DATABASE_MYSQL_DATABASE": "pastes",
		"SIXPASTER_DATABASE_MYSQL_TLS_MODE": "required",
		"SIXPASTER_PASTE_MINIMUM_TTL":       "2h",
		"SIXPASTER_PASTE_MAXIMUM_TTL":       "48h",
		"SIXPASTER_PASTE_MAXIMUM_BYTES":     "123456",
		"SIXPASTER_PASTE_ALLOW_IMAGES":      "false",
		"SIXPASTER_RATE_LIMIT_PER_HOUR":     "25",
		"SIXPASTER_RATE_LIMIT_PER_DAY":      "250",
		"SIXPASTER_CLEANUP_INTERVAL":        "30m",
	}
	for name, value := range env {
		t.Setenv(name, value)
	}

	cfg, err := LoadOptional(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":9090" || cfg.Server.PublicURL != "https://paste.example.test" || cfg.Server.BasePath != "/paste" || !cfg.Server.RequireHTTPS || cfg.Server.PageTitle != "Environment Paste" {
		t.Fatalf("server environment not applied: %+v", cfg.Server)
	}
	if got := strings.Join(cfg.Server.TrustedProxies, ","); got != "10.0.0.0/8,2001:db8::/32" {
		t.Fatalf("trusted proxies = %q", got)
	}
	if cfg.Database.Driver != "mysql" || cfg.Database.SQLite.Path != "/unused/pastes.db" {
		t.Fatalf("database environment not applied: %+v", cfg.Database)
	}
	mysql := cfg.Database.MySQL
	if mysql.Host != "database" || mysql.Port != 3307 || mysql.User != "paster" || mysql.Password != "secret" || mysql.Database != "pastes" || mysql.TLSMode != "required" {
		t.Fatalf("MySQL environment not applied: %+v", mysql)
	}
	if cfg.Paste.MinimumTTL.Duration != 2*time.Hour || cfg.Paste.MaximumTTL.Duration != 48*time.Hour || cfg.Paste.MaximumBytes != 123456 || cfg.Paste.AllowImages {
		t.Fatalf("paste environment not applied: %+v", cfg.Paste)
	}
	if cfg.RateLimit.PerHour != 25 || cfg.RateLimit.PerDay != 250 || cfg.Cleanup.Interval.Duration != 30*time.Minute {
		t.Fatalf("limit environment not applied: rate=%+v cleanup=%+v", cfg.RateLimit, cfg.Cleanup)
	}
}

func TestEnvironmentOverridesYAML(t *testing.T) {
	name := writeConfig(t, `
server:
  public_url: https://yaml.example
  trusted_proxies: ["127.0.0.1/32"]
database:
  driver: sqlite
  sqlite: {path: yaml.db}
paste:
  maximum_bytes: 1000
`)
	t.Setenv("SIXPASTER_SERVER_PUBLIC_URL", "https://env.example")
	t.Setenv("SIXPASTER_SERVER_TRUSTED_PROXIES", "")
	t.Setenv("SIXPASTER_DATABASE_SQLITE_PATH", "env.db")
	t.Setenv("SIXPASTER_PASTE_MAXIMUM_BYTES", "2000")

	cfg, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.PublicURL != "https://env.example" || len(cfg.Server.TrustedProxies) != 0 || cfg.Database.SQLite.Path != "env.db" || cfg.Paste.MaximumBytes != 2000 {
		t.Fatalf("environment did not override YAML: %+v", cfg)
	}
}

func TestLoadEnvironmentParseErrorsNameVariable(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"SIXPASTER_SERVER_REQUIRE_HTTPS", "sometimes"},
		{"SIXPASTER_DATABASE_MYSQL_PORT", "many"},
		{"SIXPASTER_DATABASE_MYSQL_PORT", "70000"},
		{"SIXPASTER_PASTE_MAXIMUM_BYTES", "huge"},
		{"SIXPASTER_PASTE_MINIMUM_TTL", "tomorrow"},
		{"SIXPASTER_SERVER_TRUSTED_PROXIES", "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SIXPASTER_SERVER_PUBLIC_URL", "https://paste.example")
			t.Setenv("SIXPASTER_DATABASE_DRIVER", "sqlite")
			t.Setenv("SIXPASTER_DATABASE_SQLITE_PATH", "paste.db")
			t.Setenv(tt.name, tt.value)
			_, err := LoadOptional(filepath.Join(t.TempDir(), "missing.yaml"))
			if err == nil || !strings.Contains(err.Error(), tt.name) {
				t.Fatalf("error = %v, want variable name", err)
			}
		})
	}
}

func TestLoadMissingFileBehavior(t *testing.T) {
	name := filepath.Join(t.TempDir(), "missing.yaml")
	if _, err := Load(name); err == nil || !strings.Contains(err.Error(), "open config") {
		t.Fatalf("Load missing file error = %v", err)
	}

	t.Setenv("SIXPASTER_SERVER_PUBLIC_URL", "https://paste.example")
	t.Setenv("SIXPASTER_DATABASE_DRIVER", "sqlite")
	t.Setenv("SIXPASTER_DATABASE_SQLITE_PATH", "paste.db")
	if _, err := LoadOptional(name); err != nil {
		t.Fatalf("LoadOptional missing file: %v", err)
	}
}

func TestLoadRejectsMultipleDocuments(t *testing.T) {
	name := writeConfig(t, `
server: {public_url: https://paste.example}
database: {driver: sqlite, sqlite: {path: paste.db}}
---
paste: {allow_images: false}
`)
	_, err := Load(name)
	if err == nil || !strings.Contains(err.Error(), "exactly one YAML document") {
		t.Fatalf("expected multiple document error, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
	}{
		{"public URL path", func(c *Config) { c.Server.PublicURL = "https://example.test/sub" }},
		{"HTTP URL with HTTPS required", func(c *Config) { c.Server.PublicURL = "http://example.test" }},
		{"unclean base path", func(c *Config) { c.Server.BasePath = "/sub/" }},
		{"invalid trusted proxy", func(c *Config) { c.Server.TrustedProxies = []string{"127.0.0.1"} }},
		{"no whole-second TTL", func(c *Config) {
			c.Paste.MinimumTTL.Duration = 100 * time.Millisecond
			c.Paste.MaximumTTL.Duration = 500 * time.Millisecond
		}},
		{"bad limits", func(c *Config) { c.RateLimit.PerHour = 101 }},
		{"missing sqlite path", func(c *Config) { c.Database.SQLite.Path = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaults()
			cfg.Server.PublicURL = "https://example.test"
			cfg.Database.Driver = "sqlite"
			cfg.Database.SQLite.Path = "paste.db"
			tt.edit(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
