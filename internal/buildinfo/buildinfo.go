// Package buildinfo carries the release and the source state a binary was built
// from, so an operator can tell what is actually running.
//
// The release number has one source, the VERSION file at the repository root. The
// container images build from a context with .git excluded and with
// -buildvcs=false, which is right for build hygiene but removes the toolchain's
// automatic stamping, so every value is injected at link time instead:
//
//	go build -ldflags "-X hermex/internal/buildinfo.SemVer=<x.y.z> -X hermex/internal/buildinfo.Tagged=true -X hermex/internal/buildinfo.Commit=<sha> -X hermex/internal/buildinfo.BuildTime=<rfc3339>"
//
// The Makefile does this for every build it drives, and the Dockerfile reads VERSION
// itself. A binary built any other way falls back to whatever the toolchain recorded,
// and reports "unknown" when there is nothing to report, which is honest rather than
// misleading.
package buildinfo

import "runtime/debug"

// SemVer is the release number from the VERSION file. Tagged is "true" when the
// build ran on the exact commit tagged v<SemVer> with a clean tree, which is what
// makes it that release rather than work on the way to the next one. Commit carries
// a "-dirty" suffix when the tree had uncommitted changes, since a bare sha would
// otherwise claim a source state the binary was not built from.
var (
	SemVer    = ""
	Tagged    = ""
	Commit    = ""
	BuildTime = ""
)

// Display returns the version every surface shows: "0.1.0 (adeff8a)" for a tagged
// release build, "0.1.0-dev+adeff8a" for any other build, with the commit's "-dirty"
// suffix carried through. A build with no release number reports the commit alone,
// and one with no commit reports "0.1.0-dev", which never reads as a release.
func Display() string {
	rev := Revision()
	switch {
	case SemVer == "":
		return rev
	case rev == unknown:
		return SemVer + "-dev"
	case Tagged == "true":
		return SemVer + " (" + rev + ")"
	default:
		return SemVer + "-dev+" + rev
	}
}

// unknown is what every accessor reports when there is nothing recorded.
const unknown = "unknown"

// Revision returns the commit the binary was built from. When no value was
// injected it falls back to the toolchain's own VCS stamp, which a plain `go build`
// or `go run` outside the container images does record.
func Revision() string {
	if Commit != "" {
		return Commit
	}
	rev, modified, ok := vcsStamp()
	if !ok || rev == "" {
		return unknown
	}
	if modified {
		return rev + "-dirty"
	}
	return rev
}

// Built returns the build time. It falls back to the toolchain's VCS timestamp,
// which records the commit time rather than the build time; that is the closer
// answer available and still pins the source state.
func Built() string {
	if BuildTime != "" {
		return BuildTime
	}
	for _, s := range settings() {
		if s.Key == "vcs.time" && s.Value != "" {
			return s.Value
		}
	}
	return unknown
}

// vcsStamp reads the toolchain's recorded revision and dirty flag.
func vcsStamp() (rev string, modified, ok bool) {
	for _, s := range settings() {
		switch s.Key {
		case "vcs.revision":
			rev, ok = s.Value, true
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return rev, modified, ok
}

// settings returns the embedded build settings, or nothing when the binary carries
// no build info at all (a test binary, for instance).
func settings() []debug.BuildSetting {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return info.Settings
}
