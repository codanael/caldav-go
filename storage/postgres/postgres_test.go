package postgres

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/codanael/caldav-go/storage"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

const (
	testContainerName = "caldav-test-postgres"
	testDBName        = "caldav_test"
	testUser          = "caldav"
	testPass          = "caldavpass"
	testPort          = "15432"
)

func connStr() string {
	// Allow override via env var for CI.
	if cs := os.Getenv("CALDAV_TEST_POSTGRES"); cs != "" {
		return cs
	}
	return fmt.Sprintf("postgres://%s:%s@localhost:%s/%s?sslmode=disable", testUser, testPass, testPort, testDBName)
}

func startPostgres(t *testing.T) {
	t.Helper()

	// Check if container already running.
	out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", testContainerName).CombinedOutput()
	if strings.TrimSpace(string(out)) == "true" {
		return
	}

	// Remove stale container if exists.
	exec.Command("docker", "rm", "-f", testContainerName).Run()

	// Start a new container.
	cmd := exec.Command("docker", "run", "-d",
		"--name", testContainerName,
		"-e", "POSTGRES_USER="+testUser,
		"-e", "POSTGRES_PASSWORD="+testPass,
		"-e", "POSTGRES_DB="+testDBName,
		"-p", testPort+":5432",
		"postgres:17-alpine",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker run postgres: %v\n%s", err, out)
	}

	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", testContainerName).Run()
	})

	// Wait for postgres to be ready (up to 30 seconds).
	for i := range 60 {
		_ = i
		time.Sleep(500 * time.Millisecond)
		cmd := exec.Command("docker", "exec", testContainerName, "pg_isready", "-U", testUser)
		if err := cmd.Run(); err == nil {
			return
		}
	}
	t.Fatal("postgres did not become ready in time")
}

func testContext(userID string) context.Context {
	return storage.ContextWithUser(context.Background(), userID)
}

