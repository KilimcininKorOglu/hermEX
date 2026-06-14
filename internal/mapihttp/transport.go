package mapihttp

import (
	"encoding/binary"
	"io"
	"net/http"
	"strconv"
	"unicode/utf16"
)

// serverApp is announced in X-ServerApplication; clients log but do not gate on it.
const serverApp = "Exchange/15.02.0390.000"

// MAPI/HTTP X-ResponseCode values ([MS-OXCMAPIHTTP] 2.2.3.3.3): 0 success then
// the documented failure set. A non-zero code rides in the X-ResponseCode header
// (the HTTP status stays 200).
const (
	rcSuccess          = 0
	rcInvalidVerb      = 1
	rcInvalidCtxCookie = 2
	rcMissingHeader    = 3
	rcNoPriv           = 4
	rcInvalidReqBody   = 5
	rcMissingCookie    = 6
	rcInvalidSeq       = 7
	rcInvalidReqType   = 8
)

// metaTags is the dechunked preamble a client reads before the binary body: a
// (here empty) run of PROCESSING/PENDING lines, then DONE and the trailing
// informational headers, terminated by a blank line.
const metaTags = "PROCESSING\r\nDONE\r\nX-ElapsedTime: 0\r\nX-StartTime: 0\r\n\r\n"

// commonHeaders sets the success response headers shared by every request type.
func commonHeaders(w http.ResponseWriter, r *http.Request, reqType string) {
	h := w.Header()
	h.Set("Content-Type", "application/mapi-http")
	h.Set("X-RequestType", reqType)
	h.Set("X-RequestId", r.Header.Get("X-RequestId"))
	h.Set("X-ClientInfo", r.Header.Get("X-ClientInfo"))
	h.Set("X-ResponseCode", strconv.Itoa(rcSuccess))
	h.Set("X-PendingPeriod", "15000")
	h.Set("X-ExpirationInfo", "1800000")
	h.Set("X-ServerApplication", serverApp)
}

// writeNormal frames a successful response: the meta preamble (flushed so the
// transfer is chunked, as Exchange streams it) followed by the binary body.
func writeNormal(w http.ResponseWriter, r *http.Request, reqType string, body []byte) {
	commonHeaders(w, r, reqType)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, metaTags)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_, _ = w.Write(body)
}

// writeRespError frames an X-ResponseCode failure. Per the protocol the HTTP
// status is still 200; the failure is carried in the X-ResponseCode header.
func writeRespError(w http.ResponseWriter, r *http.Request, reqType string, code int) {
	h := w.Header()
	h.Set("Content-Type", "text/html")
	h.Set("X-RequestType", reqType)
	h.Set("X-RequestId", r.Header.Get("X-RequestId"))
	h.Set("X-ResponseCode", strconv.Itoa(code))
	h.Set("X-ServerApplication", serverApp)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "X-ResponseCode: "+strconv.Itoa(code))
}

// --- little-endian request/response body cursor ---

// reader is a fail-fast little-endian cursor over a request body. A short read
// sets err; callers check it once and map a true err to rcInvalidReqBody.
type reader struct {
	b   []byte
	pos int
	err bool
}

func (r *reader) u32() uint32 {
	if r.err || r.pos+4 > len(r.b) {
		r.err = true
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v
}

func (r *reader) take(n int) []byte {
	if r.err || n < 0 || r.pos+n > len(r.b) {
		r.err = true
		return nil
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v
}

// cstr reads a NUL-terminated ASCII string.
func (r *reader) cstr() string {
	if r.err {
		return ""
	}
	i := r.pos
	for i < len(r.b) && r.b[i] != 0 {
		i++
	}
	if i >= len(r.b) {
		r.err = true
		return ""
	}
	s := string(r.b[r.pos:i])
	r.pos = i + 1
	return s
}

// writer builds a little-endian response body.
type writer struct{ b []byte }

func (w *writer) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }

// str appends a NUL-terminated ASCII string.
func (w *writer) str(s string) {
	w.b = append(w.b, s...)
	w.b = append(w.b, 0)
}

// wstr appends a NUL-terminated UTF-16LE string.
func (w *writer) wstr(s string) {
	for _, u := range utf16.Encode([]rune(s)) {
		w.b = binary.LittleEndian.AppendUint16(w.b, u)
	}
	w.b = binary.LittleEndian.AppendUint16(w.b, 0)
}

func (w *writer) raw(b []byte) { w.b = append(w.b, b...) }
