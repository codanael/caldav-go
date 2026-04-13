package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/codanael/caldav-go/storage"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

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

func TestNew(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	b, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	// Verify tables exist by running a simple query.
	var count int
	err = b.db.QueryRow("SELECT COUNT(*) FROM calendars").Scan(&count)
	if err != nil {
		t.Fatalf("query calendars table: %v", err)
	}
}

func TestCalendarCRUD(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	b, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	ctx := testContext("alice")

	// Create
	cal := &caldav.Calendar{
		Path:                  "/alice/calendars/work/",
		Name:                  "Work",
		Description:           "Work calendar",
		SupportedComponentSet: []string{"VEVENT", "VTODO"},
	}
	if err := b.CreateCalendar(ctx, cal); err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	// List
	cals, err := b.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	if len(cals) != 1 {
		t.Fatalf("expected 1 calendar, got %d", len(cals))
	}
	if cals[0].Name != "Work" {
		t.Errorf("expected name Work, got %s", cals[0].Name)
	}

	// Get
	got, err := b.GetCalendar(ctx, "/alice/calendars/work/")
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if got.Name != "Work" {
		t.Errorf("expected name Work, got %s", got.Name)
	}

	// Another user can't see it
	bobCtx := testContext("bob")
	bobCals, err := b.ListCalendars(bobCtx)
	if err != nil {
		t.Fatalf("ListCalendars(bob): %v", err)
	}
	if len(bobCals) != 0 {
		t.Errorf("expected 0 calendars for bob, got %d", len(bobCals))
	}
}

func TestPutAndGetCalendarObject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	b, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	ctx := testContext("alice")

	// Create calendar first
	if err := b.CreateCalendar(ctx, &caldav.Calendar{
		Path: "/alice/calendars/work/",
		Name: "Work",
	}); err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	// Put an event
	start := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 15, 11, 0, 0, 0, time.UTC)
	eventCal := makeEvent("event-1", "Team Standup", start, end)

	co, err := b.PutCalendarObject(ctx, "/alice/calendars/work/event-1.ics", eventCal, nil)
	if err != nil {
		t.Fatalf("PutCalendarObject: %v", err)
	}
	if co.ETag == "" {
		t.Error("expected non-empty ETag")
	}

	// Get the object
	got, err := b.GetCalendarObject(ctx, "/alice/calendars/work/event-1.ics", &caldav.CalendarCompRequest{})
	if err != nil {
		t.Fatalf("GetCalendarObject: %v", err)
	}
	if got.ETag != co.ETag {
		t.Errorf("ETag mismatch: %s != %s", got.ETag, co.ETag)
	}
	if got.Data == nil {
		t.Fatal("expected non-nil Data")
	}
}

func TestPutCalendarObject_VTODO(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	b, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	ctx := testContext("alice")
	if err := b.CreateCalendar(ctx, &caldav.Calendar{
		Path: "/alice/calendars/tasks/",
		Name: "Tasks",
	}); err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	due := time.Date(2026, 4, 20, 17, 0, 0, 0, time.UTC)
	todoCal := makeTodo("todo-1", "Buy groceries", due)

	co, err := b.PutCalendarObject(ctx, "/alice/calendars/tasks/todo-1.ics", todoCal, nil)
	if err != nil {
		t.Fatalf("PutCalendarObject(VTODO): %v", err)
	}
	if co.ETag == "" {
		t.Error("expected non-empty ETag")
	}
}

func TestPutCalendarObject_IfNoneMatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	b, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	ctx := testContext("alice")
	if err := b.CreateCalendar(ctx, &caldav.Calendar{
		Path: "/alice/calendars/work/",
		Name: "Work",
	}); err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	start := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 15, 11, 0, 0, 0, time.UTC)
	eventCal := makeEvent("event-1", "Meeting", start, end)

	// First put succeeds
	_, err = b.PutCalendarObject(ctx, "/alice/calendars/work/event-1.ics", eventCal, nil)
	if err != nil {
		t.Fatalf("first put: %v", err)
	}

	// Second put with IfNoneMatch=* should fail
	_, err = b.PutCalendarObject(ctx, "/alice/calendars/work/event-1.ics", eventCal, &caldav.PutCalendarObjectOptions{
		IfNoneMatch: "*",
	})
	if err == nil {
		t.Fatal("expected error for IfNoneMatch on existing resource")
	}
}

