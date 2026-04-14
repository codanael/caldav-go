package server

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/codanael/caldav-go/storage"
)

// resourceLevel determines the nesting level of a CalDAV resource path.
// /{user}/ = 1, /{user}/calendars/ = 2, /{user}/calendars/{name}/ = 3, etc.
func resourceLevel(path string) int {
	p := strings.TrimPrefix(path, "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return 0
	}
	return len(strings.Split(p, "/"))
}

// isCalendarPath returns true if the path looks like a calendar collection
// (3 segments: /{user}/calendars/{name}/).
func isCalendarPath(path string) bool {
	return resourceLevel(path) == 3
}

// newExtendedHandler returns an http.Handler that intercepts PROPPATCH,
// DELETE on calendars, PROPFIND (to inject sync-token), and sync-collection
// REPORT. Everything else is delegated to the inner CalDAV handler.
func newExtendedHandler(eb storage.ExtendedBackend, inner http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PROPPATCH":
			if isCalendarPath(r.URL.Path) {
				handlePropPatch(w, r, eb, logger)
				return
			}
		case "DELETE":
			if isCalendarPath(r.URL.Path) {
				handleDeleteCalendar(w, r, eb, logger)
				return
			}
		case "PROPFIND":
			if isCalendarPath(r.URL.Path) {
				handlePropFindWithSync(w, r, eb, inner, logger)
				return
			}
		case "REPORT":
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read request body", http.StatusBadRequest)
				return
			}
			r.Body.Close()

			var query syncCollectionQuery
			if err := xml.Unmarshal(bodyBytes, &query); err == nil && query.XMLName.Local == "sync-collection" {
				handleSyncCollection(w, r, eb, &query, logger)
				return
			}
			// Replay body for the inner handler.
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		inner.ServeHTTP(w, r)
	})
}

// --- PROPPATCH ---

// proppatchRequest models the DAV:propertyupdate XML.
type proppatchRequest struct {
	XMLName xml.Name         `xml:"DAV: propertyupdate"`
	Set     []proppatchSet   `xml:"set"`
	Remove  []proppatchRemove `xml:"remove"`
}

type proppatchSet struct {
	Prop proppatchProp `xml:"prop"`
}

type proppatchRemove struct {
	Prop proppatchProp `xml:"prop"`
}

type proppatchProp struct {
	DisplayName *string `xml:"displayname"`
	Description *string `xml:"calendar-description"`
	Color       *string `xml:"calendar-color"`
}

