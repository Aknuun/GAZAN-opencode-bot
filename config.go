package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Token     string
	BaseURL   string
	Agent     string
	StateFile string
	Allowed   map[int64]bool
	PollMs    int

	OCConfigHome string // پوشه کانفیگ opencode (شامل opencode.json[c])
	OCDataHome   string // پوشه دیتای opencode (شامل auth.json)
	OCService    string // نام سرویس systemd سرور opencode
}

func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func loadConfig() (*Config, error) {
	loadEnvFile(".env")
	loadEnvFile("/etc/negahban-opencode.env")

	cfg := &Config{
		BaseURL:   getEnv("OPENCODE_BASE_URL", "http://127.0.0.1:14999"),
		Agent:     getEnv("OPENCODE_AGENT", "build"),
		StateFile: getEnv("STATE_FILE", "state.json"),
		PollMs:    getEnvInt("TELEGRAM_POLL_MS", 60),
		Allowed:   map[int64]bool{},

		OCConfigHome: getEnv("OPENCODE_CONFIG_HOME", ""),
		OCDataHome:   getEnv("OPENCODE_DATA_HOME", ""),
		OCService:    getEnv("OPENCODE_SERVE_SERVICE", ""),
	}
	cfg.Token = os.Getenv("TELEGRAM_BOT_TOKEN")
	if cfg.Token == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN ست نشده")
	}
	for _, s := range strings.Split(getEnv("ALLOWED_USER_IDS", ""), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ALLOWED_USER_IDS نامعتبر: %q", s)
		}
		cfg.Allowed[id] = true
	}
	if len(cfg.Allowed) == 0 {
		return nil, fmt.Errorf("حداقل یک ALLOWED_USER_IDS (شناسه تلگرام خودت) لازم است")
	}
	return cfg, nil
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getEnvInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
