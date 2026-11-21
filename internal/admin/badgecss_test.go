package admin

import (
	"bytes"
	"os"
	"testing"
)

// TestSignInBadgeStylesAreShared: the webmail and admin sign-in pages share one
// look, defined in badge.css. The panel embeds its copy and the SPA bundles its
// own, so the two files must stay byte-identical or the pages drift apart.
func TestSignInBadgeStylesAreShared(t *testing.T) {
	admin, err := staticFS.ReadFile("static/badge.css")
	if err != nil {
		t.Fatalf("read the embedded admin copy: %v", err)
	}
	webmail, err := os.ReadFile("../webmail2/src/styles/badge.css")
	if err != nil {
		t.Fatalf("read the webmail copy: %v", err)
	}
	if !bytes.Equal(admin, webmail) {
		t.Fatal("internal/admin/static/badge.css and internal/webmail2/src/styles/badge.css differ; copy one over the other")
	}
}
