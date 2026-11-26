// Package config reads Plumb's settings from environment variables.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Service is one public app shown on the status page.
type Service struct {
	Name string
	URL  string
}

// Config holds every setting Plumb needs.
type Config struct {
	DiscordToken   string
	GuildID        string
	AlertChannelID string
	PlatformRoleID string
	PingOnCritical bool

	AlertmanagerURL string
	MetricsURL      string
	GrafanaURL      string

	// Internal listener: Alertmanager webhook and health checks. Never exposed publicly.
	ListenAddr   string
	WebhookToken string
	// WebhookTokenGenerated is true when no token was set and Plumb made a
	// random one, so the webhook rejects every call until one is set.
	WebhookTokenGenerated bool

	// Public listener: only the status page.
	StatusAddr     string
	StatusTitle    string
	StatusServices []Service
	StatusInterval time.Duration
	StatusSlow     time.Duration
	StatusHost     string // shown in the page's title bar
	NoticesFile    string // git-managed notices (ConfigMap), optional
	Discovery      bool   // list opted-in IngressRoutes
	NoticeChannel  string // Discord channel for /notice; empty = alert channel
	TimeZone       string
}

// FromEnv loads the config. Secrets may be given directly or as *_FILE paths.
func FromEnv() (Config, error) {
	c := Config{
		GuildID:         os.Getenv("PLUMB_GUILD_ID"),
		AlertChannelID:  os.Getenv("PLUMB_ALERT_CHANNEL_ID"),
		PlatformRoleID:  os.Getenv("PLUMB_PLATFORM_ROLE_ID"),
		AlertmanagerURL: env("PLUMB_ALERTMANAGER_URL", "http://alertmanager.operations.svc.cluster.local:9093"),
		MetricsURL:      env("PLUMB_METRICS_URL", "http://victoriametrics.platform.svc.cluster.local:8428"),
		GrafanaURL:      os.Getenv("PLUMB_GRAFANA_URL"),
		ListenAddr:      env("PLUMB_LISTEN_ADDR", ":8080"),
		StatusAddr:      env("PLUMB_STATUS_ADDR", ":8081"),
		StatusTitle:     env("PLUMB_STATUS_TITLE", "BitsAndBricks status"),
		StatusHost:      os.Getenv("PLUMB_STATUS_HOST"),
		NoticesFile:     os.Getenv("PLUMB_NOTICES_FILE"),
		NoticeChannel:   os.Getenv("PLUMB_NOTICE_CHANNEL_ID"),
		TimeZone:        env("PLUMB_TIMEZONE", "Europe/Zurich"),
	}
	var err error
	if c.DiscordToken, err = secret("PLUMB_DISCORD_TOKEN"); err != nil {
		return c, err
	}
	if c.WebhookToken, err = secret("PLUMB_WEBHOOK_TOKEN"); err != nil {
		return c, err
	}
	if c.WebhookToken == "" {
		// Closed by default: nobody knows this token, not even Alertmanager.
		b := make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return c, fmt.Errorf("generating a webhook token: %w", err)
		}
		c.WebhookToken, c.WebhookTokenGenerated = hex.EncodeToString(b), true
	}
	if c.PingOnCritical, err = strconv.ParseBool(env("PLUMB_PING_ON_CRITICAL", "true")); err != nil {
		return c, fmt.Errorf("PLUMB_PING_ON_CRITICAL: %w", err)
	}
	if c.StatusInterval, err = time.ParseDuration(env("PLUMB_STATUS_INTERVAL", "60s")); err != nil {
		return c, fmt.Errorf("PLUMB_STATUS_INTERVAL: %w", err)
	}
	if c.StatusSlow, err = time.ParseDuration(env("PLUMB_STATUS_SLOW", "2s")); err != nil {
		return c, fmt.Errorf("PLUMB_STATUS_SLOW: %w", err)
	}
	if c.Discovery, err = strconv.ParseBool(env("PLUMB_STATUS_DISCOVERY", "false")); err != nil {
		return c, fmt.Errorf("PLUMB_STATUS_DISCOVERY: %w", err)
	}
	if _, err = time.LoadLocation(c.TimeZone); err != nil {
		return c, fmt.Errorf("PLUMB_TIMEZONE: %w", err)
	}
	if c.StatusServices, err = ParseServices(os.Getenv("PLUMB_STATUS_SERVICES")); err != nil {
		return c, err
	}

	var missing []string
	for k, v := range map[string]string{
		"PLUMB_DISCORD_TOKEN":    c.DiscordToken,
		"PLUMB_GUILD_ID":         c.GuildID,
		"PLUMB_ALERT_CHANNEL_ID": c.AlertChannelID,
		"PLUMB_PLATFORM_ROLE_ID": c.PlatformRoleID,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

// ParseServices parses "Name=https://url,Other=https://url2".
func ParseServices(s string) ([]Service, error) {
	var out []Service
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, url, ok := strings.Cut(part, "=")
		name, url = strings.TrimSpace(name), strings.TrimSpace(url)
		if !ok || name == "" || !(strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) {
			return nil, fmt.Errorf("PLUMB_STATUS_SERVICES: bad entry %q, want Name=https://url", part)
		}
		out = append(out, Service{Name: name, URL: url})
	}
	return out, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func secret(k string) (string, error) {
	if v := os.Getenv(k); v != "" {
		return v, nil
	}
	if p := os.Getenv(k + "_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", k, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", nil
}
