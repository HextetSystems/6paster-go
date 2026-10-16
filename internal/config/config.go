package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return errors.New("duration must be a string such as 1h")
	}
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Database  DatabaseConfig  `yaml:"database"`
	Paste     PasteConfig     `yaml:"paste"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	Cleanup   CleanupConfig   `yaml:"cleanup"`
}

type ServerConfig struct {
	Listen         string   `yaml:"listen"`
	PublicURL      string   `yaml:"public_url"`
	BasePath       string   `yaml:"base_path"`
	RequireHTTPS   bool     `yaml:"require_https"`
	PageTitle      string   `yaml:"page_title"`
	TrustedProxies []string `yaml:"trusted_proxies"`
}

type DatabaseConfig struct {
	Driver string       `yaml:"driver"`
	SQLite SQLiteConfig `yaml:"sqlite"`
	MySQL  MySQLConfig  `yaml:"mysql"`
}

type SQLiteConfig struct {
	Path string `yaml:"path"`
}

type MySQLConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	TLSMode  string `yaml:"tls_mode"`
}

type PasteConfig struct {
	MinimumTTL   Duration `yaml:"minimum_ttl"`
	MaximumTTL   Duration `yaml:"maximum_ttl"`
	MaximumBytes int64    `yaml:"maximum_bytes"`
	AllowImages  bool     `yaml:"allow_images"`
}

type RateLimitConfig struct {
	PerHour int `yaml:"per_hour"`
	PerDay  int `yaml:"per_day"`
}

type CleanupConfig struct {
	Interval Duration `yaml:"interval"`
}

func defaults() Config {
	return Config{
		Server: ServerConfig{Listen: ":8080", BasePath: "/", RequireHTTPS: true, PageTitle: "6paster"},
		Database: DatabaseConfig{
			MySQL: MySQLConfig{Host: "127.0.0.1", Port: 3306, TLSMode: "disabled"},
		},
		Paste: PasteConfig{
			MinimumTTL: Duration{time.Hour}, MaximumTTL: Duration{30 * 24 * time.Hour}, MaximumBytes: 300 * 1024, AllowImages: true,
		},
		RateLimit: RateLimitConfig{PerHour: 60, PerDay: 100},
		Cleanup:   CleanupConfig{Interval: Duration{time.Hour}},
	}
}

func Load(filename string) (Config, error) {
	f, err := os.Open(filename)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", filename, err)
	}
	defer f.Close()

	cfg := defaults()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", filename, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("config must contain exactly one YAML document")
		}
		return Config{}, fmt.Errorf("decode trailing config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Server.Listen == "" {
		return errors.New("server.listen is required")
	}
	u, err := url.Parse(c.Server.PublicURL)
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("server.public_url must be an absolute http or https URL")
	}
	if u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("server.public_url must not contain a path, query, or fragment; use server.base_path")
	}
	if c.Server.RequireHTTPS && u.Scheme != "https" {
		return errors.New("server.public_url must use https when server.require_https is true")
	}
	c.Server.PublicURL = strings.TrimRight(c.Server.PublicURL, "/")
	if c.Server.BasePath == "" {
		c.Server.BasePath = "/"
	}
	if !strings.HasPrefix(c.Server.BasePath, "/") || path.Clean(c.Server.BasePath) != c.Server.BasePath {
		return errors.New("server.base_path must be / or a clean absolute path without a trailing slash")
	}
	if c.Server.PageTitle == "" {
		return errors.New("server.page_title is required")
	}
	for _, cidr := range c.Server.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("server.trusted_proxies contains invalid CIDR %q", cidr)
		}
	}
	switch c.Database.Driver {
	case "sqlite":
		if c.Database.SQLite.Path == "" {
			return errors.New("database.sqlite.path is required for sqlite")
		}
	case "mysql":
		m := c.Database.MySQL
		if m.Host == "" || m.Port <= 0 || m.Port > 65535 || m.User == "" || m.Database == "" {
			return errors.New("database.mysql host, valid port, user, and database are required")
		}
		switch m.TLSMode {
		case "disabled", "preferred", "required", "skip-verify":
		default:
			return errors.New("database.mysql.tls_mode must be disabled, preferred, required, or skip-verify")
		}
	default:
		return errors.New("database.driver must be sqlite or mysql")
	}
	if c.Paste.MinimumTTL.Duration <= 0 || c.Paste.MaximumTTL.Duration < c.Paste.MinimumTTL.Duration {
		return errors.New("paste TTLs must be positive and maximum_ttl must be at least minimum_ttl")
	}
	minimumSeconds := c.Paste.MinimumTTL.Duration / time.Second
	if c.Paste.MinimumTTL.Duration%time.Second != 0 {
		minimumSeconds++
	}
	if minimumSeconds > c.Paste.MaximumTTL.Duration/time.Second {
		return errors.New("paste TTL range must contain at least one whole second")
	}
	if c.Paste.MaximumBytes <= 0 {
		return errors.New("paste.maximum_bytes must be positive")
	}
	if c.RateLimit.PerHour <= 0 || c.RateLimit.PerDay <= 0 || c.RateLimit.PerHour > c.RateLimit.PerDay {
		return errors.New("rate limits must be positive and per_hour must not exceed per_day")
	}
	if c.Cleanup.Interval.Duration <= 0 {
		return errors.New("cleanup.interval must be positive")
	}
	return nil
}

func (c Config) PublicBaseURL() string {
	if c.Server.BasePath == "/" {
		return c.Server.PublicURL + "/"
	}
	return c.Server.PublicURL + c.Server.BasePath + "/"
}
