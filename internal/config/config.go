package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Checkin  CheckinConfig  `yaml:"checkin"`
	Patrol   PatrolConfig   `yaml:"patrol"`
	Notify   NotifyConfig   `yaml:"notify"`
	Sub2API  Sub2APIConfig  `yaml:"sub2api"`
	Store    StoreConfig    `yaml:"store"`
	Security SecurityConfig `yaml:"security"`
}

type ServerConfig struct {
	Addr     string `yaml:"addr"`
	BasePath string `yaml:"base_path"`
}

type CheckinConfig struct {
	Enabled      bool    `yaml:"enabled"`
	RewardAmount float64 `yaml:"reward_amount"`
	Timezone     string  `yaml:"timezone"`
	NotesPrefix  string  `yaml:"notes_prefix"`
}

type Sub2APIConfig struct {
	BaseURL        string `yaml:"base_url"`
	AdminToken     string `yaml:"admin_token"`
	// PublicHost optional external host when BaseURL is an internal docker name.
	PublicHost     string `yaml:"public_host"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}


type PatrolConfig struct {
	// Enabled controls whether the cron scheduler is active. Default false.
	Enabled bool `yaml:"enabled"`
	// Cron is a 5-field expression (min hour dom month dow). Default: every 6 hours.
	Cron string `yaml:"cron"`
	// Groups are Sub2API account groups to inspect. Empty means no accounts.
	Groups []string `yaml:"groups"`
	// TestModel is the fixed model id to probe. Empty falls back to account model_mapping.
	TestModel string `yaml:"test_model"`
	// Concurrency is account-level worker count.
	Concurrency int `yaml:"concurrency"`
	// TimeoutMs is idle timeout per model test stream.
	TimeoutMs int `yaml:"timeout_ms"`
	// ActionOnFail: disable | delete | none
	ActionOnFail string `yaml:"action_on_fail"`
	// OnlySchedulable skips accounts already off the scheduler.
	OnlySchedulable bool `yaml:"only_schedulable"`
	// StopOnFirstModelFailure stops testing more models for one account after first fail.
	StopOnFirstModelFailure bool `yaml:"stop_on_first_model_failure"`
	// Prompt used by account test API.
	Prompt string `yaml:"prompt"`
	// AutoEnableOnSuccess re-enables schedulable when all models pass.
	AutoEnableOnSuccess bool `yaml:"auto_enable_on_success"`
	// Timezone used when listing accounts (and cron evaluation location).
	Timezone string `yaml:"timezone"`
	// KeepRuns is how many historical run summaries to retain.
	KeepRuns int `yaml:"keep_runs"`
	// FailThreshold is how many consecutive failed runs an account must hit
	// before ActionOnFail is applied. 1 keeps the legacy behaviour.
	FailThreshold int `yaml:"fail_threshold"`
}

// NotifyConfig is the startup default for the notification module.
// Values changed from the admin page are stored in SQLite and win.
type NotifyConfig struct {
	// Enabled turns outbound notifications on.
	Enabled bool `yaml:"enabled"`
	// Channel: webhook | wecom | telegram
	Channel string `yaml:"channel"`
	// Target is the webhook URL, or the bot token for telegram.
	Target string `yaml:"target"`
	// Extra is channel specific (telegram chat id).
	Extra string `yaml:"extra"`
	// Secret is sent as an Authorization bearer token when set.
	Secret string `yaml:"secret"`
	// Events to subscribe; empty means all.
	Events []string `yaml:"events"`
	// MinLevel: info | warn | error
	MinLevel string `yaml:"min_level"`
}

type StoreConfig struct {
	SQLitePath string `yaml:"sqlite_path"`
}

type SecurityConfig struct {
	// CORSOrigins allowed browser origins; empty uses defaults.
	CORSOrigins []string `yaml:"cors_origins"`
	// FrameAncestors for CSP; empty uses CORSOrigins.
	FrameAncestors []string `yaml:"frame_ancestors"`
	// Rate limits per minute.
	RateCheckinPerMin    int `yaml:"rate_checkin_per_min"`
	RateStatusPerMin     int `yaml:"rate_status_per_min"`
	RateAdminWritePerMin int `yaml:"rate_admin_write_per_min"`
	RateAdminReadPerMin  int `yaml:"rate_admin_read_per_min"`
	// SensitiveWriteRequireAPIKey requires server admin credential for high-risk writes.
	SensitiveWriteRequireAPIKey bool `yaml:"sensitive_write_require_api_key"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{
			Addr:     ":8090",
			BasePath: "",
		},
		Checkin: CheckinConfig{
			Enabled:      true,
			RewardAmount: 0.1,
			Timezone:     "Asia/Shanghai",
			NotesPrefix:  "daily-checkin",
		},
		Sub2API: Sub2APIConfig{
			BaseURL:        "http://127.0.0.1:8080",
			TimeoutSeconds: 15,
		},
		Patrol: PatrolConfig{
			Enabled:                 false,
			Cron:                    "0 */6 * * *",
			Groups:                  []string{},
			TestModel:               "gpt-5.4",
			Concurrency:            8,
			TimeoutMs:               45000,
			ActionOnFail:            "disable",
			OnlySchedulable:         false,
			StopOnFirstModelFailure: true,
			Prompt:                  "hi",
			AutoEnableOnSuccess:     true,
			Timezone:                "Asia/Shanghai",
			KeepRuns:                50,
			FailThreshold:           1,
		},
		Store: StoreConfig{
			SQLitePath: "./data/checkin.db",
		},
		Security: SecurityConfig{
			CORSOrigins:    []string{},
			FrameAncestors: []string{},
			RateCheckinPerMin:           10,
			RateStatusPerMin:            60,
			RateAdminWritePerMin:        30,
			RateAdminReadPerMin:         60,
			SensitiveWriteRequireAPIKey: true,
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = envOr("CONFIG_PATH", "configs/config.yaml")
	}
	if raw, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("SERVER_BASE_PATH"); v != "" {
		cfg.Server.BasePath = v
	}
	if v := os.Getenv("CHECKIN_ENABLED"); v != "" {
		cfg.Checkin.Enabled = parseBool(v, cfg.Checkin.Enabled)
	}
	if v := os.Getenv("CHECKIN_REWARD_AMOUNT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Checkin.RewardAmount = f
		}
	}
	if v := os.Getenv("CHECKIN_TIMEZONE"); v != "" {
		cfg.Checkin.Timezone = v
	}
	if v := os.Getenv("CHECKIN_NOTES_PREFIX"); v != "" {
		cfg.Checkin.NotesPrefix = v
	}
	if v := os.Getenv("SUB2API_BASE_URL"); v != "" {
		cfg.Sub2API.BaseURL = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("SUB2API_PUBLIC_HOST"); v != "" {
		cfg.Sub2API.PublicHost = strings.TrimSpace(v)
	}
	if v := os.Getenv("SUB2API_ADMIN_TOKEN"); v != "" {
		cfg.Sub2API.AdminToken = v
	}
	if v := os.Getenv("SUB2API_ADMIN_API_KEY"); v != "" {
		cfg.Sub2API.AdminToken = v
	}
	if v := os.Getenv("SUB2API_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Sub2API.TimeoutSeconds = n
		}
	}
	if v := os.Getenv("PATROL_ENABLED"); v != "" {
		cfg.Patrol.Enabled = parseBool(v, cfg.Patrol.Enabled)
	}
	if v := os.Getenv("PATROL_CRON"); v != "" {
		cfg.Patrol.Cron = strings.TrimSpace(v)
	}
	if v := os.Getenv("PATROL_GROUPS"); v != "" {
		cfg.Patrol.Groups = splitCSV(v)
	}
	if v := os.Getenv("PATROL_TEST_MODEL"); v != "" {
		cfg.Patrol.TestModel = strings.TrimSpace(v)
	}
	if v := os.Getenv("PATROL_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Patrol.Concurrency = n
		}
	}
	if v := os.Getenv("PATROL_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Patrol.TimeoutMs = n
		}
	}
	if v := os.Getenv("PATROL_ACTION_ON_FAIL"); v != "" {
		cfg.Patrol.ActionOnFail = strings.TrimSpace(v)
	}
	if v := os.Getenv("PATROL_ONLY_SCHEDULABLE"); v != "" {
		cfg.Patrol.OnlySchedulable = parseBool(v, cfg.Patrol.OnlySchedulable)
	}
	if v := os.Getenv("PATROL_STOP_ON_FIRST_FAILURE"); v != "" {
		cfg.Patrol.StopOnFirstModelFailure = parseBool(v, cfg.Patrol.StopOnFirstModelFailure)
	}
	if v := os.Getenv("PATROL_PROMPT"); v != "" {
		cfg.Patrol.Prompt = v
	}
	if v := os.Getenv("PATROL_AUTO_ENABLE_ON_SUCCESS"); v != "" {
		cfg.Patrol.AutoEnableOnSuccess = parseBool(v, cfg.Patrol.AutoEnableOnSuccess)
	}
	if v := os.Getenv("PATROL_TIMEZONE"); v != "" {
		cfg.Patrol.Timezone = v
	}
	if v := os.Getenv("PATROL_KEEP_RUNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Patrol.KeepRuns = n
		}
	}
	if v := os.Getenv("PATROL_FAIL_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Patrol.FailThreshold = n
		}
	}
	if v := os.Getenv("NOTIFY_ENABLED"); v != "" {
		cfg.Notify.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("NOTIFY_CHANNEL"); v != "" {
		cfg.Notify.Channel = v
	}
	if v := os.Getenv("NOTIFY_TARGET"); v != "" {
		cfg.Notify.Target = v
	}
	if v := os.Getenv("NOTIFY_EXTRA"); v != "" {
		cfg.Notify.Extra = v
	}
	if v := os.Getenv("NOTIFY_SECRET"); v != "" {
		cfg.Notify.Secret = v
	}
	if v := os.Getenv("NOTIFY_MIN_LEVEL"); v != "" {
		cfg.Notify.MinLevel = v
	}
	if v := os.Getenv("NOTIFY_EVENTS"); v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		cfg.Notify.Events = out
	}
	if v := os.Getenv("SQLITE_PATH"); v != "" {
		cfg.Store.SQLitePath = v
	}
	if v := os.Getenv("CORS_ORIGINS"); v != "" {
		cfg.Security.CORSOrigins = splitCSV(v)
	}
	if v := os.Getenv("FRAME_ANCESTORS"); v != "" {
		cfg.Security.FrameAncestors = splitCSV(v)
	}
	if v := os.Getenv("RATE_CHECKIN_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Security.RateCheckinPerMin = n
		}
	}
	if v := os.Getenv("RATE_STATUS_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Security.RateStatusPerMin = n
		}
	}
	if v := os.Getenv("RATE_ADMIN_WRITE_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Security.RateAdminWritePerMin = n
		}
	}
	if v := os.Getenv("RATE_ADMIN_READ_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Security.RateAdminReadPerMin = n
		}
	}
	if v := os.Getenv("SENSITIVE_WRITE_REQUIRE_API_KEY"); v != "" {
		cfg.Security.SensitiveWriteRequireAPIKey = parseBool(v, cfg.Security.SensitiveWriteRequireAPIKey)
	}

	cfg.Sub2API.BaseURL = strings.TrimRight(cfg.Sub2API.BaseURL, "/")
	cfg.Server.BasePath = normalizeBasePath(cfg.Server.BasePath)
	if len(cfg.Security.CORSOrigins) == 0 {
		cfg.Security.CORSOrigins = Default().Security.CORSOrigins
	}
	if len(cfg.Security.FrameAncestors) == 0 {
		cfg.Security.FrameAncestors = append([]string{}, cfg.Security.CORSOrigins...)
	}
	if cfg.Security.RateCheckinPerMin <= 0 {
		cfg.Security.RateCheckinPerMin = 10
	}
	if cfg.Security.RateStatusPerMin <= 0 {
		cfg.Security.RateStatusPerMin = 60
	}
	if cfg.Security.RateAdminWritePerMin <= 0 {
		cfg.Security.RateAdminWritePerMin = 30
	}
	if cfg.Security.RateAdminReadPerMin <= 0 {
		cfg.Security.RateAdminReadPerMin = 60
	}

	// patrol defaults / clamps
	if strings.TrimSpace(cfg.Patrol.Cron) == "" {
		cfg.Patrol.Cron = "0 */6 * * *"
	}
	if cfg.Patrol.Concurrency <= 0 {
		cfg.Patrol.Concurrency = 8
	}
	if cfg.Patrol.Concurrency > 50 {
		cfg.Patrol.Concurrency = 50
	}
	if cfg.Patrol.TimeoutMs < 1000 {
		cfg.Patrol.TimeoutMs = 45000
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Patrol.ActionOnFail)) {
	case "disable", "delete", "none":
		cfg.Patrol.ActionOnFail = strings.ToLower(strings.TrimSpace(cfg.Patrol.ActionOnFail))
	default:
		cfg.Patrol.ActionOnFail = "disable"
	}
	if strings.TrimSpace(cfg.Patrol.Prompt) == "" {
		cfg.Patrol.Prompt = "hi"
	}
	if strings.TrimSpace(cfg.Patrol.Timezone) == "" {
		cfg.Patrol.Timezone = cfg.Checkin.Timezone
	}
	if cfg.Patrol.KeepRuns <= 0 {
		cfg.Patrol.KeepRuns = 50
	}
	if cfg.Patrol.FailThreshold <= 0 {
		cfg.Patrol.FailThreshold = 1
	}
	if strings.TrimSpace(cfg.Notify.Channel) == "" {
		cfg.Notify.Channel = "webhook"
	}
	if strings.TrimSpace(cfg.Notify.MinLevel) == "" {
		cfg.Notify.MinLevel = "warn"
	}
	if cfg.Patrol.FailThreshold > 10 {
		cfg.Patrol.FailThreshold = 10
	}
}


