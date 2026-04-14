package server

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/codanael/caldav-go/storage"
	"github.com/emersion/go-webdav/caldav"
)

// New creates a CalDAV http.Handler with authentication and logging middleware.
// Options are applied via functional option values. A default no-op logger is
// used when none is provided.
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

	// Check if backend supports sync-collection.
	var syncHandler http.Handler
	if sb, ok := cfg.backend.(storage.SyncBackend); ok {
		syncHandler = newSyncCollectionHandler(sb, caldavHandler, cfg.logger)
	} else {
		syncHandler = caldavHandler
	}

	// Wrap with middleware: auth first (innermost), then logging (outermost).
	var h http.Handler = syncHandler
	if cfg.auth != nil {
		h = authMiddleware(cfg.auth, cfg.logger, h)
	}
	h = loggingMiddleware(cfg.logger, h)

	return h
}