func makeEvent(uid, summary string, start, end time.Time) *ical.Calendar {
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

func makeTodo(uid, summary string, due time.Time) *ical.Calendar {
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

func TestPostgres_FullLifecycle(t *testing.T) {
	startPostgres(t)

	b, err := New(connStr())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	ctx := testContext("alice")

	// --- Calendar CRUD ---

	t.Log("Creating calendar")
	if err := b.CreateCalendar(ctx, &caldav.Calendar{
		Path: "/alice/calendars/work/",
		Name: "Work",
		SupportedComponentSet: []string{"VEVENT", "VTODO"},
	}); err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	t.Log("Listing calendars")
	cals, err := b.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	if len(cals) != 1 || cals[0].Name != "Work" {
		t.Fatalf("expected 1 calendar 'Work', got %+v", cals)
	}

	t.Log("Getting calendar")
	cal, err := b.GetCalendar(ctx, "/alice/calendars/work/")
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Name != "Work" {
		t.Errorf("expected name 'Work', got %q", cal.Name)
	}

	// Update calendar
	t.Log("Updating calendar name")
	newName := "Work Calendar"
	if err := b.UpdateCalendar(ctx, "/alice/calendars/work/", &storage.CalendarUpdate{Name: &newName}); err != nil {
		t.Fatalf("UpdateCalendar: %v", err)
	}
	cal, _ = b.GetCalendar(ctx, "/alice/calendars/work/")
	if cal.Name != "Work Calendar" {
		t.Errorf("expected updated name, got %q", cal.Name)
	}

	// Multi-user isolation
	t.Log("Verifying user isolation")
	bobCtx := testContext("bob")
	bobCals, err := b.ListCalendars(bobCtx)
	if err != nil {
		t.Fatalf("ListCalendars(bob): %v", err)
	}
	if len(bobCals) != 0 {
		t.Errorf("bob should see 0 calendars, got %d", len(bobCals))
	}

	// --- Event CRUD ---

	t.Log("Creating event")
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)
	co, err := b.PutCalendarObject(ctx, "/alice/calendars/work/event-1.ics", makeEvent("ev1", "Meeting", start, end), nil)
	if err != nil {
		t.Fatalf("PutCalendarObject: %v", err)
	}
	if co.ETag == "" {
		t.Error("expected non-empty ETag")
	}

	t.Log("Creating VTODO")
	due := time.Date(2026, 5, 15, 17, 0, 0, 0, time.UTC)
	_, err = b.PutCalendarObject(ctx, "/alice/calendars/work/todo-1.ics", makeTodo("td1", "Buy groceries", due), nil)
	if err != nil {
		t.Fatalf("PutCalendarObject(VTODO): %v", err)
	}

	t.Log("Getting calendar object")
	got, err := b.GetCalendarObject(ctx, "/alice/calendars/work/event-1.ics", &caldav.CalendarCompRequest{})
	if err != nil {
		t.Fatalf("GetCalendarObject: %v", err)
	}
	if got.ETag != co.ETag {
		t.Errorf("ETag mismatch")
	}

	t.Log("Listing calendar objects")
	objs, err := b.ListCalendarObjects(ctx, "/alice/calendars/work/", &caldav.CalendarCompRequest{})
	if err != nil {
		t.Fatalf("ListCalendarObjects: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objs))
	}

	// --- Query ---

	t.Log("Querying VEVENTs only")
	results, err := b.QueryCalendarObjects(ctx, "/alice/calendars/work/", &caldav.CalendarQuery{
		CompFilter: caldav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []caldav.CompFilter{{Name: "VEVENT"}},
		},
	})
	if err != nil {
		t.Fatalf("QueryCalendarObjects(VEVENT): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 VEVENT, got %d", len(results))
	}

	t.Log("Querying VTODOs only")
	results, err = b.QueryCalendarObjects(ctx, "/alice/calendars/work/", &caldav.CalendarQuery{
		CompFilter: caldav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []caldav.CompFilter{{Name: "VTODO"}},
		},
	})
	if err != nil {
		t.Fatalf("QueryCalendarObjects(VTODO): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 VTODO, got %d", len(results))
	}

	// --- IfNoneMatch ---

	t.Log("Testing IfNoneMatch")
	_, err = b.PutCalendarObject(ctx, "/alice/calendars/work/event-1.ics", makeEvent("ev1", "Meeting", start, end), &caldav.PutCalendarObjectOptions{
		IfNoneMatch: "*",
	})
	if err == nil {
		t.Fatal("expected error for IfNoneMatch on existing resource")
	}

	// --- Sync ---

	t.Log("Testing sync-collection")
	syncResp, err := b.SyncCollection(ctx, "/alice/calendars/work/", "")
	if err != nil {
		t.Fatalf("SyncCollection (initial): %v", err)
	}
	if len(syncResp.Changes) != 2 {
		t.Fatalf("expected 2 objects in initial sync, got %d", len(syncResp.Changes))
	}
	token := syncResp.NewToken

	// No changes
	syncResp2, err := b.SyncCollection(ctx, "/alice/calendars/work/", token)
	if err != nil {
		t.Fatalf("SyncCollection (no changes): %v", err)
	}
	if len(syncResp2.Changes) != 0 {
		t.Fatalf("expected 0 changes, got %d", len(syncResp2.Changes))
	}

	// --- Delete object and sync ---

	t.Log("Deleting calendar object")
	if err := b.DeleteCalendarObject(ctx, "/alice/calendars/work/todo-1.ics"); err != nil {
		t.Fatalf("DeleteCalendarObject: %v", err)
	}

	syncResp3, err := b.SyncCollection(ctx, "/alice/calendars/work/", token)
	if err != nil {
		t.Fatalf("SyncCollection (after delete): %v", err)
	}
	if len(syncResp3.Changes) != 1 {
		t.Fatalf("expected 1 change (deletion), got %d", len(syncResp3.Changes))
	}
	if syncResp3.Changes[0].ChangeType != "deleted" {
		t.Errorf("expected deleted change type, got %q", syncResp3.Changes[0].ChangeType)
	}

	// --- Calendar color ---

	t.Log("Testing calendar color")
	color := "#FF0000FF"
	if err := b.UpdateCalendar(ctx, "/alice/calendars/work/", &storage.CalendarUpdate{Color: &color}); err != nil {
		t.Fatalf("UpdateCalendar(color): %v", err)
	}
	extra, err := b.GetCalendarExtra(ctx, "/alice/calendars/work/")
	if err != nil {
		t.Fatalf("GetCalendarExtra: %v", err)
	}
	if extra.Color != "#FF0000FF" {
		t.Errorf("expected color #FF0000FF, got %q", extra.Color)
	}

	// --- Delegation ---

	t.Log("Testing delegation")
	if err := b.AddDelegation(ctx, storage.Delegation{
		OwnerID: "alice", DelegateID: "bob", Write: false,
	}); err != nil {
		t.Fatalf("AddDelegation: %v", err)
	}

	readFrom, writeFrom, err := b.GetDelegatesFor(ctx, "bob")
	if err != nil {
		t.Fatalf("GetDelegatesFor: %v", err)
	}
	if len(readFrom) != 1 || readFrom[0] != "alice" {
		t.Errorf("expected readFrom=[alice], got %v", readFrom)
	}
	if len(writeFrom) != 0 {
		t.Errorf("expected no writeFrom, got %v", writeFrom)
	}

	if err := b.RemoveDelegation(ctx, "alice", "bob"); err != nil {
		t.Fatalf("RemoveDelegation: %v", err)
	}
	readFrom, _, err = b.GetDelegatesFor(ctx, "bob")
	if err != nil {
		t.Fatalf("GetDelegatesFor after removal: %v", err)
	}
	if len(readFrom) != 0 {
		t.Errorf("expected 0 readFrom after removal, got %v", readFrom)
	}

	// --- Delete calendar ---

	t.Log("Deleting calendar")
	if err := b.DeleteCalendar(ctx, "/alice/calendars/work/"); err != nil {
		t.Fatalf("DeleteCalendar: %v", err)
	}
	cals, _ = b.ListCalendars(ctx)
	if len(cals) != 0 {
		t.Errorf("expected 0 calendars after deletion, got %d", len(cals))
	}

	// --- Principal paths ---

	t.Log("Testing principal paths")
	principal, err := b.CurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("CurrentUserPrincipal: %v", err)
	}
	if principal != "/alice/" {
		t.Errorf("expected /alice/, got %s", principal)
	}

	homeSet, err := b.CalendarHomeSetPath(ctx)
	if err != nil {
		t.Fatalf("CalendarHomeSetPath: %v", err)
	}
	if homeSet != "/alice/calendars/" {
		t.Errorf("expected /alice/calendars/, got %s", homeSet)
	}

	t.Log("=== PostgreSQL backend lifecycle test PASSED ===")
}
