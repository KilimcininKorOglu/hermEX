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
		"bayes_prob": {"0.9"}, "sa_threshold": {"4.5"},
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
	if d.settings.BayesProb != 0.9 || d.settings.SAThreshold != 4.5 {
		t.Errorf("bayes/SA cutoffs not persisted: BayesProb=%v SAThreshold=%v, want 0.9 / 4.5", d.settings.BayesProb, d.settings.SAThreshold)
	}
}

// TestSaveAntispamSettingsRejectsBadCutoffs proves a Bayes probability outside 0–1 or
// a non-positive SpamAssassin threshold is rejected and nothing is persisted, so the
// scorer can never be configured to flag every message or never fire those signals.
func TestSaveAntispamSettingsRejectsBadCutoffs(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	base := url.Values{
		"spf_fail": {"5"}, "spf_softfail": {"2"}, "dkim_fail": {"3"}, "dmarc_fail": {"6"},
		"dnsbl_hit": {"6"}, "bayes_spam": {"4"}, "sa_rules_hit": {"4"},
		"threshold": {"10"}, "zones": {""},
	}
	// Bayes probability above 1 is rejected.
	bad := cloneValues(base)
	bad.Set("bayes_prob", "1.5")
	bad.Set("sa_threshold", "5")
	resp := htmxPOST(t, ts, "/admin/ui/antispam/settings", session, csrf, bad)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "between 0 and 1") {
		t.Errorf("expected a Bayes-cutoff validation message:\n%s", body)
	}
	if d.settingsFound {
		t.Error("an invalid Bayes cutoff must not be persisted")
	}
}

// cloneValues copies a url.Values so a test can vary one field without mutating the base.
func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
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

