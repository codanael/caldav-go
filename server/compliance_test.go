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

	"encoding/xml"

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

// syncMultistatusResp is used to parse sync-collection REPORT responses.
type syncMultistatusResp struct {
	XMLName   xml.Name           `xml:"multistatus"`
	Responses []syncRespEntry    `xml:"response"`
	SyncToken string             `xml:"sync-token"`
}

type syncRespEntry struct {
	Href     string          `xml:"href"`
	Status   string          `xml:"status"`
	PropStat *syncPropStatResp `xml:"propstat"`
}

type syncPropStatResp struct {
	Prop   syncPropResp `xml:"prop"`
	Status string       `xml:"status"`
}

type syncPropResp struct {
	GetETag string `xml:"getetag"`
}

func createCalendar(t *testing.T, tsURL, path string) {
	t.Helper()
	mkcolBody := `<?xml version="1.0" encoding="UTF-8"?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set><prop>
    <resourcetype><collection/><C:calendar/></resourcetype>
    <displayname>Sync Test</displayname>
  </prop></set>
</mkcol>`
	req, _ := http.NewRequest("MKCOL", tsURL+path, strings.NewReader(mkcolBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("MKCOL: %v", err)
	}
	resp.Body.Close()
}

func syncCollection(t *testing.T, tsURL, calPath, syncToken string) *syncMultistatusResp {
	t.Helper()
	syncBody := `<?xml version="1.0" encoding="UTF-8"?>
<sync-collection xmlns="DAV:">
  <sync-token>` + syncToken + `</sync-token>
  <sync-level>1</sync-level>
  <prop>
    <getetag/>
  </prop>
</sync-collection>`

	req, _ := http.NewRequest("REPORT", tsURL+calPath, strings.NewReader(syncBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("REPORT sync-collection: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d: %s", resp.StatusCode, body)
	}

	var ms syncMultistatusResp
	if err := xml.Unmarshal(body, &ms); err != nil {
		t.Fatalf("unmarshal sync response: %v\n%s", err, body)
	}
	return &ms
}

// TestCompliance_SyncCollection_InitialSync tests initial sync with empty token.
func TestCompliance_SyncCollection_InitialSync(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()
	calPath := "/testuser/calendars/sync1/"
	createCalendar(t, ts.URL, calPath)

	// Add two events
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	_, err := client.PutCalendarObject(ctx, calPath+"event-1.ics", makeTestEvent("ev1", "Event 1", start, end))
	if err != nil {
		t.Fatalf("put event-1: %v", err)
	}
	_, err = client.PutCalendarObject(ctx, calPath+"event-2.ics", makeTestEvent("ev2", "Event 2", start.Add(time.Hour), end.Add(time.Hour)))
	if err != nil {
		t.Fatalf("put event-2: %v", err)
	}

	// Initial sync (empty token) should return all objects
	ms := syncCollection(t, ts.URL, calPath, "")
	if len(ms.Responses) != 2 {
		t.Fatalf("expected 2 responses on initial sync, got %d", len(ms.Responses))
	}
	if ms.SyncToken == "" {
		t.Fatal("expected non-empty sync token")
	}

	// All responses should have ETags (not deleted)
	for _, r := range ms.Responses {
		if r.PropStat == nil {
			t.Errorf("expected propstat for %s", r.Href)
		}
	}
}

// TestCompliance_SyncCollection_IncrementalSync tests sync after changes.
func TestCompliance_SyncCollection_IncrementalSync(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()
	calPath := "/testuser/calendars/sync2/"
	createCalendar(t, ts.URL, calPath)

	// Add an event
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	_, err := client.PutCalendarObject(ctx, calPath+"event-1.ics", makeTestEvent("ev1", "Event 1", start, end))
	if err != nil {
		t.Fatalf("put event-1: %v", err)
	}

	// Get initial sync token
	ms1 := syncCollection(t, ts.URL, calPath, "")
	token := ms1.SyncToken

	// Add another event
	_, err = client.PutCalendarObject(ctx, calPath+"event-2.ics", makeTestEvent("ev2", "Event 2", start.Add(time.Hour), end.Add(time.Hour)))
	if err != nil {
		t.Fatalf("put event-2: %v", err)
	}

	// Incremental sync should only return the new event
	ms2 := syncCollection(t, ts.URL, calPath, token)
	if len(ms2.Responses) != 1 {
		t.Fatalf("expected 1 change, got %d", len(ms2.Responses))
	}
	if !strings.Contains(ms2.Responses[0].Href, "event-2") {
		t.Errorf("expected event-2 in response, got %s", ms2.Responses[0].Href)
	}
	if ms2.SyncToken == token {
		t.Error("sync token should have advanced")
	}
}

// TestCompliance_SyncCollection_DeletedObjects tests that deleted objects show as 404.
func TestCompliance_SyncCollection_DeletedObjects(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()
	calPath := "/testuser/calendars/sync3/"
	createCalendar(t, ts.URL, calPath)

	// Add and sync
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	_, err := client.PutCalendarObject(ctx, calPath+"event-1.ics", makeTestEvent("ev1", "Event 1", start, end))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	ms1 := syncCollection(t, ts.URL, calPath, "")
	token := ms1.SyncToken

	// Delete the event
	delReq, _ := http.NewRequest("DELETE", ts.URL+calPath+"event-1.ics", nil)
	delReq.SetBasicAuth("testuser", "testpass")
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	delResp.Body.Close()

	// Sync should show the deleted object with 404 status
	ms2 := syncCollection(t, ts.URL, calPath, token)
	if len(ms2.Responses) != 1 {
		t.Fatalf("expected 1 change (delete), got %d", len(ms2.Responses))
	}
	r := ms2.Responses[0]
	if !strings.Contains(r.Href, "event-1") {
		t.Errorf("expected event-1, got %s", r.Href)
	}
	if !strings.Contains(r.Status, "404") {
		t.Errorf("expected 404 status for deleted object, got %q", r.Status)
	}
}

// TestCompliance_SyncCollection_NoChanges tests sync when nothing changed.
func TestCompliance_SyncCollection_NoChanges(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()
	calPath := "/testuser/calendars/sync4/"
	createCalendar(t, ts.URL, calPath)

	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	_, err := client.PutCalendarObject(ctx, calPath+"event-1.ics", makeTestEvent("ev1", "Event 1", start, end))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	ms1 := syncCollection(t, ts.URL, calPath, "")
	token := ms1.SyncToken

	// Sync again with same token — no changes expected
	ms2 := syncCollection(t, ts.URL, calPath, token)
	if len(ms2.Responses) != 0 {
		t.Fatalf("expected 0 changes, got %d", len(ms2.Responses))
	}
	if ms2.SyncToken != token {
		t.Error("sync token should not change when nothing happened")
	}
}

// TestCompliance_PROPPATCH_DisplayName tests updating calendar display name.
func TestCompliance_PROPPATCH_DisplayName(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()
	calPath := "/testuser/calendars/pp1/"
	createCalendar(t, ts.URL, calPath)

	// PROPPATCH to change display name
	proppatchBody := `<?xml version="1.0" encoding="UTF-8"?>
<propertyupdate xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set>
    <prop>
      <displayname>Renamed Calendar</displayname>
    </prop>
  </set>
</propertyupdate>`

	req, _ := http.NewRequest("PROPPATCH", ts.URL+calPath, strings.NewReader(proppatchBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PROPPATCH: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d: %s", resp.StatusCode, body)
	}

	// Verify via FindCalendars
	cals, err := client.FindCalendars(ctx, "/testuser/calendars/")
	if err != nil {
		t.Fatalf("FindCalendars: %v", err)
	}
	found := false
	for _, c := range cals {
		if c.Path == calPath && c.Name == "Renamed Calendar" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected calendar name 'Renamed Calendar', got calendars: %+v", cals)
	}
}

// TestCompliance_PROPPATCH_Description tests updating calendar description.
func TestCompliance_PROPPATCH_Description(t *testing.T) {
	ts, _ := setupCompliance(t)
	calPath := "/testuser/calendars/pp2/"
	createCalendar(t, ts.URL, calPath)

	proppatchBody := `<?xml version="1.0" encoding="UTF-8"?>
<propertyupdate xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <set>
    <prop>
      <C:calendar-description>My important calendar</C:calendar-description>
    </prop>
  </set>
</propertyupdate>`

	req, _ := http.NewRequest("PROPPATCH", ts.URL+calPath, strings.NewReader(proppatchBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PROPPATCH: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", resp.StatusCode)
	}
}

// TestCompliance_DeleteCalendar tests DELETE on a calendar collection.
func TestCompliance_DeleteCalendar(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()
	calPath := "/testuser/calendars/to-delete/"
	createCalendar(t, ts.URL, calPath)

	// Add an event to the calendar
	start := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
	_, err := client.PutCalendarObject(ctx, calPath+"event-1.ics", makeTestEvent("ev1", "Doomed Event", start, end))
	if err != nil {
		t.Fatalf("put event: %v", err)
	}

	// DELETE the calendar collection
	req, _ := http.NewRequest("DELETE", ts.URL+calPath, nil)
	req.SetBasicAuth("testuser", "testpass")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE calendar: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Verify calendar is gone
	cals, err := client.FindCalendars(ctx, "/testuser/calendars/")
	if err != nil {
		t.Fatalf("FindCalendars: %v", err)
	}
	for _, c := range cals {
		if c.Path == calPath {
			t.Error("calendar should have been deleted")
		}
	}
}

// TestCompliance_PropFindSyncToken tests that PROPFIND on a calendar returns sync-token.
func TestCompliance_PropFindSyncToken(t *testing.T) {
	ts, client := setupCompliance(t)
	ctx := context.Background()
	calPath := "/testuser/calendars/synced/"
	createCalendar(t, ts.URL, calPath)

	// Add an event so sync-token advances
	start := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 12, 1, 11, 0, 0, 0, time.UTC)
	_, err := client.PutCalendarObject(ctx, calPath+"event-1.ics", makeTestEvent("ev1", "Event", start, end))
	if err != nil {
		t.Fatalf("put event: %v", err)
	}

	// PROPFIND with Depth: 0 asking for sync-token
	propfindBody := `<?xml version="1.0" encoding="UTF-8"?>
<propfind xmlns="DAV:">
  <prop>
    <displayname/>
    <sync-token/>
  </prop>
</propfind>`

	req, _ := http.NewRequest("PROPFIND", ts.URL+calPath, strings.NewReader(propfindBody))
	req.SetBasicAuth("testuser", "testpass")
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d: %s", resp.StatusCode, body)
	}

	bodyStr := string(body)
	if !strings.Contains(bodyStr, "sync-token") {
		t.Errorf("expected sync-token in PROPFIND response, got:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "sync-token-") {
		t.Errorf("expected sync-token value in PROPFIND response, got:\n%s", bodyStr)
	}
}
