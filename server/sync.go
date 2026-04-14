package server

import (
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/codanael/caldav-go/storage"
)

// syncCollectionQuery matches DAV:sync-collection XML.
type syncCollectionQuery struct {
	XMLName   xml.Name `xml:"DAV: sync-collection"`
	SyncToken string   `xml:"sync-token"`
	SyncLevel string   `xml:"sync-level"`
}

// multistatusResponse is a minimal multistatus XML response for sync-collection.
type multistatusResponse struct {
	XMLName   xml.Name        `xml:"DAV: multistatus"`
	Responses []syncResponse  `xml:"response"`
	SyncToken string          `xml:"sync-token"`
}

type syncResponse struct {
	XMLName xml.Name    `xml:"DAV: response"`
	Href    string      `xml:"href"`
	Status  string      `xml:"status,omitempty"`
	PropStat *syncPropStat `xml:"propstat,omitempty"`
}

type syncPropStat struct {
	Prop   syncProp `xml:"prop"`
	Status string   `xml:"status"`
}

type syncProp struct {
	GetETag string `xml:"DAV: getetag,omitempty"`
}

// newSyncCollectionHandler returns an http.Handler that intercepts sync-collection
// REPORT requests and delegates everything else to the inner CalDAV handler.
func newSyncCollectionHandler(sb storage.SyncBackend, inner http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "REPORT" {
			inner.ServeHTTP(w, r)
			return
		}

		// Peek at the request body to check if it's a sync-collection REPORT.
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		r.Body.Close()

		var query syncCollectionQuery
		if err := xml.Unmarshal(bodyBytes, &query); err != nil || query.XMLName.Local != "sync-collection" {
			// Not a sync-collection request — replay the body and delegate to inner handler.
			r.Body = io.NopCloser(newBytesReader(bodyBytes))
			inner.ServeHTTP(w, r)
			return
		}

		// Handle sync-collection REPORT.
		handleSyncCollection(w, r, sb, &query, logger)
	})
}

func handleSyncCollection(w http.ResponseWriter, r *http.Request, sb storage.SyncBackend, query *syncCollectionQuery, logger *slog.Logger) {
	syncResp, err := sb.SyncCollection(r.Context(), r.URL.Path, query.SyncToken)
	if err != nil {
		logger.Error("sync-collection failed", "path", r.URL.Path, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ms := multistatusResponse{
		SyncToken: syncResp.NewToken,
	}

	for _, ch := range syncResp.Changes {
		if ch.ChangeType == "deleted" {
			ms.Responses = append(ms.Responses, syncResponse{
				Href:   ch.Path,
				Status: "HTTP/1.1 404 Not Found",
			})
		} else {
			ms.Responses = append(ms.Responses, syncResponse{
				Href: ch.Path,
				PropStat: &syncPropStat{
					Prop:   syncProp{GetETag: fmt.Sprintf(`"%s"`, ch.ETag)},
					Status: "HTTP/1.1 200 OK",
				},
			})
		}
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(&ms)
}

// bytesReader wraps a byte slice as an io.Reader.
type bytesReader struct {
	data []byte
	pos  int
}

func newBytesReader(data []byte) *bytesReader {
	return &bytesReader{data: data}
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
