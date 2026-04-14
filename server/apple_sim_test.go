package server

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codanael/caldav-go/auth"
	"github.com/codanael/caldav-go/storage/sqlite"
)

// Apple Calendar simulation test.
// Replays the exact sequence of HTTP requests macOS Calendar.app sends
// when connecting to a CalDAV server, syncing, creating/updating/deleting events.

type appleSimClient struct {
	t       *testing.T
	baseURL string
	user    string
	pass    string
}

func (c *appleSimClient) do(method, path, body string, headers map[string]string) (int, string, http.Header) {
	c.t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		c.t.Fatalf("%s %s: create request: %v", method, path, err)
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("User-Agent", "macOS/15.0 (24A335) CalendarAgent/930")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(respBody), resp.Header
}

func (c *appleSimClient) propfind(path, depth, body string) (int, string) {
	c.t.Helper()
	code, respBody, _ := c.do("PROPFIND", path, body, map[string]string{
		"Content-Type": "application/xml; charset=utf-8",
		"Depth":        depth,
	})
	return code, respBody
}

func (c *appleSimClient) report(path, body string) (int, string) {
	c.t.Helper()
	code, respBody, _ := c.do("REPORT", path, body, map[string]string{
		"Content-Type": "application/xml; charset=utf-8",
		"Depth":        "1",
	})
	return code, respBody
}

func (c *appleSimClient) assertContains(body, what, context string) {
	c.t.Helper()
	if !strings.Contains(body, what) {
		c.t.Errorf("[%s] expected response to contain %q, got:\n%s", context, what, body)
	}
}

func (c *appleSimClient) assertNotContains(body, what, context string) {
	c.t.Helper()
	if strings.Contains(body, what) {
		c.t.Errorf("[%s] expected response NOT to contain %q, got:\n%s", context, what, body)
	}
}

