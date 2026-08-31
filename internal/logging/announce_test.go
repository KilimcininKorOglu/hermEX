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
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	fn()
	os.Stderr = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestBuildAnnouncesTheRunningBuild is the whole point of the stamp. With no CI and
// no tags, a running container carried no marker of the source it was built from,
// so after an incident or a rollback there was no way to answer whether a fix had
// actually shipped. Every daemon builds its logger through this one call, so the
// answer is now recorded once per start, to stderr and to the queryable store the
// admin panel reads.
func TestBuildAnnouncesTheRunningBuild(t *testing.T) {
	oldCommit, oldBuilt := buildinfo.Commit, buildinfo.BuildTime
	buildinfo.Commit, buildinfo.BuildTime = "abc1234-dirty", "2026-01-02T03:04:05Z"
	defer func() { buildinfo.Commit, buildinfo.BuildTime = oldCommit, oldBuilt }()

	out := captureStderr(t, func() {
		_, closeFn := logging.Build("hermex-mta", "", "db", "")
		if err := closeFn(); err != nil {
			t.Error(err)
		}
	})

	if !strings.Contains(out, "process.start") {
		t.Fatalf("no startup event was recorded:\n%s", out)
	}
	for _, want := range []string{"hermex-mta", "abc1234-dirty", "2026-01-02T03:04:05Z"} {
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
		_, closeFn := logging.Build("hermex-imap", "http://invalid", "db", "")
		if err := closeFn(); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "process.start") || !strings.Contains(out, "hermex-imap") {
		t.Errorf("a daemon with no reachable log store does not report its build:\n%s", out)
	}
}