func (c Config) Validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr is required")
	}
	if c.Checkin.RewardAmount <= 0 {
		return fmt.Errorf("checkin.reward_amount must be > 0")
	}
	if _, err := time.LoadLocation(c.Checkin.Timezone); err != nil {
		return fmt.Errorf("invalid checkin.timezone: %w", err)
	}
	if c.Patrol.Timezone != "" {
		if _, err := time.LoadLocation(c.Patrol.Timezone); err != nil {
			return fmt.Errorf("invalid patrol.timezone: %w", err)
		}
	}
	if c.Sub2API.BaseURL == "" {
		return fmt.Errorf("sub2api.base_url is required")
	}
	if c.Store.SQLitePath == "" {
		return fmt.Errorf("store.sqlite_path is required")
	}
	if c.Sub2API.TimeoutSeconds <= 0 {
		return fmt.Errorf("sub2api.timeout_seconds must be > 0")
	}
	return nil
}

func (c Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Checkin.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (c Config) Timeout() time.Duration {
	return time.Duration(c.Sub2API.TimeoutSeconds) * time.Second
}

// OriginAllowed returns whether browser Origin is permitted.
func (c Config) OriginAllowed(origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return true // non-browser / same-origin without Origin header
	}
	for _, o := range c.Security.CORSOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if strings.EqualFold(o, origin) {
			return true
		}
		// allow localhost any port for dev
		if (strings.HasPrefix(origin, "http://127.0.0.1:") || strings.HasPrefix(origin, "http://localhost:")) &&
			(o == "http://127.0.0.1" || o == "http://localhost" || strings.Contains(o, "127.0.0.1") || strings.Contains(o, "localhost")) {
			return true
		}
	}
	// always allow local loopback for admin tooling
	if strings.HasPrefix(origin, "http://127.0.0.1:") || strings.HasPrefix(origin, "http://localhost:") {
		return true
	}
	return false
}

func (c Config) CSPFrameAncestors() string {
	parts := []string{"'self'"}
	for _, a := range c.Security.FrameAncestors {
		a = strings.TrimSpace(a)
		if a != "" {
			parts = append(parts, a)
		}
	}
	return "frame-ancestors " + strings.Join(parts, " ")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func normalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
