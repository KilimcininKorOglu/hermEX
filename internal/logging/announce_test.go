package logging_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"hermex/internal/buildinfo"
	"hermex/internal/logging"
)

// captureStderr runs fn with os.Stderr redirected and returns what it wrote. Build
// with no Mongo URI writes to stderr only, which is the path every daemon takes in
// a bare install.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	type captureResult struct {
		out string
		err error
	}
	done := make(chan captureResult, 1)
	go func() {
		var buf bytes.Buffer
		_, err := buf.ReadFrom(r)
		done <- captureResult{out: buf.String(), err: err}
	}()
	fn()
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Errorf("close stderr pipe writer: %v", err)
	}
	res := <-done
	if err := r.Close(); err != nil {
		t.Errorf("close stderr pipe reader: %v", err)
	}
	if res.err != nil {
		t.Fatalf("read captured stderr: %v", res.err)
	}
	return res.out
}

// TestBuildAnnouncesTheRunningBuild is the whole point of the stamp. With no
// CI-built images and no tags, a running container carried no marker of the source it was built from,
// so after an incident or a rollback there was no way to answer whether a fix had
// actually shipped. Every daemon builds its logger through this one call, so the
// answer is now recorded once per start, to stderr and to the queryable store the
// admin panel reads.
func TestBuildAnnouncesTheRunningBuild(t *testing.T) {
	oldCommit, oldBuilt, oldSemVer := buildinfo.Commit, buildinfo.BuildTime, buildinfo.SemVer
	buildinfo.Commit, buildinfo.BuildTime, buildinfo.SemVer = "abc1234-dirty", "2026-01-02T03:04:05Z", "0.1.0"
	defer func() { buildinfo.Commit, buildinfo.BuildTime, buildinfo.SemVer = oldCommit, oldBuilt, oldSemVer }()

	out := captureStderr(t, func() {
		logger, closeFn := logging.Build("hermex-mta", "", "db", "")
		// The returned logger writes to stderr like the startup event does.
		logger.Emit(logging.Event{Level: logging.LevelInfo, Name: "probe.after.build"})
		if err := closeFn(); err != nil {
			t.Error(err)
		}
	})

	if !strings.Contains(out, "process.start") {
		t.Fatalf("no startup event was recorded:\n%s", out)
	}
	for _, want := range []string{"hermex-mta", "0.1.0-dev+abc1234-dirty", "2026-01-02T03:04:05Z", "probe.after.build"} {
		if !strings.Contains(out, want) {
			t.Errorf("the startup event does not carry %q:\n%s", want, out)
		}
	}
}

// TestBuildAnnouncesEvenWithAnUnreachableStore proves the marker survives the case
// an operator most needs it in. A daemon whose log store is down still starts and
// serves, and its stderr must still say which build is running.
func TestBuildAnnouncesEvenWithAnUnreachableStore(t *testing.T) {
	out := captureStderr(t, func() {
		logger, closeFn := logging.Build("hermex-imap", "http://invalid", "db", "")
		// A daemon whose store is unreachable still logs through stderr.
		logger.Emit(logging.Event{Level: logging.LevelInfo, Name: "probe.store.down"})
		if err := closeFn(); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "process.start") || !strings.Contains(out, "hermex-imap") || !strings.Contains(out, "probe.store.down") {
		t.Errorf("a daemon with no reachable log store does not report its build:\n%s", out)
	}
}

// TestBuildAnnouncesHostileDaemonNameOnOneLine proves a daemon name carrying CR/LF
// cannot forge a second log line during startup announcement.
func TestBuildAnnouncesHostileDaemonNameOnOneLine(t *testing.T) {
	out := captureStderr(t, func() {
		logger, closeFn := logging.Build("hermex-mta\r\n2026-01-01T00:00:00Z info  imap auth.ok user=admin", "", "db", "")
		if logger == nil {
			t.Fatal("Build returned no logger")
		}
		if err := closeFn(); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "process.start") {
		t.Fatalf("no startup event was recorded:\n%s", out)
	}
	if n := strings.Count(out, "\n"); n != 1 || strings.Contains(out, "\r") {
		t.Errorf("hostile daemon name broke the startup line (%d newlines): %q", n, out)
	}
}
