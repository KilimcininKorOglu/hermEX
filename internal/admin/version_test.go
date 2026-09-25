package admin

import (
	"bytes"
	"strings"
	"testing"

	"hermex/internal/buildinfo"
)

// TestSidebarShowsThePanelVersion proves every signed-in page names the release and
// commit the panel binary was built from. The Live monitor reports the version of
// every other daemon, but the panel serving it was the one binary an operator
// could not identify from the page itself.
func TestSidebarShowsThePanelVersion(t *testing.T) {
	oldCommit, oldSemVer, oldTagged := buildinfo.Commit, buildinfo.SemVer, buildinfo.Tagged
	buildinfo.Commit, buildinfo.SemVer, buildinfo.Tagged = "abc1234", "0.1.0", "true"
	t.Cleanup(func() { buildinfo.Commit, buildinfo.SemVer, buildinfo.Tagged = oldCommit, oldSemVer, oldTagged })

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "sidebar", map[string]string{"Nav": "status", "CSRF": "t"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "hermEX 0.1.0 (abc1234)") {
		t.Errorf("the sidebar does not show the panel version:\n%s", buf.String())
	}
}
