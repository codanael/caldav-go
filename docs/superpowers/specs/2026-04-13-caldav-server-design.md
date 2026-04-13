# CalDAV Server Design Spec

## Overview

A CalDAV server in Go that follows RFC 4791 (CalDAV), RFC 4918 (WebDAV), RFC 5545 (iCalendar), and RFC 6578 (Collection Synchronization). Supports VEVENT and VTODO components for compatibility with Apple Calendar, Apple Reminders, and Google Calendar clients. Runs standalone or integrates as a library into existing Go applications.

Built on top of `emersion/go-webdav` for protocol handling. Fork only if upstream limitations block progress.

## Decisions

| Decision | Choice |
|----------|--------|
| Storage | Pluggable `Backend` interface, SQLite default via `database/sql` + `modernc.org/sqlite` (pure Go, no CGO) |
| ORM | No — hand-written SQL, simple schema |
| Auth | Basic Auth + OAuth 2.0 built-in, pluggable `AuthProvider` interface |
| Features | Core CalDAV + VTODO (no scheduling/RFC 6638 for now) |
| Config | YAML config file + env vars + CLI flags (precedence: CLI > env > file) |
| TLS | Built-in TLS with `autocert` + plain HTTP mode for reverse proxy setups |
| Base library | `emersion/go-webdav` as dependency |

## RFCs Implemented

- **RFC 4918** — WebDAV: PROPFIND, PROPPATCH, MKCOL, PUT, GET, DELETE, OPTIONS
- **RFC 4791** — CalDAV: MKCALENDAR, REPORT (calendar-query, calendar-multiget), calendar properties
- **RFC 5545** — iCalendar: VEVENT, VTODO parsing and generation
- **RFC 6578** — Collection Sync: sync-token, sync-collection REPORT

## Project Structure

```
caldav-go/
├── cmd/
│   └── caldav-server/          # Standalone binary
│       └── main.go
├── server/                     # Core library package
│   ├── server.go               # CalDAV server (http.Handler)
│   ├── options.go              # Functional options pattern
│   └── middleware.go           # Auth, logging, CORS middleware
├── auth/                       # Authentication
│   ├── provider.go             # AuthProvider interface
│   ├── basic.go                # Basic Auth implementation
│   └── oauth2.go               # OAuth 2.0 implementation
├── storage/                    # Storage layer
│   ├── backend.go              # Backend interface + types
│   └── sqlite/
│       ├── sqlite.go           # SQLite implementation
│       └── migrations.go       # Schema migrations
├── config/                     # Configuration
│   ├── config.go               # Config struct + loader (YAML + env + flags)
│   └── config.yaml.example
├── tls/                        # TLS/autocert helpers
│   └── tls.go
├── internal/
│   └── ical/                   # iCal helper utilities
├── testdata/                   # Test fixtures
├── go.mod
└── go.sum
```

## Storage Interface

```go
package storage

type Backend interface {
    // Calendar operations
    CreateCalendar(ctx context.Context, userID string, calendar *Calendar) error
    GetCalendar(ctx context.Context, userID string, path string) (*Calendar, error)
    ListCalendars(ctx context.Context, userID string) ([]Calendar, error)
    UpdateCalendar(ctx context.Context, userID string, path string, update *CalendarUpdate) error
    DeleteCalendar(ctx context.Context, userID string, path string) error

    // Calendar object operations (events + todos)
    PutObject(ctx context.Context, calendarPath string, object *CalendarObject) error
    GetObject(ctx context.Context, calendarPath string, uid string) (*CalendarObject, error)
    ListObjects(ctx context.Context, calendarPath string) ([]CalendarObject, error)
    DeleteObject(ctx context.Context, calendarPath string, uid string) error

    // Queries (REPORT support)
    QueryObjects(ctx context.Context, calendarPath string, query *Query) ([]CalendarObject, error)

    // Sync (RFC 6578)
    GetSyncToken(ctx context.Context, calendarPath string) (string, error)
    SyncCollection(ctx context.Context, calendarPath string, syncToken string) (*SyncResponse, error)
}

type Calendar struct {
    Path            string
    Name            string
    Description     string
    Color           string   // Apple X-APPLE-CALENDAR-COLOR extension
    Components      []string // "VEVENT", "VTODO"
    SyncToken       string
    MaxResourceSize int64
}

type CalendarUpdate struct {
    Name        *string
    Description *string
    Color       *string
}

type CalendarObject struct {
    Path     string
    ETag     string
    Data     []byte // Raw iCalendar data
    CompType string // "VEVENT" or "VTODO"
}

type Query struct {
    CompType  string
    TimeRange *TimeRange
    Props     []string
}

type TimeRange struct {
    Start time.Time
    End   time.Time
}

type SyncResponse struct {
    NewToken string
    Changed  []CalendarObject
    Deleted  []string
}
```

## Auth Interface

```go
package auth

type AuthProvider interface {
    // Authenticate validates credentials from the request.
    // Returns the user ID on success.
    Authenticate(ctx context.Context, r *http.Request) (userID string, err error)

    // CurrentUser returns user info for an authenticated request.
    CurrentUser(ctx context.Context, userID string) (*User, error)
}

type User struct {
    ID          string
    DisplayName string
    Email       string
}
```

