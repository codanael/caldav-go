package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codanael/caldav-go/auth"
	"github.com/codanael/caldav-go/storage/sqlite"
)

func setupTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	backend, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { backend.Close() })

	authProvider := auth.NewBasicProvider()
	if err := authProvider.AddUser("alice", "secret", auth.User{
		ID:          "alice",
		DisplayName: "Alice",
		Email:       "alice@example.com",
	}); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	handler := New(
		WithBackend(backend),
		WithAuth(authProvider),
	)

	return httptest.NewServer(handler)
}

func authRequest(method, url, body string) *http.Request {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, bodyReader)
	req.SetBasicAuth("alice", "secret")
	return req
}

func TestServer_Unauthorized(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/alice/calendars/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header")
	}
}

func TestServer_WellKnown(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req := authRequest("GET", ts.URL+"/.well-known/caldav", "")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/caldav: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Errorf("expected 308, got %d", resp.StatusCode)
	}
}

func TestServer_MKCALENDAR_And_PROPFIND(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.Close()
	client := ts.Client()

	// Create a calendar via MKCOL
	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set>
    <prop>
      <resourcetype>
        <collection/>
        <C:calendar/>
      </resourcetype>
      <displayname>Work</displayname>
    </prop>
  </set>
</mkcol>`

	req := authRequest("MKCOL", ts.URL+"/alice/calendars/work/", mkcolBody)
	req.Header.Set("Content-Type", "application/xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("MKCOL: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL expected 201, got %d", resp.StatusCode)
	}

	// PROPFIND on calendar home set
	propfindBody := `<?xml version="1.0" encoding="UTF-8"?>
<propfind xmlns="DAV:">
  <prop>
    <displayname/>
    <resourcetype/>
  </prop>
</propfind>`

	req = authRequest("PROPFIND", ts.URL+"/alice/calendars/", propfindBody)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 207 {
		t.Fatalf("PROPFIND expected 207, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Work") {
		t.Errorf("PROPFIND response should contain calendar name 'Work': %s", body)
	}
}

func TestServer_PutGetDelete(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.Close()
	client := ts.Client()

	// Create calendar
	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set>
    <prop>
      <resourcetype>
        <collection/>
        <C:calendar/>
      </resourcetype>
      <displayname>Work</displayname>
    </prop>
  </set>
</mkcol>`
	req := authRequest("MKCOL", ts.URL+"/alice/calendars/work/", mkcolBody)
	req.Header.Set("Content-Type", "application/xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("MKCOL: %v", err)
	}
	resp.Body.Close()

	// PUT event
	icalData := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//Test//EN\r\nBEGIN:VEVENT\r\nUID:test-event-1\r\nDTSTAMP:20260415T100000Z\r\nDTSTART:20260415T100000Z\r\nDTEND:20260415T110000Z\r\nSUMMARY:Team Meeting\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	req = authRequest("PUT", ts.URL+"/alice/calendars/work/test-event-1.ics", icalData)
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT expected 201, got %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Error("expected ETag header on PUT response")
	}

	// GET event
	req = authRequest("GET", ts.URL+"/alice/calendars/work/test-event-1.ics", "")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Team Meeting") {
		t.Errorf("GET response should contain 'Team Meeting': %s", body)
	}

	// DELETE event
	req = authRequest("DELETE", ts.URL+"/alice/calendars/work/test-event-1.ics", "")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE expected 200 or 204, got %d", resp.StatusCode)
	}

	// GET after delete should 404
	req = authRequest("GET", ts.URL+"/alice/calendars/work/test-event-1.ics", "")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET after delete: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", resp.StatusCode)
	}
}
