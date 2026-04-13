package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/codanael/caldav-go/auth"
	"github.com/codanael/caldav-go/storage"
)

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// authMiddleware returns an http.Handler that authenticates requests using the
// given provider. On failure it responds with 401 and the appropriate
// WWW-Authenticate challenge. On success it injects the user ID into the
// request context via storage.ContextWithUser and delegates to next.
func authMiddleware(provider auth.Provider, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := provider.Authenticate(r)
		if err != nil {
			logger.Debug("authentication failed",
				"method", r.Method,
				"path", r.URL.Path,
				"error", err,
			)
			w.Header().Set("WWW-Authenticate", provider.Challenge())
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		ctx := storage.ContextWithUser(r.Context(), user.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loggingMiddleware returns an http.Handler that logs each request's method,
// path, response status code, and duration using the provided logger.
func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rec, r)

		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.statusCode,
			"duration", time.Since(start),
		)
	})
}
