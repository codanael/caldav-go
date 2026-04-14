# caldav-go

A CalDAV server in Go. Runs standalone or integrates as a library into existing Go applications.

Implements RFC 4791 (CalDAV), RFC 4918 (WebDAV), RFC 5545 (iCalendar), and RFC 6578 (Collection Synchronization). Tested for compatibility with Apple Calendar, Apple Reminders (via VTODO), and Google Calendar.

## Features

- VEVENT and VTODO support (events and tasks)
- Sync-token and CTag for efficient incremental sync
- PROPFIND, PROPPATCH, MKCOL, PUT, GET, DELETE, OPTIONS, REPORT
- calendar-query, calendar-multiget, sync-collection reports
- Basic Auth (bcrypt) and OAuth 2.0 (JWT/JWKS) authentication
- SQLite and PostgreSQL storage backends
- Calendar color, delegation, and scheduling URL support
- Let's Encrypt auto-TLS or manual certificate configuration
- 56 tests including a full Apple Calendar lifecycle simulation

## Quick Start

### Standalone Server

```bash
go install github.com/codanael/caldav-go/cmd/caldav-server@latest

# Start with SQLite (default)
caldav-server --listen :8080 --db-path ./caldav.db

# Start with PostgreSQL
caldav-server --listen :8080 --db-driver postgres \
  --db-path "postgres://user:pass@localhost:5432/caldav?sslmode=disable"
```

Create a config file for persistent settings:

```yaml
listen_addr: ":8080"
db_driver: "sqlite"       # or "postgres"
db_path: "./caldav.db"    # or postgres connection string
log_level: "info"

auth:
  provider: "basic"
  basic:
    users:
      alice:
        password: "secret"
        display_name: "Alice"
        email: "alice@example.com"
```

```bash
caldav-server --config config.yaml
```

### Connect Apple Calendar

1. Open **System Settings > Internet Accounts > Add Other Account > CalDAV**
2. Select **Manual** configuration
3. Enter:
   - Server: `http://localhost:8080` (or your domain with HTTPS)
   - Username: `alice`
   - Password: `secret`

---

## Library Integration

The primary use case is embedding the CalDAV server into an existing Go application.

### Minimal Example

```go
package main

import (
    "log"
    "net/http"

    "github.com/codanael/caldav-go/auth"
    "github.com/codanael/caldav-go/server"
    "github.com/codanael/caldav-go/storage/sqlite"
)

func main() {
    // 1. Create a storage backend
    backend, err := sqlite.New("caldav.db")
    if err != nil {
        log.Fatal(err)
    }
    defer backend.Close()

    // 2. Create an auth provider
    authProvider := auth.NewBasicProvider()
    authProvider.AddUser("alice", "secret", auth.User{
        ID:          "alice",
        DisplayName: "Alice",
        Email:       "alice@example.com",
    })

    // 3. Create the CalDAV handler
    handler := server.New(
        server.WithBackend(backend),
        server.WithAuth(authProvider),
    )

    // 4. Mount and serve
    log.Println("CalDAV server on :8080")
    log.Fatal(http.ListenAndServe(":8080", handler))
}
```

### Mount Under a Subpath

```go
mux := http.NewServeMux()

caldavHandler := server.New(
    server.WithBackend(backend),
    server.WithAuth(authProvider),
    server.WithPrefix("/caldav"),
)

mux.Handle("/caldav/", caldavHandler)
mux.Handle("/", yourAppHandler)

http.ListenAndServe(":8080", mux)
```

Apple Calendar would connect to `http://localhost:8080/caldav/`.

---

## Packages

### `server` -- CalDAV HTTP Handler

```go
import "github.com/codanael/caldav-go/server"
```

Creates an `http.Handler` that speaks the CalDAV protocol.

```go
handler := server.New(opts ...server.Option) http.Handler
```

**Options:**

