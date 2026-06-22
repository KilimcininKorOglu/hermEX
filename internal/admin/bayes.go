package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"hermex/internal/antispam"
	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// retrainSampleCap bounds how many of a folder's most recent messages each
// mailbox contributes to a retrain, so a very large inbox cannot make the job run
// unbounded.
const retrainSampleCap = 500

// performBayesRetrain rebuilds the Bayesian spam model from every mailbox — the
// Junk folder as spam, the inbox as ham — and writes it atomically to the path the
// MTA loads at startup. It is the handler for the "bayes-retrain" task. A mailbox
// that fails to open is skipped, so one bad store cannot fail the whole retrain.
func (s *Server) performBayesRetrain() (string, error) {
	dirs, err := s.dir.Maildirs()
	if err != nil {
		return "", err
	}
	model := antispam.NewBayesModel()
	var nspam, nham, nbox int
	for _, dir := range dirs {
		st, err := objectstore.Open(dir)
		if err != nil {
			continue
		}
		nspam += trainFolder(st, model, int64(mapi.PrivateFIDJunk), true)
		nham += trainFolder(st, model, int64(mapi.PrivateFIDInbox), false)
		st.Close()
		nbox++
	}
	if err := model.SaveFile(s.paths.AntispamModelPath()); err != nil {
		return "", err
	}
	return fmt.Sprintf("Retrained on %d spam + %d ham messages from %d mailboxes.", nspam, nham, nbox), nil
}

// antispamPageData builds the anti-spam page model: the editable scoring settings
// (the stored row, or the built-in defaults when none has been saved), and the
// status of the Bayesian model and the SpamAssassin ruleset.
func (s *Server) antispamPageData(r *http.Request, notice string) map[string]any {
	data := map[string]any{
		"Nav":    "antispam",
		"CSRF":   csrfCookieValue(r),
		"Notice": notice,
	}
	w, threshold, zones := antispam.DefaultWeights, antispam.DefaultThreshold, ""
	bayesProb, saThreshold := antispam.DefaultBayesProb, antispam.DefaultSAThreshold
	if st, found, err := s.dir.GetAntispamSettings(); err == nil && found {
		w = weightsFromSettings(st)
		threshold, zones = st.Threshold, st.Zones
		if st.BayesProb > 0 {
			bayesProb = st.BayesProb
		}
		if st.SAThreshold > 0 {
			saThreshold = st.SAThreshold
		}
	}
	data["Weights"] = w
	data["Threshold"] = threshold
	data["Zones"] = zones
	data["BayesProb"] = bayesProb

	if m, err := antispam.LoadModelFile(s.paths.AntispamModelPath()); err == nil && m != nil {
		data["ModelTrained"] = true
		data["SpamMsgs"] = m.SpamMsgs
		data["HamMsgs"] = m.HamMsgs
	}

	// SpamAssassin ruleset: report the live data_dir ruleset if present, otherwise
	// the embedded baseline that the MTA seeds on first run.
	rs := antispam.EmbeddedRules()
	saSource := "embedded baseline (seeded on first run)"
	if live, err := antispam.LoadRulesFile(s.paths.AntispamRulesPath()); err == nil && live != nil {
		rs, saSource = live, "data_dir/"+antispam.RulesFileName
	}
	rules, metas := rs.RuleCount()
	data["SASource"] = saSource
	data["SARules"] = rules
	data["SAMetas"] = metas
	data["SASkipped"] = rs.SkippedRules
	data["SADropped"] = rs.DroppedMetas
	data["SAWeight"] = w.SARulesHit
	data["SAThreshold"] = saThreshold

	if on, err := s.dir.GetGreylistEnabled(); err == nil {
		data["GreylistEnabled"] = on
	}
	// Greylist timings: the stored values, or the greylister's built-in defaults
	// (300 s delay, 24 h and 36 d TTLs) when none has been saved.
	data["GreylistMinDelay"], data["GreylistUnconfirmedTTL"], data["GreylistConfirmedTTL"] = int64(300), int64(86400), int64(3110400)
	if t, found, err := s.dir.GetGreylistTimings(); err == nil && found {
		data["GreylistMinDelay"] = t.MinDelay
		data["GreylistUnconfirmedTTL"] = t.UnconfirmedTTL
		data["GreylistConfirmedTTL"] = t.ConfirmedTTL
	}

	// Inbound rate limiting: the stored settings, or the limiter's built-in defaults
	// (disabled, 60 messages per 60 s) when none has been saved.
	data["RateLimitEnabled"], data["RateLimitBurst"], data["RateLimitWindow"] = false, 60, 60
	if rl, found, err := s.dir.GetRateLimitSettings(); err == nil && found {
		data["RateLimitEnabled"] = rl.Enabled
		data["RateLimitBurst"] = rl.Burst
		data["RateLimitWindow"] = rl.WindowSeconds
	}

	// Inbound message size limit: shown in whole MB (0 = no limit); stored as bytes.
	data["MessageSizeMB"] = int64(0)
	if ms, found, err := s.dir.GetMessageSizeSettings(); err == nil && found {
		data["MessageSizeMB"] = ms.MaxInboundBytes / (1024 * 1024)
	}

	// Outbound abuse limiting: the stored settings, or the limiter's built-in
	// defaults (disabled, 500 external recipients per 3600 s) when none has been saved.
	data["OutboundEnabled"], data["OutboundCap"], data["OutboundWindow"] = false, 500, 3600
	if ob, found, err := s.dir.GetOutboundSettings(); err == nil && found {
		data["OutboundEnabled"] = ob.Enabled
		data["OutboundCap"] = ob.RecipientCap
		data["OutboundWindow"] = ob.WindowSeconds
	}

	// Outbound delivery retry policy: the stored values, or the relay worker's built-in
	// defaults (300 s base backoff, 10 attempts) when none has been saved.
	data["RelayBackoff"], data["RelayMaxAttempts"] = 300, 10
	if rs, found, err := s.dir.GetRelaySettings(); err == nil && found {
		data["RelayBackoff"] = rs.BackoffSeconds
		data["RelayMaxAttempts"] = rs.MaxAttempts
	}

	// Quarantine digest: the stored settings, or the worker's built-in defaults
	// (disabled, every 24 h, no base URL) when none has been saved.
	data["DigestEnabled"], data["DigestInterval"], data["DigestBaseURL"] = false, 24, ""
	if dg, found, err := s.dir.GetDigestSettings(); err == nil && found {
		data["DigestEnabled"] = dg.Enabled
		data["DigestInterval"] = dg.IntervalHours
		data["DigestBaseURL"] = dg.BaseURL
	}
	return data
}

