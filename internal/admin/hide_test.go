package admin

import (
	"net/http"
	"net/url"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// TestUIUserHideWritesMask proves the "Hide user from..." checkboxes are folded
// into the single PR_ATTR_HIDDEN mask written to user_properties, so toggling GAL
// and name-resolution actually sets the bits the NSPI layer enforces.
func TestUIUserHideWritesMask(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/hide", session, csrf, url.Values{
		"hide_gal": {"on"},
		"hide_anr": {"on"},
		// hide_al, hide_delegate left unchecked
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hide status %d, want 200", resp.StatusCode)
	}
	got, ok := d.setProps[uint32(mapi.PrAttrHiddenMask)]
	if !ok {
		t.Fatalf("PR_ATTR_HIDDEN mask not written; setProps = %v", d.setProps)
	}
	if got != "9" { // 0x01 (GAL) | 0x08 (ANR)
		t.Errorf("hide mask = %q, want \"9\" (GAL|ANR)", got)
	}
	if d.setPropsUser != "alice@hermex.test" {
		t.Errorf("wrote props for %q, want alice@hermex.test", d.setPropsUser)
	}
}

// TestUIUserHideClears proves unchecking every box clears the property (an empty
// value), so a user with no hide bits leaves no stale row behind.
func TestUIUserHideClears(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/hide", session, csrf, url.Values{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hide status %d, want 200", resp.StatusCode)
	}
	if got, ok := d.setProps[uint32(mapi.PrAttrHiddenMask)]; !ok || got != "" {
		t.Errorf("cleared mask = %q (present=%v), want empty string", got, ok)
	}
}

// TestHideViewOf proves the pre-check decode mirrors the stored mask, including
// the legacy-boolean fallback, so the form reflects what is actually enforced.
func TestHideViewOf(t *testing.T) {
	cases := []struct {
		name  string
		props map[uint32]string
		want  hideView
	}{
		{"GAL+AL mask", map[uint32]string{uint32(mapi.PrAttrHiddenMask): "3"}, hideView{GAL: true, AL: true}},
		{"GAL+ANR mask", map[uint32]string{uint32(mapi.PrAttrHiddenMask): "9"}, hideView{GAL: true, ANR: true}},
		{"all four", map[uint32]string{uint32(mapi.PrAttrHiddenMask): "0x0F"}, hideView{GAL: true, AL: true, Delegate: true, ANR: true}},
		{"legacy boolean expands to GAL+AL", map[uint32]string{uint32(mapi.PrAttrHidden): "1"}, hideView{GAL: true, AL: true}},
		{"nothing stored", map[uint32]string{}, hideView{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hideViewOf(c.props); got != c.want {
				t.Errorf("hideViewOf(%v) = %+v, want %+v", c.props, got, c.want)
			}
		})
	}
}
