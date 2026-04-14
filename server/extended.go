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
				handlePropFindCalendar(w, r, eb, inner, logger)
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

// --- PROPFIND with extended property injection ---

// propfindResponseWriter captures the inner handler's PROPFIND response
// so we can inject additional properties.
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

func handlePropFindCalendar(w http.ResponseWriter, r *http.Request, eb storage.ExtendedBackend, inner http.Handler, logger *slog.Logger) {
	// Capture the inner handler's response.
	rec := &propfindResponseWriter{ResponseWriter: w}
	inner.ServeHTTP(rec, r)

	if !rec.hijacked {
		return
	}

	body := rec.buf.String()

	// Gather extended properties to inject.
	token, tokenErr := eb.GetSyncToken(r.Context(), r.URL.Path)
	extra, extraErr := eb.GetCalendarExtra(r.Context(), r.URL.Path)

	// Remove 404 propstat blocks for properties we'll provide.
	body = remove404PropstatBlock(body, "sync-token")
	body = remove404PropstatBlock(body, "getctag")
	body = remove404PropstatBlock(body, "calendar-color")
	body = remove404PropstatBlock(body, "supported-report-set")

	// Inject properties into the 200 propstat.
	var propsToInject []string

	if tokenErr == nil && token != "" {
		propsToInject = append(propsToInject,
			fmt.Sprintf(`<sync-token xmlns="DAV:">%s</sync-token>`, token),
			fmt.Sprintf(`<getctag xmlns="http://calendarserver.org/ns/">%s</getctag>`, token),
		)
	}

	if extraErr == nil && extra != nil && extra.Color != "" {
		propsToInject = append(propsToInject,
			fmt.Sprintf(`<calendar-color xmlns="http://apple.com/ns/ical/">%s</calendar-color>`, extra.Color),
		)
	}

	// Always advertise supported reports.
	propsToInject = append(propsToInject, supportedReportSetXML)

	body = injectIntoProps(body, r.URL.Path, propsToInject)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	fmt.Fprint(w, body)
}

const supportedReportSetXML = `<supported-report-set xmlns="DAV:">` +
	`<supported-report><report><calendar-query xmlns="urn:ietf:params:xml:ns:caldav"/></report></supported-report>` +
	`<supported-report><report><calendar-multiget xmlns="urn:ietf:params:xml:ns:caldav"/></report></supported-report>` +
	`<supported-report><report><sync-collection xmlns="DAV:"/></report></supported-report>` +
	`</supported-report-set>`

// remove404PropstatBlock removes a 404 propstat that contains the given property name.
func remove404PropstatBlock(xmlBody string, propLocalName string) string {
	// Look for the property in a 404 propstat block and remove the entire block.
	for _, pattern := range []string{
		fmt.Sprintf(`<%s `, propLocalName),
		fmt.Sprintf(`<%s>`, propLocalName),
		fmt.Sprintf(`<%s/>`, propLocalName),
	} {
		idx := strings.Index(xmlBody, pattern)
		if idx < 0 {
			continue
		}

		propstatStart := strings.LastIndex(xmlBody[:idx], "<propstat")
		if propstatStart < 0 {
			continue
		}
		propstatEnd := strings.Index(xmlBody[propstatStart:], "</propstat>")
		if propstatEnd < 0 {
			continue
		}
		propstatEnd = propstatStart + propstatEnd + len("</propstat>")

		block := xmlBody[propstatStart:propstatEnd]
		if strings.Contains(block, "404") {
			xmlBody = xmlBody[:propstatStart] + xmlBody[propstatEnd:]
			// Recurse in case there are multiple 404 blocks with this property.
			return remove404PropstatBlock(xmlBody, propLocalName)
		}
	}
	return xmlBody
}

// injectIntoProps injects XML property strings into the 200 propstat's <prop>
// for the response matching calPath.
func injectIntoProps(xmlBody string, calPath string, props []string) string {
	if len(props) == 0 {
		return xmlBody
	}

	hrefTag := calPath + "</href>"
	hrefIdx := strings.Index(xmlBody, hrefTag)
	if hrefIdx < 0 {
		return xmlBody
	}

	injection := strings.Join(props, "")

	// Try to inject into an existing 200 propstat's <prop>.
	propCloseIdx := strings.Index(xmlBody[hrefIdx:], "</prop>")
	if propCloseIdx >= 0 {
		insertAt := hrefIdx + propCloseIdx
		return xmlBody[:insertAt] + injection + xmlBody[insertAt:]
	}

	// No <prop> found — build a propstat block and inject before </response>.
	respCloseIdx := strings.Index(xmlBody[hrefIdx:], "</response>")
	if respCloseIdx < 0 {
		return xmlBody
	}
	insertAt := hrefIdx + respCloseIdx
	propstat := `<propstat xmlns="DAV:"><prop>` + injection + `</prop><status>HTTP/1.1 200 OK</status></propstat>`
	return xmlBody[:insertAt] + propstat + xmlBody[insertAt:]
}
