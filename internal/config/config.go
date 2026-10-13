// Package config loads and persists the panel configuration (config/config.yaml).
//
// The layout is intentionally flat: every setting is a first-class field with a
// snake_case YAML/JSON key, editable live through the admin API (PATCH /panel/config).
package config

import (
        "bytes"
        "encoding/json"
        "fmt"
        "os"
        "path/filepath"
        "sync"

        "gopkg.in/yaml.v3"
)

// Config is the complete panel configuration.
type Config struct {
        HTTPPort  int    `yaml:"https_port" json:"https_port"`
        HTTPHost   string `yaml:"http_host" json:"http_host"`
        BaseURL    string `yaml:"base_url" json:"base_url"`
        Language   string `yaml:"language" json:"language"`

        GRPCEnabled bool `yaml:"grpc_enabled" json:"grpc_enabled"`
        GRPCPort    int  `yaml:"grpc_port" json:"grpc_port"`

        CookieExpire     int `yaml:"cookie_expire" json:"cookie_expire"`             // days
        MaxLoginAttempts int `yaml:"max_login_attempts" json:"max_login_attempts"` // per-user before lockout

        StatsUpdateFrequencySeconds int `yaml:"stats_update_frequency_seconds" json:"stats_update_frequency_seconds"`
        HistoryMaxAge               int `yaml:"history_max_age" json:"history_max_age"`                       // days of stats history kept
        MaxLogLines                 int `yaml:"max_log_lines" json:"max_log_lines"`                           // ring buffer per log stream
        PanelLogsDeleteAfterDays    int `yaml:"panel_logs_delete_after_days" json:"panel_logs_delete_after_days"` // 0 = keep forever
        DirSizePollFreqMinutes      int `yaml:"dir_size_poll_freq_minutes" json:"dir_size_poll_freq_minutes"`

        MonitoredMounts []string `yaml:"monitored_mounts" json:"monitored_mounts"`
        JavaExtraPaths  []string `yaml:"java_extra_paths" json:"java_extra_paths"`

        EnableOTPSkew        bool `yaml:"enable_otp_skew" json:"enable_otp_skew"`
        SuperMFA             bool `yaml:"super_mfa" json:"super_mfa"`                             // require TOTP for superusers
        GeneralUserLogAccess bool `yaml:"general_user_log_access" json:"general_user_log_access"` // non-admins may read panel logs
        ResetSecretsOnNextBoot bool `yaml:"reset_secrets_on_next_boot" json:"reset_secrets_on_next_boot"`
        Experimental           bool `yaml:"experimental" json:"experimental"`
        EnableUserSelfDelete   bool `yaml:"enable_user_self_delete" json:"enable_user_self_delete"`
        ShowErrors             bool `yaml:"show_errors" json:"show_errors"`
        ShowContributeLink     bool `yaml:"show_contribute_link" json:"show_contribute_link"`

        MaxImageUploadSizeMB int `yaml:"max_image_upload_size_mb" json:"max_image_upload_size_mb"`

        BigBucketRepo   string `yaml:"big_bucket_repo" json:"big_bucket_repo"`     // server download manifest source
        DockerSteamImage string `yaml:"docker_steam_image" json:"docker_steam_image"` // image used to run Windows-only Steam games
        ClusterToken     string `yaml:"cluster_token" json:"cluster_token"`         // shared token for panel/agent pairing

        VirtualTerminalLines int            `yaml:"virtual_terminal_lines" json:"virtual_terminal_lines"` // console scrollback buffer
        Keywords            []string      `yaml:"terminal_keywords" json:"terminal_keywords"`           // console keyword highlighting
        SamplingTiers       []SamplingTier `yaml:"sampling_tiers" json:"sampling_tiers"`
        SamplingFallbackDivisor int       `yaml:"sampling_fallback_divisor" json:"sampling_fallback_divisor"`

        DisabledLanguageFiles []string `yaml:"disabled_language_files" json:"disabled_language_files"`

        AllowNSFWProfilePictures bool `yaml:"allow_nsfw_profile_pictures" json:"allow_nsfw_profile_pictures"`
}

