package storage

import "context"

type contextKey string

const UserIDKey contextKey = "caldav-user-id"

func UserFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(UserIDKey).(string); ok {
		return v
	}
	return ""
}

func ContextWithUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, UserIDKey, userID)
}
