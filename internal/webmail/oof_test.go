package webmail

import (
	"net/url"
	"strings"
	"testing"

	"hermex/internal/objectstore"
)

// oofOf reads the mailbox's stored out-of-office settings.
func oofOf(t *testing.T, path string) objectstore.OOFSettings {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg, err := st.GetOOFSettings()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestOOFFormRoundTrip enables out-of-office through the form and checks the
// settings are stored and shown back on the page.
func TestOOFFormRoundTrip(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := postForm(t, c, ts.URL+"/oof", url.Values{
		"enabled":           {"1"},
		"internalsubject":   {"On vacation"},
		"internalreply":     {"Back Monday."},
		"externalenabled":   {"1"},
		"externalsubject":   {"Out of office"},
		"externalreply":     {"Away from email."},
		"externalknownonly": {"1"},
	}); code != 200 && code != 303 {
		t.Fatalf("submit = %d", code)
	}

	cfg := oofOf(t, path)
	if !cfg.Enabled {
		t.Error("out-of-office not enabled after submit")
	}
	if cfg.InternalSubject != "On vacation" || cfg.InternalReply != "Back Monday." {
		t.Errorf("stored cfg = %+v, want internal subject/reply set", cfg)
	}
	// The full form must carry every external field too, or a Save would zero them.
	if !cfg.ExternalEnabled || cfg.ExternalSubject != "Out of office" || cfg.ExternalReply != "Away from email." ||
		cfg.ExternalAudience != objectstore.OOFExternalKnown {
		t.Errorf("stored cfg = %+v, want external subject/reply/known-only audience set", cfg)
	}

	_, body := get(t, c, ts.URL+"/oof")
	for _, want := range []string{"On vacation", "Back Monday.", "Out of office", "Away from email.", "checked"} {
		if !strings.Contains(body, want) {
			t.Errorf("form missing %q:\n%s", want, body)
		}
	}
}

// TestOOFDisable turns out-of-office off: an unchecked box submits no "enabled"
// field, which must clear the flag.
func TestOOFDisable(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	postForm(t, c, ts.URL+"/oof", url.Values{"enabled": {"1"}, "internalsubject": {"Away"}})
	if !oofOf(t, path).Enabled {
		t.Fatal("precondition: should be enabled")
	}
	// Resubmit without "enabled" (the box was unchecked).
	postForm(t, c, ts.URL+"/oof", url.Values{"internalsubject": {"Away"}})
	if oofOf(t, path).Enabled {
		t.Error("out-of-office still enabled after unchecking the box")
	}
}

// TestOOFScheduleStored checks the optional datetime-local window is parsed and
// stored as unix bounds.
func TestOOFScheduleStored(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	postForm(t, c, ts.URL+"/oof", url.Values{
		"enabled": {"1"},
		"start":   {"2026-06-01T09:00"},
		"end":     {"2026-06-10T17:00"},
	})
	cfg := oofOf(t, path)
	if cfg.Start == 0 || cfg.End == 0 {
		t.Fatalf("schedule not stored: start=%d end=%d", cfg.Start, cfg.End)
	}
	if cfg.End <= cfg.Start {
		t.Errorf("end %d not after start %d", cfg.End, cfg.Start)
	}
}

// TestOOFExternalRelayNotice locks the fail-loud copy: the external-reply
// section must state that external replies are not delivered yet (no outbound
// relay), so a user is not misled into thinking external senders are answered.
func TestOOFExternalRelayNotice(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, body := get(t, c, ts.URL+"/oof")
	if !strings.Contains(body, "outbound mail relay") || !strings.Contains(body, "not available yet") {
		t.Errorf("out-of-office page is missing the external-relay limitation notice:\n%s", body)
	}
}
