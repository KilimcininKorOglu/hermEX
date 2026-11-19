package pop3

import (
	"bufio"
	"bytes"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestQuitReportsDeletionsTheStoreRefused marks a message deleted on a store that
// refuses every write, then quits. RFC 1939 section 6 gives QUIT in the UPDATE
// state two answers, "+OK" and "-ERR some deleted messages not removed"; a client
// told +OK believes the message is gone and will not try again, so a refused
// deletion must be answered -ERR, and the failure is recorded.
func TestQuitReportsDeletionsTheStoreRefused(t *testing.T) {
	st, err := objectstore.Open(filepath.Join(t.TempDir(), "mbox"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	sink := &captureSink{}
	var out bytes.Buffer
	c := &pop3Session{
		s:    &Server{Logger: logging.New(sink)},
		conn: server,
		w:    &ew{out: bufio.NewWriter(&out)},
		mb: &mailbox{
			st: st, folder: int64(mapi.PrivateFIDInbox),
			msgs: []objectstore.MessageInfo{{UID: 7}}, deleted: []bool{true},
		},
	}
	c.quit()
	c.w.flush()

	if !strings.HasPrefix(out.String(), "-ERR") {
		t.Errorf("QUIT after a refused deletion answered %q, want -ERR", out.String())
	}
	if _, ok := findEvent(sink.snapshot(), "quit.commit.fail"); !ok {
		t.Error("the refused deletion left no record")
	}
}