// SamplingTier controls history downsampling for a max window.
type SamplingTier struct {
        MaxHours   int `yaml:"max_hours" json:"max_hours"`
        SampleRate int `yaml:"sample_rate" json:"sample_rate"` // keep every Nth point
}

// Categories groups config keys for the admin UI (GET /panel/config).
var Categories = map[string][]string{
        "general": {
                "https_port", "http_host", "base_url", "language", "show_errors", "show_contribute_link",
                "enable_user_self_delete", "experimental", "max_image_upload_size_mb",
                "allow_nsfw_profile_pictures", "disabled_language_files",
        },
        "security": {
                "cookie_expire", "max_login_attempts", "enable_otp_skew", "super_mfa",
                "reset_secrets_on_next_boot",
        },
        "logs": {
                "max_log_lines", "panel_logs_delete_after_days", "virtual_terminal_lines", "terminal_keywords",
        },
        "monitoring": {
                "stats_update_frequency_seconds", "history_max_age", "sampling_tiers",
                "sampling_fallback_divisor", "dir_size_poll_freq_minutes", "monitored_mounts",
        },
        "miscellaneous": {
                "java_extra_paths", "big_bucket_repo", "docker_steam_image", "general_user_log_access",
        },
        "server": {
                "cluster_token",
        },
}

// Default builds the stock configuration used on first run.
func Default() *Config {
        return &Config{
                HTTPPort: 8443,
                HTTPHost:  "0.0.0.0",
                BaseURL:    "http://localhost:8443",
                Language:   "en",

                GRPCEnabled: false,
                GRPCPort:    5055,

                CookieExpire:     7,
                MaxLoginAttempts: 3,

                StatsUpdateFrequencySeconds: 10,
                HistoryMaxAge:               7,
                MaxLogLines:                 1000,
                DirSizePollFreqMinutes:       5,

                MonitoredMounts: []string{"/"},
                JavaExtraPaths:  []string{},

                EnableOTPSkew:        true,
                ShowErrors:           true,
                ShowContributeLink:   true,
                MaxImageUploadSizeMB: 5,

                BigBucketRepo:   "https://jars.arcadiatech.org",
                DockerSteamImage: "steamcmd/steamcmd:latest",

                VirtualTerminalLines: 200,
                Keywords:            []string{"ERROR", "WARN", "FATAL", "SEVERE"},
                SamplingTiers: []SamplingTier{
                        {MaxHours: 6, SampleRate: 1},
                        {MaxHours: 24, SampleRate: 2},
                        {MaxHours: 72, SampleRate: 6},
                },
                SamplingFallbackDivisor: 12,

                DisabledLanguageFiles: []string{},
        }
}

// Manager owns the live configuration and persists changes back to disk.
type Manager struct {
        mu   sync.RWMutex
        cfg  *Config
        path string
}

// Load reads config.yaml at path, creating it with defaults when missing.
func Load(path string) (*Manager, error) {
        cfg := Default()
        raw, err := os.ReadFile(path)
        switch {
        case err == nil:
                if err := yaml.Unmarshal(raw, cfg); err != nil {
                        return nil, fmt.Errorf("parse %s: %w", path, err)
                }
        case os.IsNotExist(err):
                if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
                        return nil, err
                }
                if out, yerr := yaml.Marshal(cfg); yerr == nil {
                        _ = os.WriteFile(path, out, 0o600)
                }
        default:
                return nil, err
        }
        if err := cfg.sanitize(); err != nil {
                return nil, err
        }
        return &Manager{cfg: cfg, path: path}, nil
}

// NewManager builds a manager around an already-loaded config (tests).
func NewManager(cfg *Config, path string) *Manager { return &Manager{cfg: cfg, path: path} }

// Set replaces the live configuration and persists it (CLI overrides).
func (m *Manager) Set(c *Config) {
        if c == nil {
                return
        }
        _ = c.sanitize()
        m.mu.Lock()
        m.cfg = c
        if m.path != "" {
                if out, yerr := yaml.Marshal(c); yerr == nil {
                        _ = os.WriteFile(m.path, out, 0o600)
                }
        }
        m.mu.Unlock()
}

// Get returns the live configuration. Treat it as read-only; use Patch to change it.
func (m *Manager) Get() *Config {
        m.mu.RLock()
        defer m.mu.RUnlock()
        return m.cfg
}

