package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddr string    `yaml:"listen_addr"`
	BasePath   string    `yaml:"base_path"`
	TLS        TLSConfig `yaml:"tls"`
	DBPath     string    `yaml:"db_path"`
	Auth       AuthConfig `yaml:"auth"`
	LogLevel   string    `yaml:"log_level"`
}

type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	AutoCert bool   `yaml:"auto_cert"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	ACMEHost string `yaml:"acme_host"`
	CacheDir string `yaml:"cache_dir"`
}

type AuthConfig struct {
	Provider string       `yaml:"provider"`
	Basic    BasicConfig  `yaml:"basic"`
	OAuth2   OAuth2Config `yaml:"oauth2"`
}

type BasicConfig struct {
	Users map[string]UserConfig `yaml:"users"`
}

type UserConfig struct {
	Password    string `yaml:"password"`
	DisplayName string `yaml:"display_name"`
	Email       string `yaml:"email"`
}

type OAuth2Config struct {
	JWKSURL     string `yaml:"jwks_url"`
	Issuer      string `yaml:"issuer"`
	Audience    string `yaml:"audience"`
	UserIDClaim string `yaml:"user_id_claim"`
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		ListenAddr: ":8080",
		BasePath:   "/",
		DBPath:     "./caldav.db",
		LogLevel:   "info",
		Auth: AuthConfig{
			Provider: "basic",
		},
		TLS: TLSConfig{
			CacheDir: ".cache/certs",
		},
	}
}

// Load builds a Config by layering sources in order:
// defaults < YAML file < environment variables < CLI flags.
// configPath may be empty to skip file loading. args are the CLI
// arguments (typically os.Args[1:]).
func Load(configPath string, args []string) (*Config, error) {
	cfg := DefaultConfig()

	// --- CLI flags (parse early so we can pick up --config) ---
	fs := flag.NewFlagSet("caldav", flag.ContinueOnError)

	flagListen := fs.String("listen", "", "listen address")
	flagDBPath := fs.String("db-path", "", "database path")
	flagBasePath := fs.String("base-path", "", "base URL path")
	flagLogLevel := fs.String("log-level", "", "log level")
	flagConfig := fs.String("config", "", "config file path")
	flagTLS := fs.Bool("tls", false, "enable TLS")
	flagAutoCert := fs.Bool("auto-cert", false, "enable ACME auto-cert")
	flagCertFile := fs.String("cert-file", "", "TLS certificate file")
	flagKeyFile := fs.String("key-file", "", "TLS key file")
	flagACMEHost := fs.String("acme-host", "", "ACME host")
	flagAuthProvider := fs.String("auth-provider", "", "auth provider (basic or oauth2)")

	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("parsing flags: %w", err)
	}

	// If --config was given, use it; otherwise fall back to the caller-supplied path.
	if *flagConfig != "" {
		configPath = *flagConfig
	}

	// --- YAML file ---
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file: %w", err)
		}
	}

	// --- Environment variables ---
	applyEnv(cfg)

	// --- CLI flag overrides (only when explicitly set) ---
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen":
			cfg.ListenAddr = *flagListen
		case "db-path":
			cfg.DBPath = *flagDBPath
		case "base-path":
			cfg.BasePath = *flagBasePath
		case "log-level":
			cfg.LogLevel = *flagLogLevel
		case "tls":
			cfg.TLS.Enabled = *flagTLS
		case "auto-cert":
			cfg.TLS.AutoCert = *flagAutoCert
		case "cert-file":
			cfg.TLS.CertFile = *flagCertFile
		case "key-file":
			cfg.TLS.KeyFile = *flagKeyFile
		case "acme-host":
			cfg.TLS.ACMEHost = *flagACMEHost
		case "auth-provider":
			cfg.Auth.Provider = *flagAuthProvider
		}
	})

	return cfg, nil
}

// applyEnv overlays environment variables onto cfg.
func applyEnv(cfg *Config) {
	if v := os.Getenv("CALDAV_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("CALDAV_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("CALDAV_BASE_PATH"); v != "" {
		cfg.BasePath = v
	}
	if v := os.Getenv("CALDAV_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("CALDAV_TLS_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.TLS.Enabled = b
		}
	}
	if v := os.Getenv("CALDAV_TLS_AUTO_CERT"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.TLS.AutoCert = b
		}
	}
	if v := os.Getenv("CALDAV_TLS_CERT_FILE"); v != "" {
		cfg.TLS.CertFile = v
	}
	if v := os.Getenv("CALDAV_TLS_KEY_FILE"); v != "" {
		cfg.TLS.KeyFile = v
	}
	if v := os.Getenv("CALDAV_TLS_ACME_HOST"); v != "" {
		cfg.TLS.ACMEHost = v
	}
	if v := os.Getenv("CALDAV_AUTH_PROVIDER"); v != "" {
		cfg.Auth.Provider = v
	}
}
