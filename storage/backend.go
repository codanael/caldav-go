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

// SyncChange represents a single change tracked for sync-collection REPORT.
type SyncChange struct {
	Path       string
	ETag       string // empty for deleted objects
	ChangeType string // "created", "modified", "deleted"
}

// SyncResponse holds the result of a sync-collection query.
type SyncResponse struct {
	NewToken string
	Changes  []SyncChange
}

// SyncBackend extends caldav.Backend with RFC 6578 sync-token support.
type SyncBackend interface {
	// GetSyncToken returns the current sync token for a calendar.
	GetSyncToken(ctx context.Context, calendarPath string) (string, error)

	// SyncCollection returns changes since the given sync token.
	// If syncToken is empty, returns all current objects as "created".
	SyncCollection(ctx context.Context, calendarPath string, syncToken string) (*SyncResponse, error)
}
