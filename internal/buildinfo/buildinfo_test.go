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

// TestDisplayFormats pins the version string every surface shows. A tagged build is
// the release itself; anything else is work on the way to the next one and must
// never read as the release, even when the commit is unknown.
func TestDisplayFormats(t *testing.T) {
	cases := []struct {
		name, semver, tagged, commit, want string
	}{
		{"tagged release", "0.1.0", "true", "adeff8a", "0.1.0 (adeff8a)"},
		{"untagged build", "0.1.0", "", "adeff8a", "0.1.0-dev+adeff8a"},
		{"dirty build", "0.1.0", "", "adeff8a-dirty", "0.1.0-dev+adeff8a-dirty"},
		{"no commit", "0.1.0", "true", "", "0.1.0-dev"},
		{"no release number", "", "", "adeff8a", "adeff8a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stamp(t, c.commit, "")
			oldSemVer, oldTagged := SemVer, Tagged
			SemVer, Tagged = c.semver, c.tagged
			t.Cleanup(func() { SemVer, Tagged = oldSemVer, oldTagged })
			if got := Display(); got != c.want {
				t.Errorf("Display() = %q, want %q", got, c.want)
			}
		})
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
