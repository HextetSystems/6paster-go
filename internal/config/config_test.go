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
