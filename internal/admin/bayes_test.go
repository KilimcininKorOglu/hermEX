package admin

import (
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/antispam"
	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestAntispamPageRenders proves the anti-spam page renders for a system admin
// with the retrain control and the scoring weights.
func TestAntispamPageRenders(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/antispam", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("antispam page = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	if !strings.Contains(page, "Retrain from mailboxes") || !strings.Contains(page, "Bayesian spam") {
		t.Errorf("antispam page missing expected content:\n%s", page)
	}
	// The SpamAssassin ruleset section renders, reporting the embedded baseline
	// (the test's data_dir has no ruleset file, so it falls back to the baseline).
	if !strings.Contains(page, "SpamAssassin ruleset") || !strings.Contains(page, "embedded baseline") {
		t.Errorf("antispam page missing the SpamAssassin ruleset status:\n%s", page)
	}
}

// TestRetrainEnqueues proves the retrain button queues a bayes-retrain task.
func TestRetrainEnqueues(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/retrain", session, csrf, url.Values{})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retrain POST = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Retrain queued") {
		t.Errorf("retrain response missing acknowledgment:\n%s", body)
	}
	if len(d.tasks) != 1 || d.tasks[0].Type != "bayes-retrain" {
		t.Errorf("tasks = %+v, want one bayes-retrain task", d.tasks)
	}
}

// TestSaveAntispamSettings proves the settings form persists the edited weights,
// threshold, and zones and acknowledges the save.
func TestSaveAntispamSettings(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	form := url.Values{
		"spf_fail": {"5"}, "spf_softfail": {"2"}, "dkim_fail": {"3"}, "dmarc_fail": {"6"},
		"dnsbl_hit": {"6"}, "bayes_spam": {"4"}, "sa_rules_hit": {"4"},
		"threshold": {"10"}, "zones": {"zen.example,bl.example"},
	}
	resp := htmxPOST(t, ts, "/admin/ui/antispam/settings", session, csrf, form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save settings = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Settings saved") {
		t.Errorf("save response missing acknowledgment:\n%s", body)
	}
	if !d.settingsFound || d.settings.Threshold != 10 || d.settings.DMARCFail != 6 || d.settings.Zones != "zen.example,bl.example" {
		t.Errorf("settings not persisted as entered: found=%v %+v", d.settingsFound, d.settings)
	}
}

// TestToggleGreylist proves the greylisting toggle persists the new state and
// acknowledges it.
func TestToggleGreylist(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/greylist", session, csrf, url.Values{"enabled": {"1"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "enabled") {
		t.Fatalf("toggle = %d body=%q, want 200 acknowledging enable", resp.StatusCode, body)
	}
	if !d.greylistOn {
		t.Error("greylisting should have been turned on")
	}
}

// TestSaveAntispamSettingsRejectsBadThreshold proves a threshold below 1 (which
// would flag every message as spam) is rejected and nothing is persisted.
func TestSaveAntispamSettingsRejectsBadThreshold(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/settings", session, csrf, url.Values{"threshold": {"0"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "at least 1") {
		t.Errorf("expected a threshold validation message:\n%s", body)
	}
	if d.settingsFound {
		t.Error("an invalid threshold must not be persisted")
	}
}

// TestPerformBayesRetrain proves the retrain task reads each mailbox's Junk as
// spam and inbox as ham, trains a model, and writes it to the MTA's model path.
func TestPerformBayesRetrain(t *testing.T) {
	tmp := t.TempDir()
	mbox := filepath.Join(tmp, "alice")

	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Now()
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDJunk), []byte("Subject: cheap pills\r\n\r\nbuy now discount viagra"), when, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("Subject: meeting\r\n\r\nproject schedule review"), when, 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	d := &fakeDir{maildirs: []string{mbox}}
	s := NewServer(d, fakePaths{root: tmp}, []byte("secret"))

	msg, err := s.performBayesRetrain()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "1 spam") || !strings.Contains(msg, "1 ham") {
		t.Errorf("retrain summary = %q, want 1 spam + 1 ham", msg)
	}

	model, err := antispam.LoadModelFile(fakePaths{root: tmp}.AntispamModelPath())
	if err != nil || model == nil {
		t.Fatalf("model file = (%v, %v), want a trained model", model, err)
	}
	if model.SpamMsgs != 1 || model.HamMsgs != 1 {
		t.Errorf("model counts = spam %d ham %d, want 1/1", model.SpamMsgs, model.HamMsgs)
	}
}