func handlePropPatch(w http.ResponseWriter, r *http.Request, eb storage.ExtendedBackend, logger *slog.Logger) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req proppatchRequest
	if err := xml.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "invalid PROPPATCH XML", http.StatusBadRequest)
		return
	}

	update := &storage.CalendarUpdate{}
	var updatedProps []xml.Name

	for _, s := range req.Set {
		if s.Prop.DisplayName != nil {
			update.Name = s.Prop.DisplayName
			updatedProps = append(updatedProps, xml.Name{Space: "DAV:", Local: "displayname"})
		}
		if s.Prop.Description != nil {
			update.Description = s.Prop.Description
			updatedProps = append(updatedProps, xml.Name{Space: "urn:ietf:params:xml:ns:caldav", Local: "calendar-description"})
		}
		if s.Prop.Color != nil {
			update.Color = s.Prop.Color
			updatedProps = append(updatedProps, xml.Name{Space: "http://apple.com/ns/ical/", Local: "calendar-color"})
		}
	}

	if err := eb.UpdateCalendar(r.Context(), r.URL.Path, update); err != nil {
		logger.Error("PROPPATCH failed", "path", r.URL.Path, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build 207 multistatus response per RFC 4918.
	type propstatXML struct {
		Prop   []xml.Name `xml:"prop"`
		Status string     `xml:"status"`
	}
	type responseXML struct {
		XMLName  xml.Name    `xml:"DAV: response"`
		Href     string      `xml:"href"`
		PropStat propstatXML `xml:"propstat"`
	}
	type multistatusXML struct {
		XMLName   xml.Name    `xml:"DAV: multistatus"`
		Responses []responseXML `xml:"response"`
	}

	ms := multistatusXML{
		Responses: []responseXML{{
			Href: r.URL.Path,
			PropStat: propstatXML{
				Prop:   updatedProps,
				Status: "HTTP/1.1 200 OK",
			},
		}},
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(&ms)
}

// --- DELETE calendar ---

func handleDeleteCalendar(w http.ResponseWriter, r *http.Request, eb storage.ExtendedBackend, logger *slog.Logger) {
	if err := eb.DeleteCalendar(r.Context(), r.URL.Path); err != nil {
		logger.Error("DELETE calendar failed", "path", r.URL.Path, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- PROPFIND with sync-token injection ---

// propfindResponseWriter captures the inner handler's PROPFIND response
// so we can inject the sync-token property.
type propfindResponseWriter struct {
	http.ResponseWriter
	buf        bytes.Buffer
	statusCode int
	hijacked   bool
}

func (w *propfindResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	if code == http.StatusMultiStatus {
		w.hijacked = true
		// Don't write to the real response yet — we'll modify it first.
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *propfindResponseWriter) Write(b []byte) (int, error) {
	if w.hijacked {
		return w.buf.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func handlePropFindWithSync(w http.ResponseWriter, r *http.Request, eb storage.ExtendedBackend, inner http.Handler, logger *slog.Logger) {
	// Capture the inner handler's response.
	rec := &propfindResponseWriter{ResponseWriter: w}
	inner.ServeHTTP(rec, r)

	if !rec.hijacked {
		return // Non-207 response, already written directly.
	}

	// Get the sync token for this calendar.
	token, err := eb.GetSyncToken(r.Context(), r.URL.Path)
	if err != nil {
		logger.Debug("could not get sync token for PROPFIND", "path", r.URL.Path, "error", err)
		// Fall through without injection.
		w.WriteHeader(http.StatusMultiStatus)
		w.Write(rec.buf.Bytes())
		return
	}

	// Inject sync-token into the XML response.
	// We look for the calendar's <response> and add <sync-token> as a prop.
	body := rec.buf.String()
	body = injectSyncToken(body, r.URL.Path, token)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	fmt.Fprint(w, body)
}

// injectSyncToken replaces the empty/404 sync-token property with the actual
// value in a PROPFIND multistatus response.
func injectSyncToken(xmlBody string, calPath string, token string) string {
	syncTokenWithValue := fmt.Sprintf(`<sync-token xmlns="DAV:">%s</sync-token>`, token)

	// Strategy 1: Replace the 404 propstat block that contains sync-token.
	// go-webdav returns unknown properties as 404:
	//   <propstat><prop><sync-token></sync-token></prop><status>HTTP/1.1 404 Not Found</status></propstat>
	// We want to move sync-token into the 200 propstat with the actual value.

	// Find and remove the 404 propstat containing sync-token.
	// Look for the pattern: <propstat...><prop...><sync-token...></sync-token></prop><status>HTTP/1.1 404 Not Found</status></propstat>
	for _, nsPrefix := range []string{`xmlns="DAV:"`, ""} {
		var emptyToken string
		if nsPrefix != "" {
			emptyToken = fmt.Sprintf(`<sync-token %s></sync-token>`, nsPrefix)
		} else {
			emptyToken = `<sync-token></sync-token>`
		}

		idx := strings.Index(xmlBody, emptyToken)
		if idx < 0 {
			continue
		}

		// Find the enclosing <propstat> ... </propstat> for this 404 block.
		// Search backwards for <propstat
		propstatStart := strings.LastIndex(xmlBody[:idx], "<propstat")
		if propstatStart < 0 {
			continue
		}
		// Search forward for </propstat>
		propstatEnd := strings.Index(xmlBody[propstatStart:], "</propstat>")
		if propstatEnd < 0 {
			continue
		}
		propstatEnd = propstatStart + propstatEnd + len("</propstat>")

		propstatBlock := xmlBody[propstatStart:propstatEnd]
		if strings.Contains(propstatBlock, "404") {
			// Remove this 404 propstat block entirely.
			xmlBody = xmlBody[:propstatStart] + xmlBody[propstatEnd:]
		}
	}

	// Now inject the sync-token with value into the 200 propstat's <prop>.
	// Find the first </prop> inside a 200 propstat for our calendar.
	hrefTag := calPath + "</href>"
	hrefIdx := strings.Index(xmlBody, hrefTag)
	if hrefIdx < 0 {
		return xmlBody
	}

	// Find the first </prop> after the href.
	propCloseIdx := strings.Index(xmlBody[hrefIdx:], "</prop>")
	if propCloseIdx < 0 {
		return xmlBody
	}
	insertAt := hrefIdx + propCloseIdx
	return xmlBody[:insertAt] + syncTokenWithValue + xmlBody[insertAt:]
}