| Function | Description |
|----------|-------------|
| `server.WithBackend(b)` | Storage backend (required). Accepts `caldav.Backend` or `storage.ExtendedBackend`. |
| `server.WithAuth(a)` | Authentication provider. Without this, all requests are unauthenticated. |
| `server.WithPrefix(p)` | URL prefix when mounted under a subpath (e.g., `"/caldav"`). |
| `server.WithLogger(l)` | Structured logger (`*slog.Logger`). Defaults to stderr. |

The handler automatically detects if the backend implements `storage.ExtendedBackend` and enables PROPPATCH, calendar deletion, sync-token in PROPFIND, CTag, calendar color, supported-report-set, scheduling URLs, and calendar delegation.

---

### `auth` -- Authentication

```go
import "github.com/codanael/caldav-go/auth"
```

#### Provider Interface

Implement this to plug in your own authentication:

```go
type Provider interface {
    Authenticate(r *http.Request) (*User, error)
    Challenge() string  // WWW-Authenticate header value
}

type User struct {
    ID          string
    DisplayName string
    Email       string
}
```

#### Basic Auth

```go
provider := auth.NewBasicProvider()

err := provider.AddUser("username", "plaintext-password", auth.User{
    ID:          "username",
    DisplayName: "Display Name",
    Email:       "user@example.com",
})
```

Passwords are bcrypt-hashed internally. The `Challenge()` method returns `Basic realm="CalDAV"`.

#### OAuth 2.0 (JWT)

```go
provider := auth.NewOAuth2Provider(auth.OAuth2Options{
    JWKSURL:     "https://auth.example.com/.well-known/jwks.json",
    Issuer:      "https://auth.example.com",
    Audience:    "my-caldav-server",
    UserIDClaim: "sub",  // JWT claim containing the user ID
})
```

Validates Bearer tokens by fetching the JWKS, verifying the JWT signature (RS256/RS384/RS512, ES256/ES384/ES512), and checking `exp`, `iss`, and `aud` claims. The user ID is extracted from the configured claim (default `"sub"`).

#### Custom Auth

```go
type MyAuthProvider struct{}

func (p *MyAuthProvider) Authenticate(r *http.Request) (*auth.User, error) {
    token := r.Header.Get("X-API-Key")
    user, err := myUserStore.ValidateToken(token)
    if err != nil {
        return nil, err
    }
    return &auth.User{
        ID:          user.ID,
        DisplayName: user.Name,
        Email:       user.Email,
    }, nil
}

func (p *MyAuthProvider) Challenge() string {
    return `Bearer realm="CalDAV"`
}
```

---

### `storage` -- Backend Interface

```go
import "github.com/codanael/caldav-go/storage"
```

The storage package defines interfaces and types. You don't use it directly unless building a custom backend.

#### Interface Hierarchy

```
caldav.Backend                  (from go-webdav, core CalDAV operations)
  +-- storage.SyncBackend      (adds sync-token support)
        +-- storage.ExtendedBackend  (adds PROPPATCH, calendar deletion, delegation)
```

The server automatically detects which level your backend implements and enables features accordingly.

#### `storage.ExtendedBackend`

The full interface (implemented by both SQLite and PostgreSQL backends):

```go
type ExtendedBackend interface {
    // From caldav.Backend:
    CalendarHomeSetPath(ctx context.Context) (string, error)
    CurrentUserPrincipal(ctx context.Context) (string, error)
    CreateCalendar(ctx context.Context, calendar *caldav.Calendar) error
    ListCalendars(ctx context.Context) ([]caldav.Calendar, error)
    GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error)
    GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error)
    ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error)
    QueryCalendarObjects(ctx context.Context, path string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error)
    PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error)
    DeleteCalendarObject(ctx context.Context, path string) error

    // From storage.SyncBackend:
    GetSyncToken(ctx context.Context, calendarPath string) (string, error)
    SyncCollection(ctx context.Context, calendarPath string, syncToken string) (*SyncResponse, error)

    // Extended operations:
    UpdateCalendar(ctx context.Context, path string, update *CalendarUpdate) error
    DeleteCalendar(ctx context.Context, path string) error
    GetCalendarExtra(ctx context.Context, path string) (*CalendarExtra, error)
    AddDelegation(ctx context.Context, d Delegation) error
    RemoveDelegation(ctx context.Context, ownerID, delegateID string) error
    GetDelegatesFor(ctx context.Context, userID string) (readFrom []string, writeFrom []string, err error)
    GetDelegatesOf(ctx context.Context, userID string) (readTo []string, writeTo []string, err error)
}
```

