package tls

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"golang.org/x/crypto/acme/autocert"
)

// Config holds TLS configuration for the server.
type Config struct {
	// AutoCert enables automatic certificate management via Let's Encrypt.
	AutoCert bool
	// CertFile is the path to a PEM-encoded certificate file (used when AutoCert is false).
	CertFile string
	// KeyFile is the path to a PEM-encoded private key file (used when AutoCert is false).
	KeyFile string
	// ACMEHost is the hostname that autocert will accept certificates for.
	ACMEHost string
	// CacheDir is the directory used to cache autocert certificates.
	// Defaults to ".caldav-certs" if empty.
	CacheDir string
}

// NewTLSConfig returns a *tls.Config based on the provided configuration.
// If AutoCert is true, it configures Let's Encrypt autocert with the given
// ACMEHost and CacheDir. Otherwise, it loads the certificate and key from
// the specified files.
func NewTLSConfig(cfg Config) (*tls.Config, error) {
	if cfg.AutoCert {
		if cfg.ACMEHost == "" {
			return nil, fmt.Errorf("tls: ACMEHost is required when AutoCert is enabled")
		}
		cacheDir := cfg.CacheDir
		if cacheDir == "" {
			cacheDir = ".caldav-certs"
		}
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.ACMEHost),
			Cache:      autocert.DirCache(cacheDir),
		}
		return m.TLSConfig(), nil
	}

	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return nil, fmt.Errorf("tls: CertFile and KeyFile are required when AutoCert is disabled")
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: failed to load certificate: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ListenAndServeTLS starts an HTTPS server with the given handler and TLS
// configuration on the specified address.
func ListenAndServeTLS(addr string, handler http.Handler, tlsCfg *tls.Config) error {
	srv := &http.Server{
		Addr:      addr,
		Handler:   handler,
		TLSConfig: tlsCfg,
	}
	// When TLS certificates are provided via tls.Config, pass empty strings
	// for certFile and keyFile.
	return srv.ListenAndServeTLS("", "")
}
