package buildinfo

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// releaseNumber is the only form the VERSION file may hold: a plain x.y.z, with no
// "v" prefix and no pre-release part, since the build derives those itself.
var releaseNumber = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// TestVersionFileIsTheOneReleaseNumber proves the SPA manifests carry the same
// release number as the VERSION file. `make release` writes all three; a hand edit
// to one of them otherwise lets the webmail bundle and the binaries claim two
// different releases, and nothing would notice.
func TestVersionFileIsTheOneReleaseNumber(t *testing.T) {
	raw, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(raw))
	if !releaseNumber.MatchString(want) {
		t.Fatalf("VERSION holds %q, want a plain x.y.z release number", want)
	}

	var pkg struct {
		Version string `json:"version"`
	}
	readJSON(t, "../webmail2/package.json", &pkg)
	if pkg.Version != want {
		t.Errorf("package.json version = %q, want %q from VERSION", pkg.Version, want)
	}

	var lock struct {
		Version  string `json:"version"`
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	readJSON(t, "../webmail2/package-lock.json", &lock)
	if lock.Version != want || lock.Packages[""].Version != want {
		t.Errorf("package-lock.json versions = %q and %q, want %q from VERSION", lock.Version, lock.Packages[""].Version, want)
	}
}

// readJSON decodes one manifest file into v.
func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
