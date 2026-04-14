package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/codanael/caldav-go/storage"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	_ "modernc.org/sqlite"
)

// Backend implements caldav.Backend backed by SQLite.
type Backend struct {
	db *sql.DB
}

// New creates a new SQLite-backed CalDAV backend.
// It opens the database, enables WAL mode and foreign keys, and runs migrations.
func New(dbPath string) (*Backend, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: enable WAL: %w", err)
	}

	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: enable foreign keys: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: migrate: %w", err)
	}

	return &Backend{db: db}, nil
}

// Close closes the underlying database connection.
func (b *Backend) Close() error {
	return b.db.Close()
}

func httpError(code int, msg string) error {
	return webdav.NewHTTPError(code, fmt.Errorf("%s", msg))
}

// CurrentUserPrincipal returns the principal path for the user in context.
func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return "", httpError(http.StatusUnauthorized, "caldav: no user in context")
	}
	return "/" + userID + "/", nil
}

// CalendarHomeSetPath returns the calendar home set path for the user in context.
func (b *Backend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return "", httpError(http.StatusUnauthorized, "caldav: no user in context")
	}
	return "/" + userID + "/calendars/", nil
}

// CreateCalendar creates a new calendar. The calendar's Path must already be set.
func (b *Backend) CreateCalendar(ctx context.Context, calendar *caldav.Calendar) error {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	components := "VEVENT,VTODO"
	if len(calendar.SupportedComponentSet) > 0 {
		components = strings.Join(calendar.SupportedComponentSet, ",")
	}

	_, err := b.db.ExecContext(ctx,
		`INSERT INTO calendars (user_id, path, name, description, components) VALUES (?, ?, ?, ?, ?)`,
		userID, calendar.Path, calendar.Name, calendar.Description, components,
	)
	if err != nil {
		return fmt.Errorf("sqlite: create calendar: %w", err)
	}
	return nil
}

// ListCalendars returns all calendars for the current user.
func (b *Backend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return nil, httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	rows, err := b.db.QueryContext(ctx,
		`SELECT path, name, description, components FROM calendars WHERE user_id = ?`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list calendars: %w", err)
	}
	defer rows.Close()

	var calendars []caldav.Calendar
	for rows.Next() {
		var cal caldav.Calendar
		var components string
		if err := rows.Scan(&cal.Path, &cal.Name, &cal.Description, &components); err != nil {
			return nil, fmt.Errorf("sqlite: scan calendar: %w", err)
		}
		if components != "" {
			cal.SupportedComponentSet = strings.Split(components, ",")
		}
		calendars = append(calendars, cal)
	}
	return calendars, rows.Err()
}

// GetCalendar returns a specific calendar, verifying it belongs to the current user.
func (b *Backend) GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return nil, httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	var cal caldav.Calendar
	var components string
	err := b.db.QueryRowContext(ctx,
		`SELECT path, name, description, components FROM calendars WHERE path = ? AND user_id = ?`,
		path, userID,
	).Scan(&cal.Path, &cal.Name, &cal.Description, &components)
	if err == sql.ErrNoRows {
		return nil, httpError(http.StatusNotFound, "caldav: calendar not found")
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get calendar: %w", err)
	}
	if components != "" {
		cal.SupportedComponentSet = strings.Split(components, ",")
	}
	return &cal, nil
}

// UpdateCalendar updates calendar properties.
func (b *Backend) UpdateCalendar(ctx context.Context, path string, update *storage.CalendarUpdate) error {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	// Build dynamic UPDATE query.
	sets := []string{}
	args := []any{}
	if update.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *update.Name)
	}
	if update.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *update.Description)
	}
	if update.Color != nil {
		sets = append(sets, "color = ?")
		args = append(args, *update.Color)
	}
	if len(sets) == 0 {
		return nil
	}

	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC())
	args = append(args, path, userID)

	query := "UPDATE calendars SET " + strings.Join(sets, ", ") + " WHERE path = ? AND user_id = ?"
	result, err := b.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("sqlite: update calendar: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 0 {
		return httpError(http.StatusNotFound, "caldav: calendar not found")
	}
	return nil
}

