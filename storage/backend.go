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

// CalendarUpdate holds optional fields to update on a calendar via PROPPATCH.
type CalendarUpdate struct {
	Name        *string
	Description *string
	Color       *string
}

// SyncBackend extends caldav.Backend with RFC 6578 sync-token support.
type SyncBackend interface {
	// GetSyncToken returns the current sync token for a calendar.
	GetSyncToken(ctx context.Context, calendarPath string) (string, error)

	// SyncCollection returns changes since the given sync token.
	// If syncToken is empty, returns all current objects as "created".
	SyncCollection(ctx context.Context, calendarPath string, syncToken string) (*SyncResponse, error)
}

// CalendarExtra holds extended calendar properties not in caldav.Calendar.
type CalendarExtra struct {
	Color string
}

// ExtendedBackend extends caldav.Backend with PROPPATCH and calendar deletion.
type ExtendedBackend interface {
	SyncBackend

	// UpdateCalendar updates calendar properties via PROPPATCH.
	UpdateCalendar(ctx context.Context, path string, update *CalendarUpdate) error

	// DeleteCalendar deletes a calendar and all its objects.
	DeleteCalendar(ctx context.Context, path string) error

	// GetCalendarExtra returns extended properties for a calendar.
	GetCalendarExtra(ctx context.Context, path string) (*CalendarExtra, error)
}
