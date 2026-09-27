package admin

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
)

// renderPart executes one shared template with no page data and returns its
// output.
func renderPart(t *testing.T, name string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, nil); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestEveryPageCarriesTheConfirmDialog proves each page loads admin.js and holds
// the confirm dialog. htmx falls back to the browser's native confirm() for an
// hx-confirm when nothing takes the question over, so a page missing either one
// would ask through the dialog the panel does not use.
func TestEveryPageCarriesTheConfirmDialog(t *testing.T) {
	head, foot := renderPart(t, "head"), renderPart(t, "foot")
	if !strings.Contains(head, assetURL("admin.js")) {
		t.Errorf("the head does not load admin.js:\n%s", head)
	}
	if !strings.Contains(foot, `id="confirm-dialog"`) {
		t.Errorf("the foot does not hold the confirm dialog:\n%s", foot)
	}

	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		src, err := fs.ReadFile(templateFS, f)
		if err != nil {
			t.Fatal(err)
		}
		page := string(src)
		if strings.Contains(page, `{{template "head"`) && !strings.Contains(page, `{{template "foot"`) {
			t.Errorf("%s opens a page with head but never closes it with foot", f)
		}
	}
}

// TestThemeLoadsBeforeStylesheet proves the theme is applied by a blocking
// script before the stylesheet, so a dark-theme page never paints light first.
func TestThemeLoadsBeforeStylesheet(t *testing.T) {
	head := renderPart(t, "head")
	theme, style := strings.Index(head, assetURL("theme.js")), strings.Index(head, assetURL("style.css"))
	if theme < 0 || theme > style {
		t.Errorf("the head does not load theme.js before the stylesheet:\n%s", head)
	}
}
