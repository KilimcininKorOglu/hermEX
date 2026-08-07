package buildinfo

import "testing"

// stamp installs link-time values for one test and restores them afterwards.
func stamp(t *testing.T, commit, built string) {
	t.Helper()
	oldCommit, oldBuilt := Commit, BuildTime
	Commit, BuildTime = commit, built
	t.Cleanup(func() { Commit, BuildTime = oldCommit, oldBuilt })
}

// TestInjectedValuesWin proves the link-time stamp is what a deployed binary
// reports. The container images build from a context with .git excluded and with
// VCS stamping off, so this injection is the only thing standing between the
// operator and a binary that cannot say where it came from.
func TestInjectedValuesWin(t *testing.T) {
	stamp(t, "abc1234-dirty", "2026-01-02T03:04:05Z")
	if got := Revision(); got != "abc1234-dirty" {
		t.Errorf("Revision() = %q, want the injected commit", got)
	}
	if got := Built(); got != "2026-01-02T03:04:05Z" {
		t.Errorf("Built() = %q, want the injected time", got)
	}
}

// TestUnstampedBuildIsHonest proves a binary with nothing recorded says so rather
// than reporting an empty string that reads like a real answer. Under `go test` the
// toolchain records no vcs.revision, so this is the genuinely unstamped case.
func TestUnstampedBuildIsHonest(t *testing.T) {
	stamp(t, "", "")
	if got := Revision(); got == "" {
		t.Error("Revision() is empty, which reads as a value rather than as no answer")
	}
	if got := Built(); got == "" {
		t.Error("Built() is empty, which reads as a value rather than as no answer")
	}
}

// TestDirtyMarkerSurvives proves the -dirty suffix is carried through rather than
// trimmed. A bare sha on a binary built from a modified tree claims a source state
// that was never built, which is worse than reporting nothing.
func TestDirtyMarkerSurvives(t *testing.T) {
	stamp(t, "deadbee-dirty", "2026-01-02T03:04:05Z")
	if got := Revision(); got != "deadbee-dirty" {
		t.Errorf("Revision() = %q, want the dirty marker preserved", got)
	}
}
