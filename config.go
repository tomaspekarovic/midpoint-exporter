package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Port            int
	Interval        time.Duration
	BaseURL         string
	TasksSuffix     string
	ResourcesSuffix string
	Username        string
	Password        string
	AppLabel        string
	JobPrefix       string
	LogLevel        string
}

func loadConfig() (Config, error) {
	cfg := Config{
		Port:            mustAtoi(getenv("PORT", "9993"), 9993),
		Interval:        time.Duration(mustAtoi(getenv("SCRAPE_INTERVAL", "30"), 30)) * time.Second,
		BaseURL:         getenv("MIDPOINT_BASE_URL", ""),
		TasksSuffix:     getenv("MIDPOINT_TASKS_URL", "/ws/rest/tasks"),
		ResourcesSuffix: getenv("MIDPOINT_RESOURCES_URL", "/ws/rest/resources"),
		Username:        getenv("MIDPOINT_USER", ""),
		Password:        getenv("MIDPOINT_PASSWORD", ""),
		AppLabel:        getenv("APP_LABEL", "mp"),
		JobPrefix:       getenv("JOB_LABEL_PREFIX", "midpoint"),
		LogLevel:        strings.ToUpper(getenv("LOG_LEVEL", "INFO")),
	}
	if cfg.BaseURL == "" {
		return cfg, errors.New("missing required env var: MIDPOINT_BASE_URL")
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustAtoi(s string, def int) int {
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return def
	}
	return v
}

func composeURL(base, suffix string) string {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	clean := strings.TrimLeft(suffix, "/")
	return base + clean
}

func (c Config) TasksURL() string     { return composeURL(c.BaseURL, c.TasksSuffix) }
func (c Config) ResourcesURL() string { return composeURL(c.BaseURL, c.ResourcesSuffix) }

func hostLabelFromBaseURL(base string) string {
	u, _ := url.Parse(base)
	host := "unknown"
	if u != nil {
		hostPart := u.Host
		path := strings.TrimSuffix(u.Path, "/")
		if hostPart != "" {
			host = hostPart + path
		}
	}
	return host
}
