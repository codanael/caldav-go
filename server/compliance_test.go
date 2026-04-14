package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codanael/caldav-go/auth"
	"github.com/codanael/caldav-go/storage/sqlite"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

// basicAuthHTTPClient wraps an http.Client to inject Basic Auth on every request.
type basicAuthHTTPClient struct {
	username, password string
}

func (c *basicAuthHTTPClient) Do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(c.username, c.password)
	return http.DefaultClient.Do(req)
}

func setupCompliance(t *testing.T) (*httptest.Server, *caldav.Client) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "compliance.db")
	backend, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { backend.Close() })

	authProvider := auth.NewBasicProvider()
	if err := authProvider.AddUser("testuser", "testpass", auth.User{
		ID:          "testuser",
		DisplayName: "Test User",
		Email:       "test@example.com",
	}); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	handler := New(
		WithBackend(backend),
		WithAuth(authProvider),
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	httpClient := &basicAuthHTTPClient{username: "testuser", password: "testpass"}
	client, err := caldav.NewClient(httpClient, ts.URL)
	if err != nil {
		t.Fatalf("caldav.NewClient: %v", err)
	}

	return ts, client
}

func makeTestEvent(uid, summary string, start, end time.Time) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Test//Test//EN")

	event := ical.NewComponent(ical.CompEvent)
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetText(ical.PropSummary, summary)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	event.Props.SetDateTime(ical.PropDateTimeStart, start)
	event.Props.SetDateTime(ical.PropDateTimeEnd, end)
	cal.Children = append(cal.Children, event)
	return cal
}

func makeTestTodo(uid, summary string, due time.Time) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Test//Test//EN")

	todo := ical.NewComponent(ical.CompToDo)
	todo.Props.SetText(ical.PropUID, uid)
	todo.Props.SetText(ical.PropSummary, summary)
	todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	todo.Props.SetDateTime(ical.PropDue, due)
	cal.Children = append(cal.Children, todo)
	return cal
}

// TestCompliance_WellKnownRedirect tests RFC 6764 well-known URL.
func TestCompliance_WellKnownRedirect(t *testing.T) {
	ts, _ := setupCompliance(t)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, _ := http.NewRequest("GET", ts.URL+"/.well-known/caldav", nil)
	req.SetBasicAuth("testuser", "testpass")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/caldav: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Errorf("expected 308 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Error("expected Location header")
	}
	if !strings.Contains(loc, "/testuser/") {
		t.Errorf("expected redirect to user principal, got %s", loc)
	}
}

// TestCompliance_OptionsDAVHeader tests that OPTIONS returns proper DAV headers.
func TestCompliance_OptionsDAVHeader(t *testing.T) {
	ts, _ := setupCompliance(t)

	req, _ := http.NewRequest("OPTIONS", ts.URL+"/testuser/calendars/", nil)
	req.SetBasicAuth("testuser", "testpass")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 200 or 204, got %d", resp.StatusCode)
	}

	dav := resp.Header.Get("DAV")
	if !strings.Contains(dav, "calendar-access") {
		t.Errorf("expected DAV header to contain 'calendar-access', got %q", dav)
	}

	allow := resp.Header.Get("Allow")
	if allow == "" {
		t.Error("expected Allow header")
	}
}