// TestAppleCalendarSimulation replays the full Apple Calendar lifecycle.
func TestAppleCalendarSimulation(t *testing.T) {
	// --- Setup ---
	dbPath := filepath.Join(t.TempDir(), "apple-sim.db")
	backend, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { backend.Close() })

	authProvider := auth.NewBasicProvider()
	if err := authProvider.AddUser("johndoe", "s3cret", auth.User{
		ID: "johndoe", DisplayName: "John Doe", Email: "john@example.com",
	}); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	handler := New(WithBackend(backend), WithAuth(authProvider))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := &appleSimClient{t: t, baseURL: ts.URL, user: "johndoe", pass: "s3cret"}

	// ===================================================================
	// PHASE 1: Account Discovery
	// Apple Calendar starts by probing /.well-known/caldav
	// ===================================================================
	t.Log("Phase 1: Account Discovery")

	// Step 1.1: Well-known redirect
	code, _, headers := c.do("GET", "/.well-known/caldav", "", nil)
	// Apple Calendar follows redirects, but we test the redirect itself
	if code != 308 {
		t.Fatalf("Step 1.1: expected 308, got %d", code)
	}
	location := headers.Get("Location")
	if !strings.Contains(location, "/johndoe/") {
		t.Fatalf("Step 1.1: expected redirect to /johndoe/, got %s", location)
	}

	// Step 1.2: Discover current-user-principal
	code, body := c.propfind("/", "0", `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:">
  <A:prop>
    <A:current-user-principal/>
    <A:resourcetype/>
  </A:prop>
</A:propfind>`)
	if code != 207 {
		t.Fatalf("Step 1.2: expected 207, got %d", code)
	}
	c.assertContains(body, "/johndoe/", "Step 1.2: current-user-principal")

	// Step 1.3: Discover calendar-home-set from principal
	code, body = c.propfind("/johndoe/", "0", `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <A:prop>
    <C:calendar-home-set/>
    <C:schedule-inbox-URL/>
    <C:schedule-outbox-URL/>
    <C:calendar-user-address-set/>
    <A:current-user-principal/>
    <A:resourcetype/>
  </A:prop>
</A:propfind>`)
	if code != 207 {
		t.Fatalf("Step 1.3: expected 207, got %d", code)
	}
	c.assertContains(body, "/johndoe/calendars/", "Step 1.3: calendar-home-set")
	c.assertContains(body, "/johndoe/inbox/", "Step 1.3: schedule-inbox-URL")
	c.assertContains(body, "/johndoe/outbox/", "Step 1.3: schedule-outbox-URL")
	c.assertContains(body, "mailto:johndoe", "Step 1.3: calendar-user-address-set")

	// ===================================================================
	// PHASE 2: Create a calendar (Apple Calendar does this on first setup)
	// ===================================================================
	t.Log("Phase 2: Create Calendar")

	code, _, _ = c.do("MKCOL", "/johndoe/calendars/home/", `<?xml version="1.0" encoding="UTF-8"?>
<A:mkcol xmlns:A="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <A:set>
    <A:prop>
      <A:resourcetype>
        <A:collection/>
        <C:calendar/>
      </A:resourcetype>
      <A:displayname>Home</A:displayname>
    </A:prop>
  </A:set>
</A:mkcol>`, map[string]string{"Content-Type": "application/xml; charset=utf-8"})
	if code != 201 {
		t.Fatalf("Step 2: MKCOL expected 201, got %d", code)
	}

	// Set calendar color via PROPPATCH (Apple Calendar does this after creation)
	code, body, _ = c.do("PROPPATCH", "/johndoe/calendars/home/", `<?xml version="1.0" encoding="UTF-8"?>
<A:propertyupdate xmlns:A="DAV:" xmlns:I="http://apple.com/ns/ical/">
  <A:set>
    <A:prop>
      <I:calendar-color>#1BADF8FF</I:calendar-color>
    </A:prop>
  </A:set>
</A:propertyupdate>`, map[string]string{"Content-Type": "application/xml; charset=utf-8"})
	if code != 207 {
		t.Fatalf("Step 2 PROPPATCH: expected 207, got %d: %s", code, body)
	}

	// ===================================================================
	// PHASE 3: List calendars (Apple Calendar does this frequently)
	// ===================================================================
	t.Log("Phase 3: List Calendars")

	code, body = c.propfind("/johndoe/calendars/", "1", `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/" xmlns:I="http://apple.com/ns/ical/">
  <A:prop>
    <A:resourcetype/>
    <A:displayname/>
    <C:supported-calendar-component-set/>
    <CS:getctag/>
    <A:sync-token/>
    <I:calendar-color/>
    <A:supported-report-set/>
    <A:current-user-privilege-set/>
  </A:prop>
</A:propfind>`)
	if code != 207 {
		t.Fatalf("Step 3: expected 207, got %d", code)
	}
	c.assertContains(body, "Home", "Step 3: displayname")
	c.assertContains(body, "calendar", "Step 3: resourcetype calendar")
	c.assertContains(body, "getctag", "Step 3: getctag")
	c.assertContains(body, "sync-token", "Step 3: sync-token")
	c.assertContains(body, "#1BADF8FF", "Step 3: calendar-color")
	c.assertContains(body, "supported-report-set", "Step 3: supported-report-set")
	c.assertContains(body, "calendar-query", "Step 3: calendar-query report")
	c.assertContains(body, "calendar-multiget", "Step 3: calendar-multiget report")
	c.assertContains(body, "sync-collection", "Step 3: sync-collection report")

	// ===================================================================
	// PHASE 4: Initial sync (empty calendar, then add events)
	// ===================================================================
	t.Log("Phase 4: Initial Sync")

	// Step 4.1: sync-collection with empty token (initial sync)
	code, body = c.report("/johndoe/calendars/home/", `<?xml version="1.0" encoding="UTF-8"?>
<A:sync-collection xmlns:A="DAV:">
  <A:sync-token/>
  <A:sync-level>1</A:sync-level>
  <A:prop>
    <A:getetag/>
  </A:prop>
</A:sync-collection>`)
	if code != 207 {
		t.Fatalf("Step 4.1: expected 207, got %d: %s", code, body)
	}
	// Empty calendar — should have sync-token but no responses
	c.assertContains(body, "sync-token", "Step 4.1: sync-token in response")

	// Parse initial sync token
	var initialSyncResp syncMultistatusResp
	if err := xml.Unmarshal([]byte(body), &initialSyncResp); err != nil {
		t.Fatalf("Step 4.1: parse sync response: %v", err)
	}
	syncToken := initialSyncResp.SyncToken
	t.Logf("  Initial sync token: %s", syncToken)

	// ===================================================================
	// PHASE 5: Create events (PUT)
	// ===================================================================
	t.Log("Phase 5: Create Events")

	// Apple Calendar creates events with specific iCal format
	event1 := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//Apple Inc.//macOS 15.0//EN\r\n" +
		"CALSCALE:GREGORIAN\r\n" +
		"BEGIN:VTIMEZONE\r\n" +
		"TZID:America/New_York\r\n" +
		"BEGIN:STANDARD\r\n" +
		"DTSTART:20071104T020000\r\n" +
		"RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\n" +
		"TZOFFSETFROM:-0400\r\n" +
		"TZOFFSETTO:-0500\r\n" +
		"TZNAME:EST\r\n" +
		"END:STANDARD\r\n" +
		"BEGIN:DAYLIGHT\r\n" +
		"DTSTART:20070311T020000\r\n" +
		"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\n" +
		"TZOFFSETFROM:-0500\r\n" +
		"TZOFFSETTO:-0400\r\n" +
		"TZNAME:EDT\r\n" +
		"END:DAYLIGHT\r\n" +
		"END:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\n" +
		"DTSTART;TZID=America/New_York:20260601T090000\r\n" +
		"DTEND;TZID=America/New_York:20260601T100000\r\n" +
		"UID:E1F5C9A2-3B4D-4E6F-8A9B-C0D1E2F3A4B5\r\n" +
		"DTSTAMP:20260414T120000Z\r\n" +
		"CREATED:20260414T120000Z\r\n" +
		"SUMMARY:Team Standup\r\n" +
		"DESCRIPTION:Daily team sync\r\n" +
		"LOCATION:Zoom\r\n" +
		"SEQUENCE:0\r\n" +
		"STATUS:CONFIRMED\r\n" +
		"TRANSP:OPAQUE\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"DESCRIPTION:Reminder\r\n" +
		"TRIGGER:-PT15M\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	code, _, putHeaders := c.do("PUT",
		"/johndoe/calendars/home/E1F5C9A2-3B4D-4E6F-8A9B-C0D1E2F3A4B5.ics",
		event1,
		map[string]string{
			"Content-Type": "text/calendar; charset=utf-8",
			"If-None-Match": "*",
		})
	if code != 201 {
		t.Fatalf("Step 5.1: PUT event expected 201, got %d", code)
	}
	event1ETag := putHeaders.Get("ETag")
	if event1ETag == "" {
		t.Error("Step 5.1: expected ETag in PUT response")
	}
	t.Logf("  Event 1 created, ETag: %s", event1ETag)

	// Create a second event (recurring)
	event2 := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//Apple Inc.//macOS 15.0//EN\r\n" +
		"CALSCALE:GREGORIAN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"DTSTART:20260615T140000Z\r\n" +
		"DTEND:20260615T150000Z\r\n" +
		"UID:B2C3D4E5-F6A7-8B9C-0D1E-2F3A4B5C6D7E\r\n" +
		"DTSTAMP:20260414T120000Z\r\n" +
		"CREATED:20260414T120000Z\r\n" +
		"SUMMARY:Weekly Review\r\n" +
		"RRULE:FREQ=WEEKLY;BYDAY=MO\r\n" +
		"SEQUENCE:0\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	code, _, _ = c.do("PUT",
		"/johndoe/calendars/home/B2C3D4E5-F6A7-8B9C-0D1E-2F3A4B5C6D7E.ics",
		event2,
		map[string]string{
			"Content-Type": "text/calendar; charset=utf-8",
			"If-None-Match": "*",
		})
	if code != 201 {
		t.Fatalf("Step 5.2: PUT event2 expected 201, got %d", code)
	}
	t.Log("  Event 2 created (recurring weekly)")

	// ===================================================================
	// PHASE 6: Incremental sync (detect new events)
	// ===================================================================
	t.Log("Phase 6: Incremental Sync")

	code, body = c.report("/johndoe/calendars/home/", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<A:sync-collection xmlns:A="DAV:">
  <A:sync-token>%s</A:sync-token>
  <A:sync-level>1</A:sync-level>
  <A:prop>
    <A:getetag/>
  </A:prop>
</A:sync-collection>`, syncToken))
	if code != 207 {
		t.Fatalf("Step 6: expected 207, got %d: %s", code, body)
	}

	var syncResp2 syncMultistatusResp
	if err := xml.Unmarshal([]byte(body), &syncResp2); err != nil {
		t.Fatalf("Step 6: parse: %v", err)
	}
	if len(syncResp2.Responses) != 2 {
		t.Fatalf("Step 6: expected 2 changed objects, got %d", len(syncResp2.Responses))
	}
	syncToken = syncResp2.SyncToken
	t.Logf("  Sync found %d new events, new token: %s", len(syncResp2.Responses), syncToken)

	// Step 6.2: Fetch full events via calendar-multiget (Apple Calendar does this)
	var hrefs string
	for _, r := range syncResp2.Responses {
		hrefs += fmt.Sprintf("  <A:href>%s</A:href>\n", r.Href)
	}
	code, body = c.report("/johndoe/calendars/home/", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:A="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <A:prop>
    <A:getetag/>
    <C:calendar-data/>
  </A:prop>
%s</C:calendar-multiget>`, hrefs))
	if code != 207 {
		t.Fatalf("Step 6.2: multiget expected 207, got %d: %s", code, body)
	}
	c.assertContains(body, "Team Standup", "Step 6.2: event 1 summary")
	c.assertContains(body, "Weekly Review", "Step 6.2: event 2 summary")
	c.assertContains(body, "VALARM", "Step 6.2: alarm preserved")
	c.assertContains(body, "RRULE", "Step 6.2: recurrence rule preserved")
	t.Log("  Multiget returned both events with VALARM and RRULE intact")

	// ===================================================================
	// PHASE 7: Update an event
	// ===================================================================
	t.Log("Phase 7: Update Event")

	updatedEvent1 := strings.Replace(event1, "Team Standup", "Team Standup (Moved)", 1)
	updatedEvent1 = strings.Replace(updatedEvent1, "SEQUENCE:0", "SEQUENCE:1", 1)
	updatedEvent1 = strings.Replace(updatedEvent1, "090000", "100000", -1)
	updatedEvent1 = strings.Replace(updatedEvent1, "100000Z", "110000Z", 1) // fix DTEND

	code, _, updateHeaders := c.do("PUT",
		"/johndoe/calendars/home/E1F5C9A2-3B4D-4E6F-8A9B-C0D1E2F3A4B5.ics",
		updatedEvent1,
		map[string]string{
			"Content-Type": "text/calendar; charset=utf-8",
		})
	if code != 201 {
		t.Fatalf("Step 7: PUT update expected 201, got %d", code)
	}
	newETag := updateHeaders.Get("ETag")
	if newETag == event1ETag {
		t.Error("Step 7: ETag should change after update")
	}
	t.Logf("  Event 1 updated, new ETag: %s", newETag)

	// ===================================================================
	// PHASE 8: Incremental sync (detect update)
	// ===================================================================
	t.Log("Phase 8: Sync After Update")

	code, body = c.report("/johndoe/calendars/home/", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<A:sync-collection xmlns:A="DAV:">
  <A:sync-token>%s</A:sync-token>
  <A:sync-level>1</A:sync-level>
  <A:prop>
    <A:getetag/>
  </A:prop>
</A:sync-collection>`, syncToken))
	if code != 207 {
		t.Fatalf("Step 8: expected 207, got %d", code)
	}

	var syncResp3 syncMultistatusResp
	xml.Unmarshal([]byte(body), &syncResp3)
	if len(syncResp3.Responses) != 1 {
		t.Fatalf("Step 8: expected 1 changed object (update), got %d", len(syncResp3.Responses))
	}
	c.assertContains(syncResp3.Responses[0].Href, "E1F5C9A2", "Step 8: modified event href")
	syncToken = syncResp3.SyncToken
	t.Logf("  Sync detected 1 update, new token: %s", syncToken)

	// ===================================================================
	// PHASE 9: Delete an event
	// ===================================================================
	t.Log("Phase 9: Delete Event")

	code, _, _ = c.do("DELETE",
		"/johndoe/calendars/home/B2C3D4E5-F6A7-8B9C-0D1E-2F3A4B5C6D7E.ics",
		"", nil)
	if code != 204 {
		t.Fatalf("Step 9: DELETE expected 204, got %d", code)
	}
	t.Log("  Event 2 deleted")

	// ===================================================================
	// PHASE 10: Incremental sync (detect deletion)
	// ===================================================================
	t.Log("Phase 10: Sync After Deletion")

	code, body = c.report("/johndoe/calendars/home/", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<A:sync-collection xmlns:A="DAV:">
  <A:sync-token>%s</A:sync-token>
  <A:sync-level>1</A:sync-level>
  <A:prop>
    <A:getetag/>
  </A:prop>
</A:sync-collection>`, syncToken))
	if code != 207 {
		t.Fatalf("Step 10: expected 207, got %d", code)
	}

	var syncResp4 syncMultistatusResp
	xml.Unmarshal([]byte(body), &syncResp4)
	if len(syncResp4.Responses) != 1 {
		t.Fatalf("Step 10: expected 1 deleted object, got %d", len(syncResp4.Responses))
	}
	c.assertContains(syncResp4.Responses[0].Status, "404", "Step 10: deleted status")
	c.assertContains(syncResp4.Responses[0].Href, "B2C3D4E5", "Step 10: deleted event href")
	syncToken = syncResp4.SyncToken
	t.Logf("  Sync detected 1 deletion, new token: %s", syncToken)

	// ===================================================================
	// PHASE 11: Verify final state via calendar-query
	// ===================================================================
	t.Log("Phase 11: Final State Verification")

	code, body = c.report("/johndoe/calendars/home/", `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-query xmlns:A="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <A:prop>
    <A:getetag/>
    <C:calendar-data/>
  </A:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT"/>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`)
	if code != 207 {
		t.Fatalf("Step 11: expected 207, got %d: %s", code, body)
	}
	c.assertContains(body, "Team Standup (Moved)", "Step 11: updated event present")
	c.assertNotContains(body, "Weekly Review", "Step 11: deleted event gone")
	t.Log("  Final state verified: 1 event remaining with correct title")

	// ===================================================================
	// PHASE 12: Idle sync (no changes)
	// ===================================================================
	t.Log("Phase 12: Idle Sync (No Changes)")

	code, body = c.report("/johndoe/calendars/home/", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<A:sync-collection xmlns:A="DAV:">
  <A:sync-token>%s</A:sync-token>
  <A:sync-level>1</A:sync-level>
  <A:prop>
    <A:getetag/>
  </A:prop>
</A:sync-collection>`, syncToken))
	if code != 207 {
		t.Fatalf("Step 12: expected 207, got %d", code)
	}

	var syncResp5 syncMultistatusResp
	xml.Unmarshal([]byte(body), &syncResp5)
	if len(syncResp5.Responses) != 0 {
		t.Fatalf("Step 12: expected 0 changes, got %d", len(syncResp5.Responses))
	}
	t.Logf("  No changes detected, token unchanged: %s", syncResp5.SyncToken)

	t.Log("=== Apple Calendar simulation PASSED ===")
}