// handleUIToggleGreylist turns greylisting on or off. The MTA applies the change
// within about a minute, no restart.
func (s *Server) handleUIToggleGreylist(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	on := r.FormValue("enabled") == "1"
	if err := s.dir.SetGreylistEnabled(on); err != nil {
		s.render(w, "greylist-panel", s.antispamPageData(r, "Could not change greylisting: "+err.Error()))
		return
	}
	verb := "disabled"
	if on {
		verb = "enabled"
	}
	s.render(w, "greylist-panel", s.antispamPageData(r, "Greylisting "+verb+" — the MTA applies it within about a minute."))
}

// handleUISaveGreylistTimings persists the greylist timings (the minimum delay before
// a first-seen sender is accepted and the unconfirmed/confirmed sender memory, all in
// seconds). The MTA applies them within about a minute, no restart. A value below 1
// for any field is rejected so the delay is never removed nor a memory window collapsed.
func (s *Server) handleUISaveGreylistTimings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	minDelay, unconfirmedTTL, confirmedTTL := formInt(r, "min_delay"), formInt(r, "unconfirmed_ttl"), formInt(r, "confirmed_ttl")
	if minDelay < 1 || unconfirmedTTL < 1 || confirmedTTL < 1 {
		s.render(w, "greylist-panel", s.antispamPageData(r, "The greylist delay and memory windows must each be at least 1 second; timings not saved."))
		return
	}
	t := directory.GreylistTimings{MinDelay: int64(minDelay), UnconfirmedTTL: int64(unconfirmedTTL), ConfirmedTTL: int64(confirmedTTL)}
	if err := s.dir.SetGreylistTimings(t); err != nil {
		s.render(w, "greylist-panel", s.antispamPageData(r, "Could not save the greylist timings: "+err.Error()))
		return
	}
	s.render(w, "greylist-panel", s.antispamPageData(r, "Greylist timings saved — the MTA applies them within a minute, no restart."))
}

