package admin

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
)

// TestEveryPageCarriesTheConfirmDialog proves each page loads admin.js and holds
// the confirm dialog. htmx falls back to the browser's native confirm() for an
// hx-confirm when nothing takes the question over, so a page missing either one
// would ask through the dialog the panel does not use.
func TestEveryPageCarriesTheConfirmDialog(t *testing.T) {
	var head, foot bytes.Buffer
	if err := tmpl.ExecuteTemplate(&head, "head", nil); err != nil {
		t.Fatal(err)
	}
	if err := tmpl.ExecuteTemplate(&foot, "foot", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(head.String(), assetURL("admin.js")) {
		t.Errorf("the head does not load admin.js:\n%s", head.String())
	}
	if !strings.Contains(foot.String(), `id="confirm-dialog"`) {
		t.Errorf("the foot does not hold the confirm dialog:\n%s", foot.String())
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