// TestSaveGreylistTimings proves the timings form persists the delay and memory
// windows and acknowledges the save, the values the MTA then polls to apply without a
// restart.
func TestSaveGreylistTimings(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	form := url.Values{"min_delay": {"600"}, "unconfirmed_ttl": {"7200"}, "confirmed_ttl": {"1000000"}}
	resp := htmxPOST(t, ts, "/admin/ui/antispam/greylist-timings", session, csrf, form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Greylist timings saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	if !d.greylistTimingsSet || d.greylistTimings.MinDelay != 600 || d.greylistTimings.UnconfirmedTTL != 7200 || d.greylistTimings.ConfirmedTTL != 1000000 {
		t.Errorf("timings not persisted as entered: set=%v %+v", d.greylistTimingsSet, d.greylistTimings)
	}
}

// TestSaveGreylistTimingsRejectsBadValues proves a delay or memory window below 1
// (which would remove the delay or collapse a window) is rejected and nothing persists.
func TestSaveGreylistTimingsRejectsBadValues(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/greylist-timings", session, csrf,
		url.Values{"min_delay": {"0"}, "unconfirmed_ttl": {"7200"}, "confirmed_ttl": {"1000000"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "at least 1 second") {
		t.Errorf("expected a validation message:\n%s", body)
	}
	if d.greylistTimingsSet {
		t.Error("invalid greylist timings must not be persisted")
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

// TestSaveRateLimitSettings proves the rate-limit form persists the toggle, burst,
// and window and acknowledges the save.
func TestSaveRateLimitSettings(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	form := url.Values{"enabled": {"1"}, "burst": {"120"}, "window": {"30"}}
	resp := htmxPOST(t, ts, "/admin/ui/antispam/ratelimit", session, csrf, form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Rate-limit settings saved") {
		t.Errorf("response missing acknowledgment:\n%s", body)
	}
	if !d.rateLimitFound || !d.rateLimit.Enabled || d.rateLimit.Burst != 120 || d.rateLimit.WindowSeconds != 30 {
		t.Errorf("settings not persisted as entered: found=%v %+v", d.rateLimitFound, d.rateLimit)
	}
}

// TestSaveRateLimitRejectsBadValues proves a burst or window below 1 (which would
// admit no mail or collapse the window) is rejected and nothing is persisted.
func TestSaveRateLimitRejectsBadValues(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/ratelimit", session, csrf,
		url.Values{"enabled": {"1"}, "burst": {"0"}, "window": {"60"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "at least 1") {
		t.Errorf("expected a validation message:\n%s", body)
	}
	if d.rateLimitFound {
		t.Error("invalid rate-limit settings must not be persisted")
	}
}

// TestSaveMessageSize proves the message-size form converts the entered MB to bytes
// and persists it, the value the MTA then polls to advertise and enforce SMTP SIZE
// without a restart.
func TestSaveMessageSize(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/message-size", session, csrf, url.Values{"max_mb": {"25"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Message size limit saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	if !d.messageSizeFound || d.messageSize.MaxInboundBytes != 25*1024*1024 {
		t.Errorf("limit not persisted as bytes: found=%v %+v, want 26214400", d.messageSizeFound, d.messageSize)
	}
}

// TestSaveMessageSizeZeroDisables proves entering 0 persists a zero limit, the MTA's
// signal to advertise no SIZE and accept any size.
func TestSaveMessageSizeZeroDisables(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/message-size", session, csrf, url.Values{"max_mb": {"0"}})
	resp.Body.Close()
	if !d.messageSizeFound || d.messageSize.MaxInboundBytes != 0 {
		t.Errorf("zero limit not persisted: found=%v %+v", d.messageSizeFound, d.messageSize)
	}
}

// TestSaveRelaySettings proves the relay form persists the backoff and attempt limit
// and acknowledges the save, the values the MTA then polls to apply without a restart.
func TestSaveRelaySettings(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/relay", session, csrf, url.Values{"backoff": {"120"}, "attempts": {"5"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Relay settings saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	if !d.relayFound || d.relay.BackoffSeconds != 120 || d.relay.MaxAttempts != 5 {
		t.Errorf("relay settings not persisted as entered: found=%v %+v", d.relayFound, d.relay)
	}
}

// TestSaveRelaySettingsRejectsBadValues proves a backoff or attempt count below 1 is
// rejected and nothing is persisted.
func TestSaveRelaySettingsRejectsBadValues(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/relay", session, csrf, url.Values{"backoff": {"0"}, "attempts": {"5"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "at least 1") {
		t.Errorf("expected a validation message:\n%s", body)
	}
	if d.relayFound {
		t.Error("invalid relay settings must not be persisted")
	}
}

// TestSaveOutboundSettings proves the outbound-abuse form persists the toggle, cap,
// and window and acknowledges the save.
func TestSaveOutboundSettings(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	form := url.Values{"enabled": {"1"}, "cap": {"250"}, "window": {"1800"}}
	resp := htmxPOST(t, ts, "/admin/ui/antispam/outbound", session, csrf, form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Outbound settings saved") {
		t.Errorf("response missing acknowledgment:\n%s", body)
	}
	if !d.outboundFound || !d.outbound.Enabled || d.outbound.RecipientCap != 250 || d.outbound.WindowSeconds != 1800 {
		t.Errorf("settings not persisted as entered: found=%v %+v", d.outboundFound, d.outbound)
	}
}

// TestSaveOutboundRejectsBadValues proves a cap or window below 1 is rejected and
// nothing is persisted.
func TestSaveOutboundRejectsBadValues(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/outbound", session, csrf,
		url.Values{"enabled": {"1"}, "cap": {"0"}, "window": {"3600"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "at least 1") {
		t.Errorf("expected a validation message:\n%s", body)
	}
	if d.outboundFound {
		t.Error("invalid outbound settings must not be persisted")
	}
}

// TestSaveDigestSettings proves the digest form persists the toggle, interval, and
// base URL and acknowledges the save.
func TestSaveDigestSettings(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	form := url.Values{"enabled": {"1"}, "interval": {"12"}, "base_url": {"https://mail.example.com"}}
	resp := htmxPOST(t, ts, "/admin/ui/antispam/digest", session, csrf, form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Digest settings saved") {
		t.Errorf("response missing acknowledgment:\n%s", body)
	}
	if !d.digestFound || !d.digest.Enabled || d.digest.IntervalHours != 12 || d.digest.BaseURL != "https://mail.example.com" {
		t.Errorf("settings not persisted as entered: found=%v %+v", d.digestFound, d.digest)
	}
}

// TestSaveDigestRejectsBadValues proves an interval below 1, a non-http base URL, and
// enabling with no base URL are each rejected and nothing is persisted.
func TestSaveDigestRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
		want string
	}{
		{"interval", url.Values{"enabled": {"1"}, "interval": {"0"}, "base_url": {"https://m.test"}}, "at least 1 hour"},
		{"bad url", url.Values{"enabled": {"1"}, "interval": {"24"}, "base_url": {"mail.example.com"}}, "http(s) address"},
		{"enable no url", url.Values{"enabled": {"1"}, "interval": {"24"}, "base_url": {""}}, "base URL is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
			ts := adminServer(t, d)
			session, csrf := loginCookies(t, ts)

			resp := htmxPOST(t, ts, "/admin/ui/antispam/digest", session, csrf, tc.form)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(body), tc.want) {
				t.Errorf("expected %q in response:\n%s", tc.want, body)
			}
			if d.digestFound {
				t.Error("invalid digest settings must not be persisted")
			}
		})
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