// TestCompliance_PrincipalDiscovery tests principal URL discovery via PROPFIND.
func TestCompliance_PrincipalDiscovery(t *testing.T) {
	ts, _ := setupCompliance(t)

	propfindBody := `<?xml version="1.0" encoding="UTF-8"?>
<propfind xmlns="DAV:">
  <prop>
    <current-user-principal/>
  </prop>
</propfind>`

	req, _ := http.NewRequest("PROPFIND", ts.URL+"/", strings.NewReader(propfindBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PROPFIND /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 207 {
		t.Fatalf("expected 207, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "/testuser/") {
		t.Errorf("expected current-user-principal with /testuser/, got:\n%s", body)
	}
}

// TestCompliance_CalendarHomeSetDiscovery tests calendar-home-set discovery.
func TestCompliance_CalendarHomeSetDiscovery(t *testing.T) {
	ts, _ := setupCompliance(t)

	propfindBody := `<?xml version="1.0" encoding="UTF-8"?>
<propfind xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <prop>
    <C:calendar-home-set/>
  </prop>
</propfind>`

	req, _ := http.NewRequest("PROPFIND", ts.URL+"/testuser/", strings.NewReader(propfindBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PROPFIND /testuser/: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 207 {
		t.Fatalf("expected 207, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "/testuser/calendars/") {
		t.Errorf("expected calendar-home-set with /testuser/calendars/, got:\n%s", body)
	}
}

// TestCompliance_CreateCalendarMKCOL tests MKCOL to create a calendar collection.
func TestCompliance_CreateCalendarMKCOL(t *testing.T) {
	ts, _ := setupCompliance(t)

	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set>
    <prop>
      <resourcetype>
        <collection/>
        <C:calendar/>
      </resourcetype>
      <displayname>My Calendar</displayname>
    </prop>
  </set>
</mkcol>`

	req, _ := http.NewRequest("MKCOL", ts.URL+"/testuser/calendars/default/", strings.NewReader(mkcolBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("MKCOL: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

// TestCompliance_ClientFindCalendars tests using the go-webdav client to find calendars.
func TestCompliance_ClientFindCalendars(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()

	// Create a calendar first
	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set><prop>
    <resourcetype><collection/><C:calendar/></resourcetype>
    <displayname>Work</displayname>
  </prop></set>
</mkcol>`

	req, _ := http.NewRequest("MKCOL", ts.URL+"/testuser/calendars/work/", strings.NewReader(mkcolBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("MKCOL: %v", err)
	}
	resp.Body.Close()

	// Use client to discover calendars
	cals, err := client.FindCalendars(ctx, "/testuser/calendars/")
	if err != nil {
		t.Fatalf("FindCalendars: %v", err)
	}

	if len(cals) != 1 {
		t.Fatalf("expected 1 calendar, got %d", len(cals))
	}
	if cals[0].Name != "Work" {
		t.Errorf("expected calendar name 'Work', got %q", cals[0].Name)
	}
}

// TestCompliance_ClientPutGetCalendarObject tests PUT and GET via client.
func TestCompliance_ClientPutGetCalendarObject(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()

	// Create calendar
	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set><prop>
    <resourcetype><collection/><C:calendar/></resourcetype>
    <displayname>Work</displayname>
  </prop></set>
</mkcol>`
	req, _ := http.NewRequest("MKCOL", ts.URL+"/testuser/calendars/work/", strings.NewReader(mkcolBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("MKCOL: %v", err)
	}
	resp.Body.Close()

	// PUT event via client
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)
	eventCal := makeTestEvent("meeting-1", "All Hands Meeting", start, end)

	co, err := client.PutCalendarObject(ctx, "/testuser/calendars/work/meeting-1.ics", eventCal)
	if err != nil {
		t.Fatalf("PutCalendarObject: %v", err)
	}
	if co.ETag == "" {
		t.Error("expected non-empty ETag from PUT")
	}

	// GET event via client
	got, err := client.GetCalendarObject(ctx, "/testuser/calendars/work/meeting-1.ics")
	if err != nil {
		t.Fatalf("GetCalendarObject: %v", err)
	}
	if got.Data == nil {
		t.Fatal("expected non-nil Data")
	}

	// Verify event content
	found := false
	for _, child := range got.Data.Children {
		if child.Name == ical.CompEvent {
			summary, _ := child.Props.Text(ical.PropSummary)
			if summary == "All Hands Meeting" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected to find 'All Hands Meeting' event in GET response")
	}
}

// TestCompliance_ClientQueryCalendar tests REPORT calendar-query via client.
func TestCompliance_ClientQueryCalendar(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()

	// Create calendar
	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set><prop>
    <resourcetype><collection/><C:calendar/></resourcetype>
    <displayname>Mixed</displayname>
  </prop></set>
</mkcol>`
	req, _ := http.NewRequest("MKCOL", ts.URL+"/testuser/calendars/mixed/", strings.NewReader(mkcolBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	// PUT an event
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	_, err := client.PutCalendarObject(ctx, "/testuser/calendars/mixed/event-1.ics", makeTestEvent("event-1", "Morning Standup", start, end))
	if err != nil {
		t.Fatalf("PutCalendarObject(event): %v", err)
	}

	// PUT a todo
	due := time.Date(2026, 6, 15, 17, 0, 0, 0, time.UTC)
	_, err = client.PutCalendarObject(ctx, "/testuser/calendars/mixed/todo-1.ics", makeTestTodo("todo-1", "Buy groceries", due))
	if err != nil {
		t.Fatalf("PutCalendarObject(todo): %v", err)
	}

	// Query only VEVENTs
	results, err := client.QueryCalendar(ctx, "/testuser/calendars/mixed/", &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     ical.CompCalendar,
			AllProps: true,
			Comps: []caldav.CalendarCompRequest{{
				Name:     ical.CompEvent,
				AllProps: true,
			}},
		},
		CompFilter: caldav.CompFilter{
			Name: ical.CompCalendar,
			Comps: []caldav.CompFilter{{
				Name: ical.CompEvent,
			}},
		},
	})
	if err != nil {
		t.Fatalf("QueryCalendar(VEVENT): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 VEVENT result, got %d", len(results))
	}

	// Query only VTODOs
	todoResults, err := client.QueryCalendar(ctx, "/testuser/calendars/mixed/", &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     ical.CompCalendar,
			AllProps: true,
			Comps: []caldav.CalendarCompRequest{{
				Name:     ical.CompToDo,
				AllProps: true,
			}},
		},
		CompFilter: caldav.CompFilter{
			Name: ical.CompCalendar,
			Comps: []caldav.CompFilter{{
				Name: ical.CompToDo,
			}},
		},
	})
	if err != nil {
		t.Fatalf("QueryCalendar(VTODO): %v", err)
	}
	if len(todoResults) != 1 {
		t.Fatalf("expected 1 VTODO result, got %d", len(todoResults))
	}
}

// TestCompliance_ClientMultiGet tests REPORT calendar-multiget via client.
func TestCompliance_ClientMultiGet(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()

	// Create calendar and events
	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set><prop>
    <resourcetype><collection/><C:calendar/></resourcetype>
    <displayname>Work</displayname>
  </prop></set>
</mkcol>`
	req, _ := http.NewRequest("MKCOL", ts.URL+"/testuser/calendars/work/", strings.NewReader(mkcolBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	start := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC)
	_, err := client.PutCalendarObject(ctx, "/testuser/calendars/work/event-a.ics", makeTestEvent("event-a", "Event A", start, end))
	if err != nil {
		t.Fatalf("put event-a: %v", err)
	}
	_, err = client.PutCalendarObject(ctx, "/testuser/calendars/work/event-b.ics", makeTestEvent("event-b", "Event B", start.Add(time.Hour), end.Add(time.Hour)))
	if err != nil {
		t.Fatalf("put event-b: %v", err)
	}

	// Multiget specific events
	results, err := client.MultiGetCalendar(ctx, "/testuser/calendars/work/", &caldav.CalendarMultiGet{
		Paths: []string{
			"/testuser/calendars/work/event-a.ics",
			"/testuser/calendars/work/event-b.ics",
		},
		CompRequest: caldav.CalendarCompRequest{
			Name:     ical.CompCalendar,
			AllProps: true,
			Comps: []caldav.CalendarCompRequest{{
				Name:     ical.CompEvent,
				AllProps: true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("MultiGetCalendar: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

// TestCompliance_PutDeleteVerify tests the full create/delete lifecycle.
func TestCompliance_PutDeleteVerify(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()

	// Create calendar
	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set><prop>
    <resourcetype><collection/><C:calendar/></resourcetype>
    <displayname>Trash</displayname>
  </prop></set>
</mkcol>`
	req, _ := http.NewRequest("MKCOL", ts.URL+"/testuser/calendars/trash/", strings.NewReader(mkcolBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	// PUT
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	_, err := client.PutCalendarObject(ctx, "/testuser/calendars/trash/temp.ics", makeTestEvent("temp", "Temporary", start, end))
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}

	// DELETE via raw HTTP
	deleteReq, _ := http.NewRequest("DELETE", ts.URL+"/testuser/calendars/trash/temp.ics", nil)
	deleteReq.SetBasicAuth("testuser", "testpass")
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	deleteResp.Body.Close()

	if deleteResp.StatusCode != http.StatusNoContent && deleteResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 or 204 for DELETE, got %d", deleteResp.StatusCode)
	}

	// GET should 404
	_, err = client.GetCalendarObject(ctx, "/testuser/calendars/trash/temp.ics")
	if err == nil {
		t.Fatal("expected error after DELETE")
	}
}

// TestCompliance_UpdateEvent tests PUT to update an existing event.
func TestCompliance_UpdateEvent(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()

	// Create calendar
	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set><prop>
    <resourcetype><collection/><C:calendar/></resourcetype>
    <displayname>Work</displayname>
  </prop></set>
</mkcol>`
	req, _ := http.NewRequest("MKCOL", ts.URL+"/testuser/calendars/work/", strings.NewReader(mkcolBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	// PUT initial version
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	co1, err := client.PutCalendarObject(ctx, "/testuser/calendars/work/update-me.ics", makeTestEvent("update-me", "Original Title", start, end))
	if err != nil {
		t.Fatalf("PUT initial: %v", err)
	}

	// PUT updated version (same path, same UID)
	co2, err := client.PutCalendarObject(ctx, "/testuser/calendars/work/update-me.ics", makeTestEvent("update-me", "Updated Title", start, end.Add(time.Hour)))
	if err != nil {
		t.Fatalf("PUT update: %v", err)
	}

	// ETags should differ
	if co1.ETag == co2.ETag {
		t.Error("expected different ETags after update")
	}

	// GET should return updated version
	got, err := client.GetCalendarObject(ctx, "/testuser/calendars/work/update-me.ics")
	if err != nil {
		t.Fatalf("GET after update: %v", err)
	}

	for _, child := range got.Data.Children {
		if child.Name == ical.CompEvent {
			summary, _ := child.Props.Text(ical.PropSummary)
			if summary != "Updated Title" {
				t.Errorf("expected 'Updated Title', got %q", summary)
			}
		}
	}
}