// handleUISaveRateLimit persists the inbound rate-limit settings (enable, burst, and
// window). The MTA applies the change within about a minute, no restart. A burst or
// window below 1 is rejected so the limiter is never configured to admit zero
// messages or collapse its window.
func (s *Server) handleUISaveRateLimit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	burst, window := formInt(r, "burst"), formInt(r, "window")
	if burst < 1 || window < 1 {
		s.render(w, "ratelimit-panel", s.antispamPageData(r, "Burst and window must each be at least 1; settings not saved."))
		return
	}
	st := directory.RateLimitSettings{
		Enabled:       r.FormValue("enabled") == "1",
		Burst:         burst,
		WindowSeconds: window,
	}
	if err := s.dir.SetRateLimitSettings(st); err != nil {
		s.render(w, "ratelimit-panel", s.antispamPageData(r, "Could not save rate-limit settings: "+err.Error()))
		return
	}
	s.render(w, "ratelimit-panel", s.antispamPageData(r, "Rate-limit settings saved — the MTA applies them within a minute, no restart."))
}

// handleUISaveMessageSize persists the inbound message size limit (entered in whole
// MB; 0 disables the limit). The MTA applies the change within about a minute, no
// restart, advertising it as the SMTP SIZE extension.
func (s *Server) handleUISaveMessageSize(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	mb := formInt(r, "max_mb") // megabytes; 0 disables the limit, negatives clamp to 0
	if err := s.dir.SetMessageSizeSettings(directory.MessageSizeSettings{MaxInboundBytes: int64(mb) * 1024 * 1024}); err != nil {
		s.render(w, "message-size-panel", s.antispamPageData(r, "Could not save the message size limit: "+err.Error()))
		return
	}
	s.render(w, "message-size-panel", s.antispamPageData(r, "Message size limit saved — the MTA applies it within a minute, no restart."))
}

// handleUISaveOutbound persists the outbound-abuse settings (enable, external-recipient
// cap, and window). The MTA applies the change within about a minute, no restart. A cap
// or window below 1 is rejected.
func (s *Server) handleUISaveOutbound(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	recipientCap, window := formInt(r, "cap"), formInt(r, "window")
	if recipientCap < 1 || window < 1 {
		s.render(w, "outbound-panel", s.antispamPageData(r, "Recipient cap and window must each be at least 1; settings not saved."))
		return
	}
	st := directory.OutboundSettings{
		Enabled:       r.FormValue("enabled") == "1",
		RecipientCap:  recipientCap,
		WindowSeconds: window,
	}
	if err := s.dir.SetOutboundSettings(st); err != nil {
		s.render(w, "outbound-panel", s.antispamPageData(r, "Could not save outbound settings: "+err.Error()))
		return
	}
	s.render(w, "outbound-panel", s.antispamPageData(r, "Outbound settings saved — the MTA applies them within a minute, no restart."))
}

// handleUISaveRelay persists the outbound delivery retry policy (base backoff in
// seconds and the number of attempts before giving up). The MTA applies the change
// within about a minute, no restart. A value below 1 for either is rejected.
func (s *Server) handleUISaveRelay(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	backoff, attempts := formInt(r, "backoff"), formInt(r, "attempts")
	if backoff < 1 || attempts < 1 {
		s.render(w, "relay-panel", s.antispamPageData(r, "The backoff and attempts must each be at least 1; settings not saved."))
		return
	}
	if err := s.dir.SetRelaySettings(directory.RelaySettings{BackoffSeconds: backoff, MaxAttempts: attempts}); err != nil {
		s.render(w, "relay-panel", s.antispamPageData(r, "Could not save the relay settings: "+err.Error()))
		return
	}
	s.render(w, "relay-panel", s.antispamPageData(r, "Relay settings saved — the MTA applies them within a minute, no restart."))
}

// handleUISaveDigest persists the quarantine-digest settings (enable, interval in
// hours, and the externally-reachable base URL release links are built from). The MTA
// applies the change on its next poll. An interval below 1, a base URL that is not an
// http(s) address, or enabling with no base URL is rejected.
func (s *Server) handleUISaveDigest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	enabled := r.FormValue("enabled") == "1"
	interval := formInt(r, "interval")
	baseURL := strings.TrimSpace(r.FormValue("base_url"))
	switch {
	case interval < 1:
		s.render(w, "digest-panel", s.antispamPageData(r, "The interval must be at least 1 hour; settings not saved."))
		return
	case baseURL != "" && !validBaseURL(baseURL):
		s.render(w, "digest-panel", s.antispamPageData(r, "The base URL must be a full http(s) address; settings not saved."))
		return
	case enabled && baseURL == "":
		s.render(w, "digest-panel", s.antispamPageData(r, "A base URL is required to enable the digest; settings not saved."))
		return
	}
	st := directory.DigestSettings{Enabled: enabled, IntervalHours: interval, BaseURL: baseURL}
	if err := s.dir.SetDigestSettings(st); err != nil {
		s.render(w, "digest-panel", s.antispamPageData(r, "Could not save digest settings: "+err.Error()))
		return
	}
	s.render(w, "digest-panel", s.antispamPageData(r, "Digest settings saved — the MTA applies them within a minute, no restart."))
}

