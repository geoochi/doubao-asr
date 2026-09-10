package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultBatchURL = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_nostream"

// Config holds everything the daemon needs. Values come from the process
// environment first, then from a .env file.
type Config struct {
	APIKey      string
	ResourceID  string
	URL         string
	Device      string
	SampleRate  int
	SegmentMS   int
	MaxDuration time.Duration
	Mode        string // "type" (wtype) or "clipboard" (wl-copy)
	Notify      bool
}

// envFilePath resolves which .env file to use, in priority order:
//
//	$DOUBAO_DICTATE_ENV, ~/.config/doubao-dictate/.env, ./.env
func envFilePath() (string, bool) {
	if p := os.Getenv("DOUBAO_DICTATE_ENV"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		p := filepath.Join(dir, appName, ".env")
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	if _, err := os.Stat(".env"); err == nil {
		abs, _ := filepath.Abs(".env")
		return abs, true
	}
	return "", false
}

// readEnvFile parses a KEY=VALUE file. Blank lines and # comments are ignored,
// an optional `export ` prefix and surrounding quotes are stripped.
func readEnvFile(path string) (map[string]string, error) {
	values := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return values, sc.Err()
}

// loadConfig reads the .env file (if any) and applies environment overrides.
func loadConfig() (*Config, error) {
	file := map[string]string{}
	path, found := envFilePath()
	if found {
		var err error
		if file, err = readEnvFile(path); err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		logger.Info("loaded config", "path", path)
	} else {
		logger.Warn("no .env found; relying on environment variables")
	}

	get := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		if v := file[key]; v != "" {
			return v
		}
		return def
	}
	getInt := func(key string, def int) int {
		v := get(key, "")
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			logger.Warn("invalid integer, using default", "key", key, "value", v, "default", def)
			return def
		}
		return n
	}
	getBool := func(key string, def bool) bool {
		v := strings.ToLower(get(key, ""))
		switch v {
		case "":
			return def
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	}

	cfg := &Config{
		APIKey:      get("DOUBAO_API_KEY", ""),
		ResourceID:  get("DOUBAO_RESOURCE_ID", "volc.seedasr.sauc.duration"),
		URL:         get("DOUBAO_URL", defaultBatchURL),
		Device:      get("DOUBAO_DEVICE", "default"),
		SampleRate:  getInt("DOUBAO_SAMPLE_RATE", 16000),
		SegmentMS:   getInt("DOUBAO_SEGMENT_MS", 200),
		MaxDuration: time.Duration(getInt("DOUBAO_MAX_DURATION_SECS", 120)) * time.Second,
		Mode:        get("DOUBAO_MODE", "type"),
		Notify:      getBool("DOUBAO_NOTIFY", true),
	}
	return cfg, nil
}

// segmentBytes is the PCM chunk size sent per audio frame.
func (c *Config) segmentBytes() int {
	return 2 * c.SampleRate * c.SegmentMS / 1000 // s16le mono
}