// DeleteCalendar deletes a calendar and all its objects.
func (b *Backend) DeleteCalendar(ctx context.Context, path string) error {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	result, err := b.db.ExecContext(ctx,
		`DELETE FROM calendars WHERE path = ? AND user_id = ?`,
		path, userID,
	)
	if err != nil {
		return fmt.Errorf("sqlite: delete calendar: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 0 {
		return httpError(http.StatusNotFound, "caldav: calendar not found")
	}
	return nil
}

// GetCalendarExtra returns extended properties (color) for a calendar.
func (b *Backend) GetCalendarExtra(ctx context.Context, path string) (*storage.CalendarExtra, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return nil, httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	var color string
	err := b.db.QueryRowContext(ctx,
		`SELECT color FROM calendars WHERE path = ? AND user_id = ?`,
		path, userID,
	).Scan(&color)
	if err == sql.ErrNoRows {
		return nil, httpError(http.StatusNotFound, "caldav: calendar not found")
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get calendar extra: %w", err)
	}
	return &storage.CalendarExtra{Color: color}, nil
}

// getCalendarByPath returns the calendar ID and user_id for a given path.
func (b *Backend) getCalendarByPath(ctx context.Context, calendarPath string) (int64, string, error) {
	var id int64
	var ownerID string
	err := b.db.QueryRowContext(ctx,
		`SELECT id, user_id FROM calendars WHERE path = ?`, calendarPath,
	).Scan(&id, &ownerID)
	if err == sql.ErrNoRows {
		return 0, "", httpError(http.StatusNotFound, "caldav: calendar not found")
	}
	if err != nil {
		return 0, "", fmt.Errorf("sqlite: get calendar by path: %w", err)
	}
	return id, ownerID, nil
}

// calendarPathFromObjectPath extracts the calendar path from an object path.
// Object path: /{userID}/calendars/{calendarName}/{uid}.ics
// Calendar path: /{userID}/calendars/{calendarName}/
func calendarPathFromObjectPath(objPath string) string {
	idx := strings.LastIndex(objPath, "/")
	if idx < 0 {
		return objPath
	}
	return objPath[:idx+1]
}

// PutCalendarObject creates or updates a calendar object.
func (b *Backend) PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return nil, httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	compType, uid, err := caldav.ValidateCalendarObject(calendar)
	if err != nil {
		return nil, caldav.NewPreconditionError(caldav.PreconditionValidCalendarData)
	}

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(calendar); err != nil {
		return nil, fmt.Errorf("sqlite: encode ical: %w", err)
	}
	icalData := buf.String()

	hash := sha256.Sum256([]byte(icalData))
	etag := hex.EncodeToString(hash[:])

	var startTime, endTime sql.NullTime
	for _, comp := range calendar.Children {
		if comp.Name == ical.CompTimezone {
			continue
		}
		if prop := comp.Props.Get(ical.PropDateTimeStart); prop != nil {
			t, err := prop.DateTime(time.UTC)
			if err == nil {
				startTime = sql.NullTime{Time: t, Valid: true}
			}
		}
		if prop := comp.Props.Get(ical.PropDateTimeEnd); prop != nil {
			t, err := prop.DateTime(time.UTC)
			if err == nil {
				endTime = sql.NullTime{Time: t, Valid: true}
			}
		}
		if !endTime.Valid {
			if prop := comp.Props.Get(ical.PropDue); prop != nil {
				t, err := prop.DateTime(time.UTC)
				if err == nil {
					endTime = sql.NullTime{Time: t, Valid: true}
				}
			}
		}
		break
	}

	calendarPath := calendarPathFromObjectPath(path)
	calID, ownerID, err := b.getCalendarByPath(ctx, calendarPath)
	if err != nil {
		return nil, err
	}
	if ownerID != userID {
		return nil, httpError(http.StatusForbidden, "caldav: calendar does not belong to user")
	}

	now := time.Now().UTC()

	var existingID int64
	var existingETag string
	err = b.db.QueryRowContext(ctx,
		`SELECT id, etag FROM calendar_objects WHERE path = ?`, path,
	).Scan(&existingID, &existingETag)
	existingFound := err == nil
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("sqlite: check existing object: %w", err)
	}

	if opts != nil {
		if opts.IfNoneMatch.IsSet() {
			if existingFound {
				if opts.IfNoneMatch.IsWildcard() {
					return nil, caldav.NewPreconditionError(caldav.PreconditionNoUIDConflict)
				}
				matched, matchErr := opts.IfNoneMatch.MatchETag(existingETag)
				if matchErr != nil {
					return nil, fmt.Errorf("sqlite: match etag: %w", matchErr)
				}
				if matched {
					return nil, caldav.NewPreconditionError(caldav.PreconditionNoUIDConflict)
				}
			}
		}
		if opts.IfMatch.IsSet() {
			if !existingFound {
				return nil, caldav.NewPreconditionError(caldav.PreconditionNoUIDConflict)
			}
			matched, matchErr := opts.IfMatch.MatchETag(existingETag)
			if matchErr != nil {
				return nil, fmt.Errorf("sqlite: match etag: %w", matchErr)
			}
			if !matched {
				return nil, caldav.NewPreconditionError(caldav.PreconditionNoUIDConflict)
			}
		}
	}

	size := int64(len(icalData))

	// Use a transaction to atomically update the object, bump sync token, and record change.
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite: begin tx: %w", err)
	}
	defer tx.Rollback()

	changeType := "created"
	if existingFound {
		changeType = "modified"
		_, err = tx.ExecContext(ctx,
			`UPDATE calendar_objects SET uid=?, etag=?, ical_data=?, comp_type=?, start_time=?, end_time=?, size=?, updated_at=? WHERE id=?`,
			uid, etag, icalData, compType, startTime, endTime, size, now, existingID,
		)
	} else {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO calendar_objects (calendar_id, path, uid, etag, ical_data, comp_type, start_time, end_time, size, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			calID, path, uid, etag, icalData, compType, startTime, endTime, size, now, now,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: put calendar object: %w", err)
	}

	// Bump sync token and record change.
	if _, err := tx.ExecContext(ctx,
		`UPDATE calendars SET sync_token = sync_token + 1, updated_at = ? WHERE id = ?`,
		now, calID,
	); err != nil {
		return nil, fmt.Errorf("sqlite: bump sync token: %w", err)
	}

	var newToken int64
	if err := tx.QueryRowContext(ctx,
		`SELECT sync_token FROM calendars WHERE id = ?`, calID,
	).Scan(&newToken); err != nil {
		return nil, fmt.Errorf("sqlite: get new sync token: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sync_changes (calendar_id, object_path, change_type, sync_token) VALUES (?, ?, ?, ?)`,
		calID, path, changeType, newToken,
	); err != nil {
		return nil, fmt.Errorf("sqlite: record sync change: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sqlite: commit: %w", err)
	}

	return &caldav.CalendarObject{
		Path:          path,
		ModTime:       now,
		ContentLength: size,
		ETag:          etag,
		Data:          calendar,
	}, nil
}

// GetCalendarObject returns a single calendar object by path.
func (b *Backend) GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return nil, httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	var icalData string
	var etag string
	var size int64
	var updatedAt time.Time

	err := b.db.QueryRowContext(ctx,
		`SELECT co.ical_data, co.etag, co.size, co.updated_at
		 FROM calendar_objects co
		 JOIN calendars c ON co.calendar_id = c.id
		 WHERE co.path = ? AND c.user_id = ?`,
		path, userID,
	).Scan(&icalData, &etag, &size, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, httpError(http.StatusNotFound, "caldav: calendar object not found")
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get calendar object: %w", err)
	}

	cal, err := ical.NewDecoder(strings.NewReader(icalData)).Decode()
	if err != nil {
		return nil, fmt.Errorf("sqlite: decode ical data: %w", err)
	}

	return &caldav.CalendarObject{
		Path:          path,
		ModTime:       updatedAt,
		ContentLength: size,
		ETag:          etag,
		Data:          cal,
	}, nil
}

