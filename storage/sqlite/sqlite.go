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

	if existingFound {
		_, err = b.db.ExecContext(ctx,
			`UPDATE calendar_objects SET uid=?, etag=?, ical_data=?, comp_type=?, start_time=?, end_time=?, size=?, updated_at=? WHERE id=?`,
			uid, etag, icalData, compType, startTime, endTime, size, now, existingID,
		)
	} else {
		_, err = b.db.ExecContext(ctx,
			`INSERT INTO calendar_objects (calendar_id, path, uid, etag, ical_data, comp_type, start_time, end_time, size, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			calID, path, uid, etag, icalData, compType, startTime, endTime, size, now, now,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: put calendar object: %w", err)
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

	result, err := b.db.ExecContext(ctx,
		`DELETE FROM calendar_objects WHERE path = ? AND calendar_id IN (
			SELECT id FROM calendars WHERE user_id = ?
		)`,
		path, userID,
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
	return nil
}
