package serve

import (
	"net/http"
	"strings"

	"github.com/klauspost/compress/gzip"
)

// minCompressSize is the smallest body worth compressing. Below it the gzip
// header, trailer and deflate framing cost more bytes than the encoding saves,
// and the round trip is dominated by latency rather than transfer anyway.
const minCompressSize = 1 << 10

// compressibleType reports whether a response body is worth compressing and safe
// to compress. Only JSON qualifies: it is the REST surface (the webmail and
// administration APIs), where a folder listing, a search result or a user
// directory is large, highly repetitive, and read over links that may be slow.
//
// Everything else is left alone deliberately. The Exchange-compatible surfaces
// (EWS SOAP, ActiveSync, MAPI/HTTP, DAV) are wire-compatibility contracts with
// real clients and carry their own compression where the protocol defines it, so
// they are not changed here. Server-sent events must never be compressed: a
// compressor buffers, and buffering is exactly what a push channel cannot afford.
// Archives, images and video are already compressed, so a second pass burns CPU
// to add bytes.
func compressibleType(contentType string) bool {
	return strings.HasPrefix(contentType, "application/json")
}

// compressMiddleware wraps h to gzip qualifying responses when the client asked
// for it. A client that sent no Accept-Encoding, or a handler whose response does
// not qualify, is served exactly as before: the wrapper decides at the first
// write, once the handler has set its Content-Type, and passes the bytes straight
// through when the answer is no.
func compressMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			h.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		defer cw.Close()
		// Vary tells caches that the body depends on the request's encoding
		// preference, so a compressed answer is never replayed to a client that
		// cannot read it.
		w.Header().Add("Vary", "Accept-Encoding")
		h.ServeHTTP(cw, r)
	})
}

// acceptsGzip reports whether the header offers gzip. It ignores quality values:
// a client that lists gzip at q=0 to refuse it is vanishingly rare, and the
// worst case is that it receives an encoding it announced.
func acceptsGzip(accept string) bool {
	for enc := range strings.SplitSeq(accept, ",") {
		if name, _, _ := strings.Cut(strings.TrimSpace(enc), ";"); strings.EqualFold(name, "gzip") {
			return true
		}
	}
	return false
}

// compressWriter decides on the first write whether to gzip the response, then
// stays with that decision. It has to wait for the first write because the
// handler sets its Content-Type on the way there, and it holds the first chunk
// back until it can tell whether the body clears minCompressSize.
type compressWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	status   int
	wrote    bool // WriteHeader has been called on the wrapped writer
	decided  bool
	buffered []byte
}

// Unwrap exposes the underlying writer to http.ResponseController, which the
// streaming handlers use to flush. Without it those handlers would lose their
// flush and hold their push events until the response ended.
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// WriteHeader records the status; the header is not sent until the first write,
// when the Content-Encoding is known.
func (c *compressWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

func (c *compressWriter) Write(b []byte) (int, error) {
	if c.decided {
		return c.write(b)
	}
	// Hold bytes back until either the body is clearly worth compressing or the
	// handler has declared a length that settles it.
	c.buffered = append(c.buffered, b...)
	if len(c.buffered) < minCompressSize && c.declaredLength() == 0 {
		return len(b), nil
	}
	c.decide()
	n, err := c.write(c.buffered)
	c.buffered = nil
	if err != nil {
		return 0, err
	}
	// Report the caller's own byte count: the buffered prefix was already
	// acknowledged to whoever wrote it.
	if n > len(b) {
		n = len(b)
	}
	return n, nil
}

// declaredLength returns the handler's Content-Length, or 0 when it set none.
func (c *compressWriter) declaredLength() int {
	if v := c.Header().Get("Content-Length"); v != "" {
		n := 0
		for _, ch := range v {
			if ch < '0' || ch > '9' {
				return 0
			}
			n = n*10 + int(ch-'0')
		}
		return n
	}
	return 0
}

// decide settles whether this response is gzipped and emits the header.
func (c *compressWriter) decide() {
	c.decided = true
	h := c.Header()
	// A handler that encoded the body itself keeps its encoding.
	if h.Get("Content-Encoding") == "" && compressibleType(h.Get("Content-Type")) && c.bodyLargeEnough() {
		h.Set("Content-Encoding", "gzip")
		// The stored length describes the identity body, which is no longer what
		// goes out; the response is chunked instead.
		h.Del("Content-Length")
		c.gz = gzip.NewWriter(c.ResponseWriter)
	}
	c.sendHeader()
}

// bodyLargeEnough reports whether the response clears the size floor, by declared
// length when the handler set one and by what has been buffered otherwise.
func (c *compressWriter) bodyLargeEnough() bool {
	if n := c.declaredLength(); n > 0 {
		return n >= minCompressSize
	}
	return len(c.buffered) >= minCompressSize
}

// sendHeader writes the status line once.
func (c *compressWriter) sendHeader() {
	if c.wrote {
		return
	}
	c.wrote = true
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.ResponseWriter.WriteHeader(c.status)
}

// write sends bytes through the compressor when one was installed.
func (c *compressWriter) write(b []byte) (int, error) {
	if c.gz != nil {
		return c.gz.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

// Flush pushes whatever is held through to the client, so a streaming handler
// keeps streaming. A response still under the size floor is flushed uncompressed:
// a handler that flushes is telling us these bytes are wanted now, which settles
// the question against buffering for a better ratio.
func (c *compressWriter) Flush() {
	if !c.decided {
		c.decide()
		if len(c.buffered) > 0 {
			_, _ = c.write(c.buffered)
			c.buffered = nil
		}
	}
	if c.gz != nil {
		_ = c.gz.Flush()
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close flushes a response that never reached the size floor and finishes the
// gzip stream. A handler that wrote nothing at all leaves the response untouched,
// so an empty 204 stays empty.
func (c *compressWriter) Close() {
	if !c.decided {
		if len(c.buffered) == 0 && !c.wrote && c.status == 0 {
			return
		}
		c.decide()
	}
	if len(c.buffered) > 0 {
		_, _ = c.write(c.buffered)
		c.buffered = nil
	}
	if c.gz != nil {
		_ = c.gz.Close()
	}
}
