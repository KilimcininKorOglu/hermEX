// Package buildinfo carries the source state a binary was built from, so an
// operator can tell what is actually running.
//
// With no CI and no tags, the running binary is the only evidence of what is
// deployed. The container images build from a context with .git excluded and with
// -buildvcs=false, which is right for build hygiene but removes the toolchain's
// automatic stamping, so the values are injected at link time instead:
//
//	go build -ldflags "-X hermex/internal/buildinfo.Commit=<sha> -X hermex/internal/buildinfo.BuildTime=<rfc3339>"
//
// The Makefile does this for every build it drives. A binary built any other way
// falls back to whatever the toolchain recorded, and reports "unknown" when there
// is nothing to report, which is honest rather than misleading.
package buildinfo

import "runtime/debug"

// Commit and BuildTime are set at link time. Commit carries a "-dirty" suffix when
// the tree had uncommitted changes, since a bare sha would otherwise claim a source
// state the binary was not built from.
var (
	Commit    = ""
	BuildTime = ""
)

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
