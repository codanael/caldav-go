package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/codanael/caldav-go/auth"
	"github.com/codanael/caldav-go/config"
	"github.com/codanael/caldav-go/server"
	"github.com/codanael/caldav-go/storage/postgres"
	"github.com/codanael/caldav-go/storage/sqlite"
	caldavtls "github.com/codanael/caldav-go/tls"
	"github.com/emersion/go-webdav/caldav"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Determine config file path from --config flag or CALDAV_CONFIG env var.
	configPath := os.Getenv("CALDAV_CONFIG")

	// Load configuration (layering: defaults < YAML < env < CLI flags).
	cfg, err := config.Load(configPath, os.Args[1:])
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Set up structured logger based on configured log level.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
	}))

	// Open storage backend.
	backend, closeFn, err := openBackend(cfg)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer closeFn()

	// Set up auth provider.
	authProvider, err := buildAuthProvider(cfg, logger)
	if err != nil {
		return fmt.Errorf("setting up auth: %w", err)
	}

	// Create the CalDAV server handler.
	handler := server.New(
		server.WithBackend(backend),
		server.WithAuth(authProvider),
		server.WithPrefix(cfg.BasePath),
		server.WithLogger(logger),
	)

	// Build the HTTP server.
	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler,
	}

	// Print startup banner.
	tlsStatus := "disabled"
	if cfg.TLS.Enabled {
		if cfg.TLS.AutoCert {
			tlsStatus = "enabled (auto-cert)"
		} else {
			tlsStatus = "enabled"
		}
	}
	logger.Info("starting CalDAV server",
		"listen", cfg.ListenAddr,
		"auth", cfg.Auth.Provider,
		"tls", tlsStatus,
		"db", cfg.DBPath,
	)

	// Channel to capture server errors.
	errCh := make(chan error, 1)

	// Start the server in a goroutine.
	go func() {
		if cfg.TLS.Enabled {
			tlsCfg, err := caldavtls.NewTLSConfig(caldavtls.Config{
				AutoCert: cfg.TLS.AutoCert,
				CertFile: cfg.TLS.CertFile,
				KeyFile:  cfg.TLS.KeyFile,
				ACMEHost: cfg.TLS.ACMEHost,
				CacheDir: cfg.TLS.CacheDir,
			})
			if err != nil {
				errCh <- fmt.Errorf("configuring TLS: %w", err)
				return
			}
			srv.TLSConfig = tlsCfg
			errCh <- srv.ListenAndServeTLS("", "")
		} else {
			errCh <- srv.ListenAndServe()
		}
	}()

	// Listen for OS shutdown signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("received shutdown signal", "signal", sig.String())
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server error: %w", err)
		}
	}

	// Graceful shutdown with a 10-second deadline.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger.Info("shutting down server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
	}

	// Close the storage backend.
	closeFn()

	logger.Info("server stopped")
	return nil
}

// buildAuthProvider creates the appropriate auth.Provider based on configuration.
func buildAuthProvider(cfg *config.Config, logger *slog.Logger) (auth.Provider, error) {
	switch strings.ToLower(cfg.Auth.Provider) {
	case "basic", "":
		provider := auth.NewBasicProvider()
		for username, u := range cfg.Auth.Basic.Users {
			if err := provider.AddUser(username, u.Password, auth.User{
				ID:          username,
				DisplayName: u.DisplayName,
				Email:       u.Email,
			}); err != nil {
				return nil, fmt.Errorf("adding user %q: %w", username, err)
			}
			logger.Debug("registered basic auth user", "username", username)
		}
		return provider, nil

	case "oauth2":
		provider := auth.NewOAuth2Provider(auth.OAuth2Options{
			JWKSURL:     cfg.Auth.OAuth2.JWKSURL,
			Issuer:      cfg.Auth.OAuth2.Issuer,
			Audience:    cfg.Auth.OAuth2.Audience,
			UserIDClaim: cfg.Auth.OAuth2.UserIDClaim,
		})
		return provider, nil

	default:
		return nil, fmt.Errorf("unknown auth provider: %q", cfg.Auth.Provider)
	}
}

// parseLogLevel converts a level string to slog.Level.
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// openBackend creates the appropriate storage backend based on configuration.
// Returns the backend, a close function, and any error.
func openBackend(cfg *config.Config) (caldav.Backend, func(), error) {
	switch strings.ToLower(cfg.DBDriver) {
	case "postgres", "postgresql":
		b, err := postgres.New(cfg.DBPath)
		if err != nil {
			return nil, nil, err
		}
		return b, func() { b.Close() }, nil
	case "sqlite", "":
		b, err := sqlite.New(cfg.DBPath)
		if err != nil {
			return nil, nil, err
		}
		return b, func() { b.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("unknown database driver: %q (use 'sqlite' or 'postgres')", cfg.DBDriver)
	}
}
