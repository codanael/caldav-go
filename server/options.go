package server

import (
	"log/slog"

	"github.com/codanael/caldav-go/auth"
	"github.com/emersion/go-webdav/caldav"
)

// Option configures a server instance.
type Option func(*serverConfig)

type serverConfig struct {
	backend caldav.Backend
	auth    auth.Provider
	prefix  string
	logger  *slog.Logger
}

// WithBackend sets the CalDAV storage backend.
func WithBackend(b caldav.Backend) Option {
	return func(cfg *serverConfig) {
		cfg.backend = b
	}
}

// WithAuth sets the authentication provider.
func WithAuth(a auth.Provider) Option {
	return func(cfg *serverConfig) {
		cfg.auth = a
	}
}

// WithPrefix sets the URL prefix for the CalDAV handler.
func WithPrefix(prefix string) Option {
	return func(cfg *serverConfig) {
		cfg.prefix = prefix
	}
}

// WithLogger sets the structured logger for request logging.
func WithLogger(logger *slog.Logger) Option {
	return func(cfg *serverConfig) {
		cfg.logger = logger
	}
}