// ListCalendarObjects returns all objects in a calendar.
func (b *Backend) ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return nil, httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	rows, err := b.db.QueryContext(ctx,
		`SELECT co.path, co.ical_data, co.etag, co.size, co.updated_at
		 FROM calendar_objects co
		 JOIN calendars c ON co.calendar_id = c.id
		 WHERE c.path = ? AND c.user_id = ?`,
		path, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list calendar objects: %w", err)
	}
	defer rows.Close()

	var objects []caldav.CalendarObject
	for rows.Next() {
		var objPath, icalData, etag string
		var size int64
		var updatedAt time.Time
		if err := rows.Scan(&objPath, &icalData, &etag, &size, &updatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan calendar object: %w", err)
		}

		cal, err := ical.NewDecoder(strings.NewReader(icalData)).Decode()
		if err != nil {
			return nil, fmt.Errorf("sqlite: decode ical data: %w", err)
		}

		objects = append(objects, caldav.CalendarObject{
			Path:          objPath,
			ModTime:       updatedAt,
			ContentLength: size,
			ETag:          etag,
			Data:          cal,
		})
	}
	return objects, rows.Err()
}

// QueryCalendarObjects queries calendar objects with filtering support.
func (b *Backend) QueryCalendarObjects(ctx context.Context, path string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return nil, httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	queryStr := `SELECT co.path, co.ical_data, co.etag, co.size, co.updated_at
		FROM calendar_objects co
		JOIN calendars c ON co.calendar_id = c.id
		WHERE c.path = ? AND c.user_id = ?`
	args := []any{path, userID}

	if len(query.CompFilter.Comps) > 0 {
		sub := query.CompFilter.Comps[0]
		if sub.Name != "" && !sub.IsNotDefined {
			queryStr += " AND co.comp_type = ?"
			args = append(args, sub.Name)
		}
		var zeroTime time.Time
		if sub.Start != zeroTime {
			queryStr += " AND (co.start_time IS NULL OR co.start_time < ?)"
			args = append(args, sub.End)
			queryStr += " AND (co.end_time IS NULL OR co.end_time > ?)"
			args = append(args, sub.Start)
		}
	}

	rows, err := b.db.QueryContext(ctx, queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: query calendar objects: %w", err)
	}
	defer rows.Close()

	var candidates []caldav.CalendarObject
	for rows.Next() {
		var objPath, icalData, etag string
		var size int64
		var updatedAt time.Time
		if err := rows.Scan(&objPath, &icalData, &etag, &size, &updatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan calendar object: %w", err)
		}

		cal, err := ical.NewDecoder(strings.NewReader(icalData)).Decode()
		if err != nil {
			return nil, fmt.Errorf("sqlite: decode ical data: %w", err)
		}

		candidates = append(candidates, caldav.CalendarObject{
			Path:          objPath,
			ModTime:       updatedAt,
			ContentLength: size,
			ETag:          etag,
			Data:          cal,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return caldav.Filter(query, candidates)
}

// DeleteCalendarObject deletes a calendar object by path.
func (b *Backend) DeleteCalendarObject(ctx context.Context, path string) error {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	// Find the calendar that owns this object.
	calendarPath := calendarPathFromObjectPath(path)
	calID, ownerID, err := b.getCalendarByPath(ctx, calendarPath)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return httpError(http.StatusForbidden, "caldav: calendar does not belong to user")
	}

	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin tx: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx,
		`DELETE FROM calendar_objects WHERE path = ? AND calendar_id = ?`,
		path, calID,
	)
	if err != nil {
		return fmt.Errorf("sqlite: delete calendar object: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 0 {
		return httpError(http.StatusNotFound, "caldav: calendar object not found")
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE calendars SET sync_token = sync_token + 1, updated_at = ? WHERE id = ?`,
		now, calID,
	); err != nil {
		return fmt.Errorf("sqlite: bump sync token: %w", err)
	}

	var newToken int64
	if err := tx.QueryRowContext(ctx,
		`SELECT sync_token FROM calendars WHERE id = ?`, calID,
	).Scan(&newToken); err != nil {
		return fmt.Errorf("sqlite: get new sync token: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sync_changes (calendar_id, object_path, change_type, sync_token) VALUES (?, ?, ?, ?)`,
		calID, path, "deleted", newToken,
	); err != nil {
		return fmt.Errorf("sqlite: record sync change: %w", err)
	}

	return tx.Commit()
}

// GetSyncToken returns the current sync token for a calendar.
func (b *Backend) GetSyncToken(ctx context.Context, calendarPath string) (string, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return "", httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	var token int64
	err := b.db.QueryRowContext(ctx,
		`SELECT sync_token FROM calendars WHERE path = ? AND user_id = ?`,
		calendarPath, userID,
	).Scan(&token)
	if err == sql.ErrNoRows {
		return "", httpError(http.StatusNotFound, "caldav: calendar not found")
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: get sync token: %w", err)
	}
	return fmt.Sprintf("sync-token-%d", token), nil
}

// SyncCollection returns changes since the given sync token.
// If syncToken is empty, returns all current objects.
func (b *Backend) SyncCollection(ctx context.Context, calendarPath string, syncToken string) (*storage.SyncResponse, error) {
	userID := storage.UserFromContext(ctx)
	if userID == "" {
		return nil, httpError(http.StatusUnauthorized, "caldav: no user in context")
	}

	var calID int64
	var currentToken int64
	err := b.db.QueryRowContext(ctx,
		`SELECT id, sync_token FROM calendars WHERE path = ? AND user_id = ?`,
		calendarPath, userID,
	).Scan(&calID, &currentToken)
	if err == sql.ErrNoRows {
		return nil, httpError(http.StatusNotFound, "caldav: calendar not found")
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get calendar: %w", err)
	}

	newToken := fmt.Sprintf("sync-token-%d", currentToken)

	// If no sync token provided, return all current objects as "created".
	if syncToken == "" {
		rows, err := b.db.QueryContext(ctx,
			`SELECT path, etag FROM calendar_objects WHERE calendar_id = ?`, calID,
		)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list objects for sync: %w", err)
		}
		defer rows.Close()

		resp := &storage.SyncResponse{NewToken: newToken}
		for rows.Next() {
			var ch storage.SyncChange
			if err := rows.Scan(&ch.Path, &ch.ETag); err != nil {
				return nil, fmt.Errorf("sqlite: scan object: %w", err)
			}
			ch.ChangeType = "created"
			resp.Changes = append(resp.Changes, ch)
		}
		return resp, rows.Err()
	}

	// Parse the token number.
	var requestedToken int64
	if _, err := fmt.Sscanf(syncToken, "sync-token-%d", &requestedToken); err != nil {
		return nil, httpError(http.StatusPreconditionFailed, "caldav: invalid sync token")
	}

	if requestedToken > currentToken {
		return nil, httpError(http.StatusPreconditionFailed, "caldav: sync token is in the future")
	}

	// Get changes since the requested token.
	// We need the latest change per path (in case an object was created then modified).
	rows, err := b.db.QueryContext(ctx,
		`SELECT sc.object_path, sc.change_type, co.etag
		 FROM sync_changes sc
		 LEFT JOIN calendar_objects co ON co.path = sc.object_path AND co.calendar_id = sc.calendar_id
		 WHERE sc.calendar_id = ? AND sc.sync_token > ?
		 ORDER BY sc.sync_token ASC`,
		calID, requestedToken,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: query sync changes: %w", err)
	}
	defer rows.Close()

	// Deduplicate: keep the latest state per path.
	seen := make(map[string]*storage.SyncChange)
	var order []string
	for rows.Next() {
		var objPath, changeType string
		var etag sql.NullString
		if err := rows.Scan(&objPath, &changeType, &etag); err != nil {
			return nil, fmt.Errorf("sqlite: scan sync change: %w", err)
		}

		ch := &storage.SyncChange{
			Path:       objPath,
			ChangeType: changeType,
			ETag:       etag.String,
		}

		// If the object was deleted after being created/modified, mark as deleted.
		// If created then modified, just show the final etag as modified.
		if _, exists := seen[objPath]; !exists {
			order = append(order, objPath)
		}
		seen[objPath] = ch
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	resp := &storage.SyncResponse{NewToken: newToken}
	for _, p := range order {
		ch := seen[p]
		// If the object no longer exists (LEFT JOIN etag is NULL) but change says
		// created/modified, it was created then deleted — report as deleted.
		if ch.ChangeType != "deleted" && ch.ETag == "" {
			ch.ChangeType = "deleted"
		}
		resp.Changes = append(resp.Changes, *ch)
	}
	return resp, nil
}

// AddDelegation grants a delegate access to an owner's calendars.
func (b *Backend) AddDelegation(ctx context.Context, d storage.Delegation) error {
	writeAccess := 0
	if d.Write {
		writeAccess = 1
	}
	_, err := b.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO delegations (owner_id, delegate_id, write_access) VALUES (?, ?, ?)`,
		d.OwnerID, d.DelegateID, writeAccess,
	)
	if err != nil {
		return fmt.Errorf("sqlite: add delegation: %w", err)
	}
	return nil
}

// RemoveDelegation revokes a delegate's access.
func (b *Backend) RemoveDelegation(ctx context.Context, ownerID, delegateID string) error {
	_, err := b.db.ExecContext(ctx,
		`DELETE FROM delegations WHERE owner_id = ? AND delegate_id = ?`,
		ownerID, delegateID,
	)
	if err != nil {
		return fmt.Errorf("sqlite: remove delegation: %w", err)
	}
	return nil
}

// GetDelegatesFor returns users who have delegated access TO the given user.
func (b *Backend) GetDelegatesFor(ctx context.Context, userID string) (readFrom []string, writeFrom []string, err error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT owner_id, write_access FROM delegations WHERE delegate_id = ?`, userID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite: get delegates for: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ownerID string
		var writeAccess int
		if err := rows.Scan(&ownerID, &writeAccess); err != nil {
			return nil, nil, fmt.Errorf("sqlite: scan delegation: %w", err)
		}
		if writeAccess == 1 {
			writeFrom = append(writeFrom, ownerID)
		} else {
			readFrom = append(readFrom, ownerID)
		}
	}
	return readFrom, writeFrom, rows.Err()
}

// GetDelegatesOf returns users the given user has delegated access to.
func (b *Backend) GetDelegatesOf(ctx context.Context, userID string) (readTo []string, writeTo []string, err error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT delegate_id, write_access FROM delegations WHERE owner_id = ?`, userID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite: get delegates of: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var delegateID string
		var writeAccess int
		if err := rows.Scan(&delegateID, &writeAccess); err != nil {
			return nil, nil, fmt.Errorf("sqlite: scan delegation: %w", err)
		}
		if writeAccess == 1 {
			writeTo = append(writeTo, delegateID)
		} else {
			readTo = append(readTo, delegateID)
		}
	}
	return readTo, writeTo, rows.Err()
}
