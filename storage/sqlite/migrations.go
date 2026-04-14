package sqlite

import "database/sql"

const schema = `
CREATE TABLE IF NOT EXISTS calendars (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    path TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    description TEXT DEFAULT '',
    color TEXT DEFAULT '',
    components TEXT NOT NULL DEFAULT 'VEVENT,VTODO',
    sync_token INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS calendar_objects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    path TEXT NOT NULL UNIQUE,
    uid TEXT NOT NULL,
    etag TEXT NOT NULL,
    ical_data TEXT NOT NULL,
    comp_type TEXT NOT NULL,
    start_time DATETIME,
    end_time DATETIME,
    size INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(calendar_id, uid)
);

CREATE TABLE IF NOT EXISTS sync_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    object_path TEXT NOT NULL,
    change_type TEXT NOT NULL,
    sync_token INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_calendars_user ON calendars(user_id);
CREATE INDEX IF NOT EXISTS idx_objects_calendar ON calendar_objects(calendar_id);
CREATE INDEX IF NOT EXISTS idx_objects_comp ON calendar_objects(calendar_id, comp_type);
CREATE INDEX IF NOT EXISTS idx_objects_time ON calendar_objects(calendar_id, start_time, end_time);
CREATE INDEX IF NOT EXISTS idx_sync_changes_token ON sync_changes(calendar_id, sync_token);
`

func migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}