#### User Context

The backend gets the authenticated user ID from the request context. The auth middleware sets this automatically. If you need to call backend methods directly:

```go
ctx := storage.ContextWithUser(context.Background(), "alice")
calendars, err := backend.ListCalendars(ctx)
```

#### URL Path Scheme

```
/{userID}/                              User principal
/{userID}/calendars/                    Calendar home set
/{userID}/calendars/{calendarName}/     Calendar collection
/{userID}/calendars/{calendarName}/{uid}.ics   Calendar object
```

---

### `storage/sqlite` -- SQLite Backend

```go
import "github.com/codanael/caldav-go/storage/sqlite"
```

```go
backend, err := sqlite.New("/path/to/caldav.db")
if err != nil {
    log.Fatal(err)
}
defer backend.Close()
```

Uses [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (pure Go, no CGO required). Enables WAL mode and foreign keys automatically. Schema is created on first run.

---

### `storage/postgres` -- PostgreSQL Backend

```go
import "github.com/codanael/caldav-go/storage/postgres"
```

```go
backend, err := postgres.New("postgres://user:pass@localhost:5432/caldav?sslmode=disable")
if err != nil {
    log.Fatal(err)
}
defer backend.Close()
```

Uses [lib/pq](https://pkg.go.dev/github.com/lib/pq). Schema is created on first run via `CREATE TABLE IF NOT EXISTS`.

---

### `storage/custom` -- Build Your Own Backend

Implement `caldav.Backend` for basic CalDAV support, or `storage.ExtendedBackend` for full features:

```go
type MyBackend struct {
    // your storage (Redis, MongoDB, API, etc.)
}

// Required: caldav.Backend methods
func (b *MyBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
    userID := storage.UserFromContext(ctx)
    return "/" + userID + "/calendars/", nil
}

func (b *MyBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
    userID := storage.UserFromContext(ctx)
    return "/" + userID + "/", nil
}

// ... implement remaining methods
```

Pass your backend to `server.New()`:

```go
handler := server.New(server.WithBackend(&MyBackend{}))
```

---

### `config` -- Configuration Loading

```go
import "github.com/codanael/caldav-go/config"
```

Layered configuration with precedence: CLI flags > environment variables > config file > defaults.

```go
cfg, err := config.Load("/path/to/config.yaml", os.Args[1:])
```

**Environment variables** (prefix `CALDAV_`):

| Variable | Description |
|----------|-------------|
| `CALDAV_LISTEN_ADDR` | Listen address (e.g., `:8080`) |
| `CALDAV_DB_DRIVER` | `sqlite` or `postgres` |
| `CALDAV_DB_PATH` | Database path or connection string |
| `CALDAV_BASE_PATH` | URL prefix |
| `CALDAV_LOG_LEVEL` | `debug`, `info`, `warn`, `error` |
| `CALDAV_AUTH_PROVIDER` | `basic` or `oauth2` |
| `CALDAV_TLS_ENABLED` | `true` / `false` |
| `CALDAV_TLS_AUTO_CERT` | `true` / `false` |
| `CALDAV_TLS_CERT_FILE` | Path to TLS certificate |
| `CALDAV_TLS_KEY_FILE` | Path to TLS private key |
| `CALDAV_TLS_ACME_HOST` | Hostname for Let's Encrypt |
| `CALDAV_CONFIG` | Path to config file |

---

### `tls` -- TLS Configuration

```go
import "github.com/codanael/caldav-go/tls"
```

#### Let's Encrypt Auto-TLS

```go
tlsCfg, err := tls.NewTLSConfig(tls.Config{
    AutoCert: true,
    ACMEHost: "caldav.example.com",
    CacheDir: "/var/cache/caldav-certs",
})
```

#### Manual Certificate

```go
tlsCfg, err := tls.NewTLSConfig(tls.Config{
    CertFile: "/etc/ssl/caldav.pem",
    KeyFile:  "/etc/ssl/caldav-key.pem",
})
```

#### Serve with TLS

```go
tls.ListenAndServeTLS(":443", handler, tlsCfg)
```

---

## Deployment Examples

### Docker with PostgreSQL

```yaml
# docker-compose.yml
services:
  caldav:
    build: .
    ports:
      - "8080:8080"
    environment:
      CALDAV_DB_DRIVER: postgres
      CALDAV_DB_PATH: postgres://caldav:secret@db:5432/caldav?sslmode=disable
      CALDAV_AUTH_PROVIDER: basic
    depends_on:
      - db

  db:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: caldav
      POSTGRES_PASSWORD: secret
      POSTGRES_DB: caldav
    volumes:
      - pgdata:/var/lib/postgresql/data

volumes:
  pgdata:
```

### Behind Nginx

```nginx
server {
    listen 443 ssl;
    server_name caldav.example.com;

    ssl_certificate /etc/ssl/caldav.pem;
    ssl_certificate_key /etc/ssl/caldav-key.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

### Integrate into an Existing App

```go
func main() {
    // Your existing app setup
    db := connectToYourDatabase()
    mux := http.NewServeMux()

    // Your existing routes
    mux.HandleFunc("/api/", yourAPIHandler)
    mux.HandleFunc("/", yourWebHandler)

    // Add CalDAV alongside your app
    caldavBackend, _ := sqlite.New("caldav.db")
    defer caldavBackend.Close()

    caldavAuth := auth.NewBasicProvider()
    // Sync users from your app's user store
    for _, user := range loadUsersFromDB(db) {
        caldavAuth.AddUser(user.Username, user.CalDAVPassword, auth.User{
            ID: user.ID, DisplayName: user.Name, Email: user.Email,
        })
    }

    caldavHandler := server.New(
        server.WithBackend(caldavBackend),
        server.WithAuth(caldavAuth),
        server.WithPrefix("/caldav"),
    )
    mux.Handle("/caldav/", caldavHandler)

    http.ListenAndServe(":8080", mux)
}
```

---

## RFC Compliance

| RFC | Description | Status |
|-----|-------------|--------|
| [4918](https://tools.ietf.org/html/rfc4918) | WebDAV | PROPFIND, PROPPATCH, MKCOL, PUT, GET, DELETE, OPTIONS |
| [4791](https://tools.ietf.org/html/rfc4791) | CalDAV | MKCALENDAR, calendar-query, calendar-multiget REPORT |
| [5545](https://tools.ietf.org/html/rfc5545) | iCalendar | VEVENT, VTODO, VALARM, RRULE (transparent storage) |
| [6578](https://tools.ietf.org/html/rfc6578) | Collection Sync | sync-token, sync-collection REPORT, CTag |
| [6638](https://tools.ietf.org/html/rfc6638) | Scheduling | schedule-inbox-URL, schedule-outbox-URL (stub paths) |
| [6764](https://tools.ietf.org/html/rfc6764) | Discovery | `/.well-known/caldav` redirect |

### Apple Calendar Extensions

- `X-APPLE-CALENDAR-COLOR` -- calendar color property
- `CS:getctag` -- Calendar Server CTag for change detection
- `supported-report-set` -- advertises available REPORT types
- `calendar-proxy-read-for` / `calendar-proxy-write-for` -- delegation

---

## Testing

```bash
# All tests (SQLite-based, no external dependencies)
go test ./...

# PostgreSQL tests (requires Docker)
go test ./storage/postgres/ -v

# Just the Apple Calendar simulation
go test ./server/ -run TestAppleCalendarSimulation -v
```

## License

MIT
