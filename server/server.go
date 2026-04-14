package server

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/codanael/caldav-go/storage"
	"github.com/emersion/go-webdav/caldav"
)

// New creates a CalDAV http.Handler with authentication and logging middleware.
func New(opts ...Option) http.Handler {
	cfg := &serverConfig{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	for _, o := range opts {
		o(cfg)
	}

	// Build the core CalDAV handler from go-webdav.
	caldavHandler := &caldav.Handler{
		Backend: cfg.backend,
		Prefix:  cfg.prefix,
	}

	// If backend supports extended operations, wrap with our interceptor.
	var h http.Handler
	if eb, ok := cfg.backend.(storage.ExtendedBackend); ok {
		h = newExtendedHandler(eb, caldavHandler, cfg.logger)
	} else if sb, ok := cfg.backend.(storage.SyncBackend); ok {
		h = newSyncCollectionHandler(sb, caldavHandler, cfg.logger)
	} else {
		h = caldavHandler
	}

	// Wrap with middleware: auth first (innermost), then logging (outermost).
	if cfg.auth != nil {
		h = authMiddleware(cfg.auth, cfg.logger, h)
	}
	h = loggingMiddleware(cfg.logger, h)

	return h
}