// Patch applies a partial update expressed as a JSON object (snake_case keys,
// same wire format as the API). Unknown keys or wrong types are rejected.
// On success the configuration is persisted to the YAML file it was loaded from.
func (m *Manager) Patch(patch map[string]interface{}) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        // current config as JSON, overlay the patch, decode strictly.
        base, err := json.Marshal(m.cfg)
        if err != nil {
                return err
        }
        overlay, err := json.Marshal(patch)
        if err != nil {
                return err
        }
        merged, err := mergeJSON(base, overlay)
        if err != nil {
                return err
        }
        next := Default()
        dec := json.NewDecoder(bytes.NewReader(merged))
        dec.DisallowUnknownFields()
        if err := dec.Decode(next); err != nil {
                return fmt.Errorf("invalid config patch: %w", err)
        }
        if err := next.sanitize(); err != nil {
                return err
        }
        m.cfg = next
        if m.path != "" {
                if out, yerr := yaml.Marshal(next); yerr == nil {
                        _ = os.WriteFile(m.path, out, 0o600)
                }
        }
        return nil
}

// mergeJSON deep-merges b over a (objects only; arrays and scalars replace).
func mergeJSON(a, b []byte) ([]byte, error) {
        var ao, bo map[string]interface{}
        if err := json.Unmarshal(a, &ao); err != nil {
                return nil, err
        }
        if err := json.Unmarshal(b, &bo); err != nil {
                return nil, err
        }
        deepMerge(ao, bo)
        return json.Marshal(ao)
}

func deepMerge(dst, src map[string]interface{}) {
        for k, v := range src {
                if sub, ok := v.(map[string]interface{}); ok {
                        if cur, ok := dst[k].(map[string]interface{}); ok {
                                deepMerge(cur, sub)
                                continue
                        }
                }
                dst[k] = v
        }
}

// sanitize clamps values into safe ranges after load/patch.
func (c *Config) sanitize() error {
        if c.HTTPPort <= 0 || c.HTTPPort > 65535 {
                return fmt.Errorf("https_port out of range (1-65535)")
        }
        if c.HTTPHost == "" {
                c.HTTPHost = "0.0.0.0"
        }
        if c.CookieExpire <= 0 {
                c.CookieExpire = 7
        }
        if c.MaxLoginAttempts <= 0 {
                c.MaxLoginAttempts = 3
        }
        if c.StatsUpdateFrequencySeconds < 1 {
                c.StatsUpdateFrequencySeconds = 10
        }
        if c.HistoryMaxAge <= 0 {
                c.HistoryMaxAge = 7
        }
        if c.MaxLogLines < 50 {
                c.MaxLogLines = 50
        }
        if c.DirSizePollFreqMinutes <= 0 {
                c.DirSizePollFreqMinutes = 5
        }
        if len(c.MonitoredMounts) == 0 {
                c.MonitoredMounts = []string{"/"}
        }
        if c.MaxImageUploadSizeMB <= 0 {
                c.MaxImageUploadSizeMB = 5
        }
        if c.Language == "" {
                c.Language = "en"
        }
        if c.BigBucketRepo == "" {
                c.BigBucketRepo = "https://jars.arcadiatech.org"
        }
        if c.DockerSteamImage == "" {
                c.DockerSteamImage = "steamcmd/steamcmd:latest"
        }
        if c.DisabledLanguageFiles == nil {
                c.DisabledLanguageFiles = []string{}
        }
        if c.JavaExtraPaths == nil {
                c.JavaExtraPaths = []string{}
        }
        if c.Keywords == nil {
                c.Keywords = []string{"ERROR", "WARN", "FATAL", "SEVERE"}
        }
        if c.VirtualTerminalLines < 10 {
                c.VirtualTerminalLines = 200
        }
        if len(c.SamplingTiers) == 0 {
                c.SamplingTiers = []SamplingTier{{MaxHours: 6, SampleRate: 1}, {MaxHours: 24, SampleRate: 2}, {MaxHours: 72, SampleRate: 6}}
        }
        if c.SamplingFallbackDivisor <= 0 {
                c.SamplingFallbackDivisor = 12
        }
        return nil
}
