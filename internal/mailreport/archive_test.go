package mailreport

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// failingClose is a document whose reader closes with an error, as a
// decompressor does when the stream behind it is damaged.
type failingClose struct {
	io.Reader
	err error
}

func (f failingClose) Close() error { return f.err }

// TestAReportIsNotReadPastAFailedClose: the bytes read before a failed close are
// not a report the caller may store, so none are returned and the close error is.
// A clean close returns the document.
func TestAReportIsNotReadPastAFailedClose(t *testing.T) {
	closeErr := errors.New("flate: corrupt input")
	b, err := readAllClosing(failingClose{Reader: strings.NewReader("<feedback/>"), err: closeErr})
	if !errors.Is(err, closeErr) || b != nil {
		t.Errorf("readAllClosing with a failed close = %q, %v; want no bytes and the close error", b, err)
	}

	b, err = readAllClosing(io.NopCloser(strings.NewReader("<feedback/>")))
	if err != nil || string(b) != "<feedback/>" {
		t.Errorf("readAllClosing with a clean close = %q, %v; want the document", b, err)
	}
}
