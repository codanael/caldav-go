package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.BasePath != "/" {
		t.Errorf("BasePath = %q, want %q", cfg.BasePath, "/")
	}
	if cfg.DBPath != "./caldav.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "./caldav.db")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.Auth.Provider != "basic" {
		t.Errorf("Auth.Provider = %q, want %q", cfg.Auth.Provider, "basic")
	}
	if cfg.TLS.Enabled {
		t.Error("TLS.Enabled should be false by default")
	}
}

func TestLoadFromYAML(t *testing.T) {
	yamlContent := `
listen_addr: ":9090"
base_path: "/dav"
db_path: "/var/lib/caldav.db"
log_level: "debug"
tls:
  enabled: true
  cert_file: "/etc/ssl/cert.pem"
  key_file: "/etc/ssl/key.pem"
auth:
  provider: "oauth2"
  oauth2:
    jwks_url: "https://auth.example.com/jwks"
    issuer: "https://auth.example.com/"
    audience: "caldav"
    user_id_claim: "email"
  basic:
    users:
      testuser:
        password: "secret"
        display_name: "Test User"
        email: "test@example.com"
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
	}
	if cfg.BasePath != "/dav" {
		t.Errorf("BasePath = %q, want %q", cfg.BasePath, "/dav")
	}
	if cfg.DBPath != "/var/lib/caldav.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/var/lib/caldav.db")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if !cfg.TLS.Enabled {
		t.Error("TLS.Enabled should be true")
	}
	if cfg.TLS.CertFile != "/etc/ssl/cert.pem" {
		t.Errorf("TLS.CertFile = %q, want %q", cfg.TLS.CertFile, "/etc/ssl/cert.pem")
	}
	if cfg.TLS.KeyFile != "/etc/ssl/key.pem" {
		t.Errorf("TLS.KeyFile = %q, want %q", cfg.TLS.KeyFile, "/etc/ssl/key.pem")
	}
	if cfg.Auth.Provider != "oauth2" {
		t.Errorf("Auth.Provider = %q, want %q", cfg.Auth.Provider, "oauth2")
	}
	if cfg.Auth.OAuth2.JWKSURL != "https://auth.example.com/jwks" {
		t.Errorf("Auth.OAuth2.JWKSURL = %q, want %q", cfg.Auth.OAuth2.JWKSURL, "https://auth.example.com/jwks")
	}
	if cfg.Auth.OAuth2.UserIDClaim != "email" {
		t.Errorf("Auth.OAuth2.UserIDClaim = %q, want %q", cfg.Auth.OAuth2.UserIDClaim, "email")
	}
	u, ok := cfg.Auth.Basic.Users["testuser"]
	if !ok {
		t.Fatal("expected user 'testuser' in basic auth config")
	}
	if u.Password != "secret" {
		t.Errorf("user password = %q, want %q", u.Password, "secret")
	}
	if u.DisplayName != "Test User" {
		t.Errorf("user display_name = %q, want %q", u.DisplayName, "Test User")
	}
}

func TestEnvOverrides(t *testing.T) {
	envVars := map[string]string{
		"CALDAV_LISTEN_ADDR":   ":3000",
		"CALDAV_DB_PATH":       "/tmp/test.db",
		"CALDAV_BASE_PATH":     "/cal",
		"CALDAV_LOG_LEVEL":     "warn",
		"CALDAV_TLS_ENABLED":   "true",
		"CALDAV_TLS_AUTO_CERT": "true",
		"CALDAV_TLS_CERT_FILE": "/certs/cert.pem",
		"CALDAV_TLS_KEY_FILE":  "/certs/key.pem",
		"CALDAV_TLS_ACME_HOST": "example.com",
		"CALDAV_AUTH_PROVIDER":  "oauth2",
	}
	for k, v := range envVars {
		t.Setenv(k, v)
	}

	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.ListenAddr != ":3000" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":3000")
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/tmp/test.db")
	}
	if cfg.BasePath != "/cal" {
		t.Errorf("BasePath = %q, want %q", cfg.BasePath, "/cal")
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "warn")
	}
	if !cfg.TLS.Enabled {
		t.Error("TLS.Enabled should be true")
	}
	if !cfg.TLS.AutoCert {
		t.Error("TLS.AutoCert should be true")
	}
	if cfg.TLS.CertFile != "/certs/cert.pem" {
		t.Errorf("TLS.CertFile = %q, want %q", cfg.TLS.CertFile, "/certs/cert.pem")
	}
	if cfg.TLS.KeyFile != "/certs/key.pem" {
		t.Errorf("TLS.KeyFile = %q, want %q", cfg.TLS.KeyFile, "/certs/key.pem")
	}
	if cfg.TLS.ACMEHost != "example.com" {
		t.Errorf("TLS.ACMEHost = %q, want %q", cfg.TLS.ACMEHost, "example.com")
	}
	if cfg.Auth.Provider != "oauth2" {
		t.Errorf("Auth.Provider = %q, want %q", cfg.Auth.Provider, "oauth2")
	}
}

func TestFlagOverrides(t *testing.T) {
	args := []string{
		"--listen", ":4000",
		"--db-path", "/data/caldav.db",
		"--base-path", "/webdav",
		"--log-level", "error",
		"--tls",
		"--auto-cert",
		"--cert-file", "/ssl/cert.pem",
		"--key-file", "/ssl/key.pem",
		"--acme-host", "cal.example.com",
		"--auth-provider", "oauth2",
	}

	cfg, err := Load("", args)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.ListenAddr != ":4000" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":4000")
	}
	if cfg.DBPath != "/data/caldav.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/data/caldav.db")
	}
	if cfg.BasePath != "/webdav" {
		t.Errorf("BasePath = %q, want %q", cfg.BasePath, "/webdav")
	}
	if cfg.LogLevel != "error" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "error")
	}
	if !cfg.TLS.Enabled {
		t.Error("TLS.Enabled should be true")
	}
	if !cfg.TLS.AutoCert {
		t.Error("TLS.AutoCert should be true")
	}
	if cfg.TLS.CertFile != "/ssl/cert.pem" {
		t.Errorf("TLS.CertFile = %q, want %q", cfg.TLS.CertFile, "/ssl/cert.pem")
	}
	if cfg.TLS.KeyFile != "/ssl/key.pem" {
		t.Errorf("TLS.KeyFile = %q, want %q", cfg.TLS.KeyFile, "/ssl/key.pem")
	}
	if cfg.TLS.ACMEHost != "cal.example.com" {
		t.Errorf("TLS.ACMEHost = %q, want %q", cfg.TLS.ACMEHost, "cal.example.com")
	}
	if cfg.Auth.Provider != "oauth2" {
		t.Errorf("Auth.Provider = %q, want %q", cfg.Auth.Provider, "oauth2")
	}
}

func TestPrecedence(t *testing.T) {
	// YAML sets everything to one set of values
	yamlContent := `
listen_addr: ":1111"
db_path: "/yaml/db"
base_path: "/yaml"
log_level: "debug"
auth:
  provider: "basic"
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Env overrides some of those
	t.Setenv("CALDAV_LISTEN_ADDR", ":2222")
	t.Setenv("CALDAV_LOG_LEVEL", "warn")

	// Flags override one more
	args := []string{
		"--listen", ":3333",
	}

	cfg, err := Load(cfgPath, args)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// --listen flag wins over env and yaml
	if cfg.ListenAddr != ":3333" {
		t.Errorf("ListenAddr = %q, want %q (flag should win)", cfg.ListenAddr, ":3333")
	}
	// env wins over yaml for log_level (no flag set)
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want %q (env should win over yaml)", cfg.LogLevel, "warn")
	}
	// yaml value preserved where no env or flag overrides
	if cfg.DBPath != "/yaml/db" {
		t.Errorf("DBPath = %q, want %q (yaml value should be preserved)", cfg.DBPath, "/yaml/db")
	}
	if cfg.BasePath != "/yaml" {
		t.Errorf("BasePath = %q, want %q (yaml value should be preserved)", cfg.BasePath, "/yaml")
	}
	if cfg.Auth.Provider != "basic" {
		t.Errorf("Auth.Provider = %q, want %q (yaml value should be preserved)", cfg.Auth.Provider, "basic")
	}
}