### Basic Auth Implementation
- Configurable user store (in-memory map for standalone, interface for library use)
- Passwords stored as bcrypt hashes
- Responds with `WWW-Authenticate: Basic` on 401

### OAuth 2.0 Implementation
- Validates Bearer tokens against a configurable token endpoint or JWKS URL
- Supports standard JWT validation (issuer, audience, expiry)
- Extracts user ID from configurable JWT claim

## Config

```go
type Config struct {
    // Server
    ListenAddr string `yaml:"listen_addr" env:"CALDAV_LISTEN_ADDR" flag:"listen"`
    BasePath   string `yaml:"base_path" env:"CALDAV_BASE_PATH" flag:"base-path"`

    // TLS
    TLS       TLSConfig `yaml:"tls"`
    AutoCert  bool      `yaml:"auto_cert" env:"CALDAV_AUTO_CERT" flag:"auto-cert"`
    CertFile  string    `yaml:"cert_file" env:"CALDAV_CERT_FILE" flag:"cert-file"`
    KeyFile   string    `yaml:"key_file" env:"CALDAV_KEY_FILE" flag:"key-file"`
    ACMEHost  string    `yaml:"acme_host" env:"CALDAV_ACME_HOST" flag:"acme-host"`

    // Storage
    DBPath string `yaml:"db_path" env:"CALDAV_DB_PATH" flag:"db-path"`

    // Auth
    Auth AuthConfig `yaml:"auth"`
}

type AuthConfig struct {
    Provider string            `yaml:"provider"` // "basic" or "oauth2"
    Basic    BasicAuthConfig   `yaml:"basic"`
    OAuth2   OAuth2Config      `yaml:"oauth2"`
}
```

Precedence: CLI flags > environment variables > config file > defaults.
Config file path set via `--config` flag or `CALDAV_CONFIG` env var.

## Server (Library API)

```go
package server

// New creates a CalDAV server as an http.Handler.
func New(opts ...Option) http.Handler

// Options
func WithBackend(b storage.Backend) Option
func WithAuth(a auth.AuthProvider) Option
func WithBasePath(path string) Option
func WithLogger(logger *slog.Logger) Option
```

Library usage:

```go
import (
    "github.com/user/caldav-go/server"
    "github.com/user/caldav-go/storage/sqlite"
    "github.com/user/caldav-go/auth"
)

backend, _ := sqlite.New("caldav.db")
authProvider := auth.NewBasicProvider(users)

handler := server.New(
    server.WithBackend(backend),
    server.WithAuth(authProvider),
)
http.Handle("/caldav/", handler)
```

## SQLite Schema

```sql
CREATE TABLE calendars (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT NOT NULL,
    path        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT DEFAULT '',
    color       TEXT DEFAULT '',
    components  TEXT NOT NULL DEFAULT 'VEVENT,VTODO',
    sync_token  INTEGER NOT NULL DEFAULT 1,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE calendar_objects (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id   INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    path          TEXT NOT NULL,
    uid           TEXT NOT NULL,
    etag          TEXT NOT NULL,
    data          BLOB NOT NULL,
    comp_type     TEXT NOT NULL, -- 'VEVENT' or 'VTODO'
    start_time    DATETIME,     -- for time-range queries
    end_time      DATETIME,     -- for time-range queries
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(calendar_id, uid)
);

CREATE TABLE sync_changes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id  INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    object_path  TEXT NOT NULL,
    change_type  TEXT NOT NULL, -- 'created', 'modified', 'deleted'
    sync_token   INTEGER NOT NULL,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE users (
    id           TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    email        TEXT DEFAULT '',
    password     TEXT NOT NULL, -- bcrypt hash
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_calendar_objects_calendar ON calendar_objects(calendar_id);
CREATE INDEX idx_calendar_objects_comp ON calendar_objects(calendar_id, comp_type);
CREATE INDEX idx_calendar_objects_time ON calendar_objects(calendar_id, start_time, end_time);
CREATE INDEX idx_sync_changes_token ON sync_changes(calendar_id, sync_token);
CREATE INDEX idx_calendars_user ON calendars(user_id);
```

## Apple/Google Compatibility

### Apple Calendar & Reminders
- Return proper `DAV:` headers with `calendar-access` in OPTIONS
- Support `X-APPLE-CALENDAR-COLOR` property
- Return `WWW-Authenticate: Basic` on 401 (not 403)
- Support VTODO for Apple Reminders sync
- Implement sync-token for efficient collection sync

### Google Calendar
- Support PROPFIND/REPORT (no LOCK/UNLOCK/COPY/MOVE needed)
- Implement RFC 6578 collection synchronization (required by Google)
- VEVENT only (Google ignores VTODO)

## Testing Strategy

- Unit tests for each package (storage, auth, server)
- Integration tests using `net/http/httptest` with real CalDAV requests
- Apple CalDAVTester compliance suite (if available)
- Litmus WebDAV compliance suite for base WebDAV operations

## Out of Scope (Future Work)

- RFC 6638 scheduling (INBOX/OUTBOX, invites)
- VJOURNAL components
- WebDAV ACL (RFC 3744)
- CardDAV (contacts)
- Multi-server / clustering
