package auth

import (
	"context"
	"net/http"
)

// User represents an authenticated user.
type User struct {
	ID          string
	DisplayName string
	Email       string
}

// Provider authenticates HTTP requests and returns user info.
type Provider interface {
	// Authenticate validates the request credentials.
	// Returns the authenticated user or an error.
	Authenticate(r *http.Request) (*User, error)

	// Challenge returns the WWW-Authenticate header value for 401 responses.
	Challenge() string
}

// contextKey is an unexported type used for context keys in this package.
type contextKey struct{}

// userContextKey is the key used to store the authenticated user in a context.
var userContextKey = contextKey{}

// WithUser returns a new context with the given user stored in it.
func WithUser(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

// UserFromContext retrieves the authenticated user from the context, if any.
func UserFromContext(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(userContextKey).(*User)
	return user, ok
}