func TestListCalendarObjects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	b, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	ctx := testContext("alice")
	if err := b.CreateCalendar(ctx, &caldav.Calendar{
		Path: "/alice/calendars/work/",
		Name: "Work",
	}); err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	// Put 2 events
	for i, name := range []string{"event-1", "event-2"} {
		start := time.Date(2026, 4, 15+i, 10, 0, 0, 0, time.UTC)
		end := time.Date(2026, 4, 15+i, 11, 0, 0, 0, time.UTC)
		_, err := b.PutCalendarObject(ctx, "/alice/calendars/work/"+name+".ics", makeEvent(name, "Event "+name, start, end), nil)
		if err != nil {
			t.Fatalf("PutCalendarObject(%s): %v", name, err)
		}
	}

	objects, err := b.ListCalendarObjects(ctx, "/alice/calendars/work/", &caldav.CalendarCompRequest{})
	if err != nil {
		t.Fatalf("ListCalendarObjects: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objects))
	}
}

func TestQueryCalendarObjects_ByCompType(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	b, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	ctx := testContext("alice")
	if err := b.CreateCalendar(ctx, &caldav.Calendar{
		Path: "/alice/calendars/mixed/",
		Name: "Mixed",
	}); err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	// Add an event and a todo
	start := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 15, 11, 0, 0, 0, time.UTC)
	_, err = b.PutCalendarObject(ctx, "/alice/calendars/mixed/event-1.ics", makeEvent("event-1", "Meeting", start, end), nil)
	if err != nil {
		t.Fatalf("put event: %v", err)
	}

	due := time.Date(2026, 4, 20, 17, 0, 0, 0, time.UTC)
	_, err = b.PutCalendarObject(ctx, "/alice/calendars/mixed/todo-1.ics", makeTodo("todo-1", "Task", due), nil)
	if err != nil {
		t.Fatalf("put todo: %v", err)
	}

	// Query only VEVENTs
	results, err := b.QueryCalendarObjects(ctx, "/alice/calendars/mixed/", &caldav.CalendarQuery{
		CompFilter: caldav.CompFilter{
			Name: "VCALENDAR",
			Comps: []caldav.CompFilter{
				{Name: "VEVENT"},
			},
		},
	})
	if err != nil {
		t.Fatalf("QueryCalendarObjects(VEVENT): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 VEVENT, got %d", len(results))
	}

	// Query only VTODOs
	results, err = b.QueryCalendarObjects(ctx, "/alice/calendars/mixed/", &caldav.CalendarQuery{
		CompFilter: caldav.CompFilter{
			Name: "VCALENDAR",
			Comps: []caldav.CompFilter{
				{Name: "VTODO"},
			},
		},
	})
	if err != nil {
		t.Fatalf("QueryCalendarObjects(VTODO): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 VTODO, got %d", len(results))
	}
}

func TestDeleteCalendarObject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	b, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	ctx := testContext("alice")
	if err := b.CreateCalendar(ctx, &caldav.Calendar{
		Path: "/alice/calendars/work/",
		Name: "Work",
	}); err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	start := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 15, 11, 0, 0, 0, time.UTC)
	_, err = b.PutCalendarObject(ctx, "/alice/calendars/work/event-1.ics", makeEvent("event-1", "Meeting", start, end), nil)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	if err := b.DeleteCalendarObject(ctx, "/alice/calendars/work/event-1.ics"); err != nil {
		t.Fatalf("DeleteCalendarObject: %v", err)
	}

	// Should be gone
	_, err = b.GetCalendarObject(ctx, "/alice/calendars/work/event-1.ics", &caldav.CalendarCompRequest{})
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestPrincipalPaths(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	b, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	ctx := testContext("alice")

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
}
