package activesync

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// fakeRecorder captures UpsertSession calls for the telemetry tests. When err is
// set every write fails, exercising the best-effort error path.
type fakeRecorder struct {
	mu   sync.Mutex
	recs []directory.SessionRecord
	err  error
}

func (f *fakeRecorder) UpsertSession(s directory.SessionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, s)
	return f.err
}

func (f *fakeRecorder) all() []directory.SessionRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]directory.SessionRecord(nil), f.recs...)
}

// TestDispatchRecordsSession proves dispatch opens a live-session row at the start
// (still running) and stamps it ended on exit, with the request's metadata. The
// command handler erroring on the empty body is irrelevant — telemetry wraps it.
func TestDispatchRecordsSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, "mail.hermex.test")
	rec := &fakeRecorder{}
	srv.Sessions = rec

	r := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync?Cmd=FolderSync&DeviceId=dev1&DeviceType=iPhone", nil)
	r.Header.Set("User-Agent", "TestAgent/1.0")
	w := httptest.NewRecorder()
	sess := &session{
		user: "alice@hermex.test", mailbox: dir, protocol: "14.1",
		req: asRequest{cmd: "FolderSync", deviceID: "dev1", deviceType: "iPhone"},
	}
	srv.dispatch(w, r, sess)

	recs := rec.all()
	if len(recs) < 2 {
		t.Fatalf("got %d telemetry writes, want at least begin+finish", len(recs))
	}
	begin, finish := recs[0], recs[len(recs)-1]
	if begin.Command != "FolderSync" || begin.EndedAt != 0 || begin.Username != "alice@hermex.test" ||
		begin.DeviceID != "dev1" || begin.DeviceType != "iPhone" || begin.UserAgent != "TestAgent/1.0" || begin.IP == "" {
		t.Errorf("begin record = %+v, want running FolderSync row with metadata", begin)
	}
	if finish.ID != begin.ID || finish.EndedAt == 0 {
		t.Errorf("finish record = %+v, want same id %q and a non-zero ended", finish, begin.ID)
	}
}

// TestSessionTouchAndPush proves a Ping session is flagged push and that touch
// refreshes last_update on the same row, while begin/finish bracket it.
func TestSessionTouchAndPush(t *testing.T) {
	rec := &fakeRecorder{}
	srv := &Server{Sessions: rec}
	r := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync?Cmd=Ping&DeviceId=d2", nil)
	sess := &session{user: "bob@hermex.test", req: asRequest{cmd: "Ping", deviceID: "d2"}}

	srv.beginSession(r, sess)
	first := sess.tel.LastUpdate
	sess.tel.LastUpdate = first - 1 // simulate time passing without the clock
	srv.touchSession(sess)
	srv.finishSession(sess)

	recs := rec.all()
	if len(recs) != 3 {
		t.Fatalf("got %d writes, want 3 (begin, touch, finish)", len(recs))
	}
	if !recs[0].Push {
		t.Errorf("Ping begin not flagged push: %+v", recs[0])
	}
	if recs[0].ID != recs[2].ID {
		t.Errorf("touch/finish wrote a different session id than begin")
	}
	if recs[2].EndedAt == 0 {
		t.Errorf("finish did not stamp ended: %+v", recs[2])
	}
}

// TestSessionWriteFailureLogged proves a failed telemetry write is best-effort:
// the command path is untouched and the failure is logged at debug so a silently
// dead monitor (a broken real write path the fake recorder never exercises) is
// diagnosable instead of invisible.
func TestSessionWriteFailureLogged(t *testing.T) {
	sink := &captureSink{}
	rec := &fakeRecorder{err: errors.New("column mismatch")}
	srv := &Server{Sessions: rec, Logger: logging.New(sink)}
	r := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync?Cmd=Ping&DeviceId=d3", nil)
	sess := &session{user: "carol@hermex.test", req: asRequest{cmd: "Ping", deviceID: "d3"}}

	srv.beginSession(r, sess)
	srv.finishSession(sess)

	// The command path still completed: telemetry stayed on and both writes ran.
	if !sess.telOn {
		t.Errorf("a write failure disabled telemetry; it must be best-effort")
	}
	be, ok := sink.find("session.begin.fail")
	if !ok {
		t.Fatalf("begin write failure was not logged")
	}
	if be.Level != logging.LevelDebug || be.Fields["error"] != "column mismatch" || be.Fields["device"] != "d3" {
		t.Errorf("begin.fail event = %+v, want debug level with the device and error", be)
	}
	if _, ok := sink.find("session.finish.fail"); !ok {
		t.Errorf("finish write failure was not logged")
	}
}

// TestNilSessionsNoop proves telemetry is fully optional: with no recorder, the
// helpers are no-ops and never panic.
func TestNilSessionsNoop(t *testing.T) {
	srv := &Server{}
	r := httptest.NewRequest("POST", "/x", nil)
	sess := &session{req: asRequest{cmd: "Sync"}}
	srv.beginSession(r, sess)
	srv.touchSession(sess)
	srv.finishSession(sess)
	if sess.telOn {
		t.Errorf("telemetry marked on without a recorder")
	}
}