// validBaseURL reports whether s is a full http(s) URL with a host, the form a release
// link can be built from.
func validBaseURL(s string) bool {
	u, err := url.ParseRequestURI(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// weightsFromSettings maps a stored settings row to antispam.Weights for display.
func weightsFromSettings(st directory.AntispamSettings) antispam.Weights {
	return antispam.Weights{
		SPFFail: st.SPFFail, SPFSoftFail: st.SPFSoftFail, DKIMFail: st.DKIMFail, DMARCFail: st.DMARCFail,
		DNSBLHit: st.DNSBLHit, BayesSpam: st.BayesSpam, SARulesHit: st.SARulesHit,
	}
}

// handleUISaveAntispamSettings persists edited scoring settings. The MTA
// hot-reloads them within about a minute, with no restart.
func (s *Server) handleUISaveAntispamSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	if threshold := formInt(r, "threshold"); threshold < 1 {
		s.render(w, "scoring-panel", s.antispamPageData(r, "Threshold must be at least 1; settings not saved."))
		return
	}
	bayesProb, saThreshold := formFloat(r, "bayes_prob"), formFloat(r, "sa_threshold")
	if bayesProb <= 0 || bayesProb > 1 {
		s.render(w, "scoring-panel", s.antispamPageData(r, "The Bayes probability cutoff must be between 0 and 1; settings not saved."))
		return
	}
	if saThreshold <= 0 {
		s.render(w, "scoring-panel", s.antispamPageData(r, "The SpamAssassin score threshold must be greater than 0; settings not saved."))
		return
	}
	st := directory.AntispamSettings{
		SPFFail:     formInt(r, "spf_fail"),
		SPFSoftFail: formInt(r, "spf_softfail"),
		DKIMFail:    formInt(r, "dkim_fail"),
		DMARCFail:   formInt(r, "dmarc_fail"),
		DNSBLHit:    formInt(r, "dnsbl_hit"),
		BayesSpam:   formInt(r, "bayes_spam"),
		SARulesHit:  formInt(r, "sa_rules_hit"),
		Threshold:   formInt(r, "threshold"),
		Zones:       strings.TrimSpace(r.FormValue("zones")),
		BayesProb:   bayesProb,
		SAThreshold: saThreshold,
	}
	if err := s.dir.SetAntispamSettings(st); err != nil {
		s.render(w, "scoring-panel", s.antispamPageData(r, "Could not save settings: "+err.Error()))
		return
	}
	s.render(w, "scoring-panel", s.antispamPageData(r, "Settings saved — the MTA applies them within a minute, no restart."))
}

// formInt reads a non-negative integer form field, returning 0 when absent or
// unparseable.
func formInt(r *http.Request, name string) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// formFloat reads a non-negative float form field, returning 0 when absent or
// unparseable so the caller's range check rejects it.
func formFloat(r *http.Request, name string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue(name)), 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// handleUIAntispam renders the anti-spam page (system admins).
func (s *Server) handleUIAntispam(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "antispam.html", s.antispamPageData(r, ""))
}

// handleUIRetrainBayes enqueues a Bayesian model retrain as an async task and
// re-renders the page acknowledging it; the result appears on the Task queue.
func (s *Server) handleUIRetrainBayes(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiAuthorized(w, r)
	if !ok {
		return
	}
	id, err := s.dir.CreateTask("bayes-retrain", "", cl.Login)
	if err != nil {
		s.render(w, "antispam-panel", s.antispamPageData(r, "Could not queue the retrain: "+err.Error()))
		return
	}
	s.render(w, "antispam-panel", s.antispamPageData(r,
		fmt.Sprintf("Retrain queued as task #%d — watch the Task queue for its result.", id)))
}

// trainFolder trains the model on up to retrainSampleCap of a folder's most recent
// messages with the given label, returning the number trained.
func trainFolder(st *objectstore.Store, m *antispam.BayesModel, folder int64, spam bool) int {
	msgs, err := st.ListMessages(folder)
	if err != nil {
		return 0
	}
	if len(msgs) > retrainSampleCap {
		msgs = msgs[len(msgs)-retrainSampleCap:]
	}
	n := 0
	for _, mi := range msgs {
		raw, err := st.GetMessageRaw(folder, mi.UID)
		if err != nil {
			continue
		}
		m.Train(antispam.MessageText(raw), spam)
		n++
	}
	return n
}
