package admin

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"hermex/internal/antispam"
	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// retrainSampleCap bounds how many of a folder's most recent messages each
// mailbox contributes to a retrain, so a very large inbox cannot make the job run
// unbounded.
const retrainSampleCap = 500

// minMailboxContribution is the floor under the per-mailbox cap: below it a large
// instance would train on too little of each mailbox to model anything.
const minMailboxContribution = 50

// perMailboxCap bounds one mailbox's contribution to a class. A folder's contents
// are entirely under its owner's control (anyone can file self-authored messages
// into their own Junk, or leave chosen content in their Inbox), so an unbounded
// share is a poisoning primitive against a model that scores mail for everyone:
// the cap makes one mailbox's influence fall as the instance grows, while the
// floor keeps a small instance trainable.
//
// The cap is the only control that holds here. Anything read out of the message
// itself (a Received header proving it arrived, the sender it claims) is written
// by whoever filed it, so it can be fabricated as easily as the label it would
// vouch for. Bounding the share does not depend on the content being honest.
func perMailboxCap(mailboxes int) int {
	if mailboxes <= 1 {
		return retrainSampleCap
	}
	cap := retrainSampleCap / mailboxes
	if cap < minMailboxContribution {
		return minMailboxContribution
	}
	return cap
}

// performBayesRetrain rebuilds the Bayesian spam model from every mailbox, the
// Junk folder as spam, the inbox as ham, and writes it atomically to the path the
// MTA loads at startup. It is the handler for the "bayes-retrain" task. A mailbox
// that fails to open is skipped and recorded, so one bad store cannot fail the
// whole retrain; a mailbox that was never provisioned is skipped, not created.
func (s *Server) performBayesRetrain() (string, error) {
	dirs, err := s.dir.Maildirs()
	if err != nil {
		return "", err
	}
	model := antispam.NewBayesModel()
	limit := perMailboxCap(len(dirs))
	var nspam, nham, nbox int
	for _, dir := range dirs {
		st, err := objectstore.OpenExisting(dir)
		if errors.Is(err, objectstore.ErrNotProvisioned) {
			continue
		}
		if err != nil {
			s.logger.Emit(logging.Event{Level: logging.LevelWarn, Subsystem: logging.Admin,
				Name: "bayes.mailbox_skipped", Err: err.Error()})
			continue
		}
		nspam += trainFolder(st, model, int64(mapi.PrivateFIDJunk), true, limit)
		nham += trainFolder(st, model, int64(mapi.PrivateFIDInbox), false, limit)
		_ = st.Close()
		nbox++
	}
	if err := model.SaveFile(s.paths.AntispamModelPath()); err != nil {
		return "", err
	}
	return msg("tasks.retrained", strconv.Itoa(nspam), strconv.Itoa(nham), strconv.Itoa(nbox)), nil
}

// antispamPageData builds the anti-spam page model: the editable scoring settings
// (the stored row, or the built-in defaults when none has been saved), and the
// status of the Bayesian model and the SpamAssassin ruleset. A setting that could
// not be read is recorded in ReadFailed, and its card shows that in place of its
// form, which would otherwise offer the defaults for a save to store.
func (s *Server) antispamPageData(r *http.Request, notice panelNotice) map[string]any {
	failed := readFailures{}
	data := map[string]any{
		"Nav":        "antispam",
		"CSRF":       csrfCookieValue(r),
		"Notice":     notice,
		"ReadFailed": failed,
	}
	sc, err := s.scoringSettings()
	if s.noteRead(failed, "scoring", "what.scoring", err) {
		data["Weights"] = sc.weights
		data["Threshold"] = sc.threshold
		data["Zones"] = sc.zones
		data["BayesProb"] = sc.bayesProb
	}

	s.addModelStatus(data)
	s.addRulesStatus(data, sc)
	s.addGreylistSettings(data, failed)
	s.addInboundLimits(data, failed)
	s.addOutboundSettings(data, failed)
	s.addAutoReplySettings(data, failed)
	s.addRelaySettings(data, failed)
	s.addGatewaySettings(data, failed)
	s.addDigestSettings(data, failed)
	return data
}

// antispamScoring is the editable scoring configuration the page renders: the
// stored row, or the built-in defaults for whatever it does not set.
type antispamScoring struct {
	weights     antispam.Weights
	threshold   int
	zones       string
	bayesProb   float64
	saThreshold float64
}

// scoringSettings reads the stored scoring settings, falling back to the defaults
// when none has been saved. A failed read returns the defaults with the error.
func (s *Server) scoringSettings() (antispamScoring, error) {
	sc := antispamScoring{
		weights:     antispam.DefaultWeights,
		threshold:   antispam.DefaultThreshold,
		bayesProb:   antispam.DefaultBayesProb,
		saThreshold: antispam.DefaultSAThreshold,
	}
	st, found, err := s.dir.GetAntispamSettings()
	if err != nil || !found {
		return sc, err
	}
	sc.weights = weightsFromSettings(st)
	sc.threshold, sc.zones = st.Threshold, st.Zones
	if st.BayesProb > 0 {
		sc.bayesProb = st.BayesProb
	}
	if st.SAThreshold > 0 {
		sc.saThreshold = st.SAThreshold
	}
	return sc, nil
}

// addModelStatus reports how much the Bayesian model has been trained on. A model
// file that exists but cannot be read is reported, not shown as the cold-start
// model, which only a missing file means.
func (s *Server) addModelStatus(data map[string]any) {
	m, err := antispam.LoadModelFile(s.paths.AntispamModelPath())
	switch {
	case err != nil:
		data["ModelError"] = s.notice("antispam.modelUnread", err)
	case m != nil:
		data["ModelTrained"] = true
		data["SpamMsgs"] = m.SpamMsgs
		data["HamMsgs"] = m.HamMsgs
	}
}

// addRulesStatus reports the live data_dir SpamAssassin ruleset if present,
// otherwise the embedded baseline that the MTA seeds on first run. A ruleset file
// that exists but cannot be read is reported, not shown as the baseline.
func (s *Server) addRulesStatus(data map[string]any, sc antispamScoring) {
	data["SAWeight"] = sc.weights.SARulesHit
	data["SAThreshold"] = sc.saThreshold
	rs := antispam.EmbeddedRules()
	saSource := "antispam.embeddedBaseline"
	live, err := antispam.LoadRulesFile(s.paths.AntispamRulesPath())
	switch {
	case err != nil:
		data["RulesError"] = s.notice("antispam.rulesUnread", err)
		return
	case live != nil:
		rs, saSource = live, "data_dir/"+antispam.RulesFileName
	}
	rules, metas := rs.RuleCount()
	data["SASource"] = saSource
	data["SARules"] = rules
	data["SAMetas"] = metas
	data["SASkipped"] = rs.SkippedRules
	data["SADropped"] = rs.DroppedMetas
}

// addGreylistSettings reports the greylist toggle and its timings: the stored
// values, or the greylister's built-in defaults (300 s delay, 24 h and 36 d TTLs)
// when none has been saved. Each is recorded in failed when it could not be read.
func (s *Server) addGreylistSettings(data map[string]any, failed readFailures) {
	on, err := s.dir.GetGreylistEnabled()
	if s.noteRead(failed, "greylist", "what.greylist", err) {
		data["GreylistEnabled"] = on
	}
	t, found, err := s.dir.GetGreylistTimings()
	if !s.noteRead(failed, "greylist-timings", "what.greylistTimings", err) {
		return
	}
	if !found {
		t = directory.GreylistTimings{MinDelay: 300, UnconfirmedTTL: 86400, ConfirmedTTL: 3110400}
	}
	data["GreylistMinDelay"] = t.MinDelay
	data["GreylistUnconfirmedTTL"] = t.UnconfirmedTTL
	data["GreylistConfirmedTTL"] = t.ConfirmedTTL
}

// addInboundLimits reports the inbound rate limit (built-in default: disabled, 60
// messages per 60 s) and the message size ceiling. The size is shown in whole MB
// (0 = no limit) and stored as bytes; with nothing saved the server enforces its
// built-in ceiling, so show that rather than 0, which would claim a limit the
// server does not actually apply. Each is recorded in failed when it could not be
// read.
func (s *Server) addInboundLimits(data map[string]any, failed readFailures) {
	rl, rlFound, err := s.dir.GetRateLimitSettings()
	if s.noteRead(failed, "ratelimit", "what.rateLimit", err) {
		if !rlFound {
			rl = directory.RateLimitSettings{Burst: 60, WindowSeconds: 60}
		}
		data["RateLimitEnabled"], data["RateLimitBurst"], data["RateLimitWindow"] = rl.Enabled, rl.Burst, rl.WindowSeconds
	}
	ms, msFound, err := s.dir.GetMessageSizeSettings()
	if s.noteRead(failed, "message-size", "what.messageSize", err) {
		if !msFound {
			ms.MaxInboundBytes = directory.DefaultMaxInboundBytes
		}
		data["MessageSizeMB"] = ms.MaxInboundBytes / (1024 * 1024)
	}
}

// addOutboundSettings reports the outbound abuse limit: the stored settings, or the
// limiter's built-in defaults (disabled, 500 external recipients per 3600 s). It is
// recorded in failed when it could not be read.
func (s *Server) addOutboundSettings(data map[string]any, failed readFailures) {
	ob, found, err := s.dir.GetOutboundSettings()
	if !s.noteRead(failed, "outbound", "what.outbound", err) {
		return
	}
	if !found {
		ob = directory.OutboundSettings{RecipientCap: 500, WindowSeconds: 3600}
	}
	data["OutboundEnabled"] = ob.Enabled
	data["OutboundCap"] = ob.RecipientCap
	data["OutboundWindow"] = ob.WindowSeconds
}

// addRelaySettings reports the outbound delivery retry policy: the stored values,
// or the relay worker's built-in defaults (300 s base backoff, 10 attempts). It is
// recorded in failed when it could not be read.
func (s *Server) addRelaySettings(data map[string]any, failed readFailures) {
	rs, found, err := s.dir.GetRelaySettings()
	if !s.noteRead(failed, "relay", "what.relay", err) {
		return
	}
	if !found {
		rs = directory.RelaySettings{BackoffSeconds: 300, MaxAttempts: 10}
	}
	data["RelayBackoff"] = rs.BackoffSeconds
	data["RelayMaxAttempts"] = rs.MaxAttempts
}

// addDigestSettings reports the quarantine digest: the stored settings, or the
// worker's built-in defaults (disabled, every 24 h, no base URL). It also reports
// whether the digest can actually send, because the toggle alone says nothing:
// without a signing secret the worker skips every run, so a panel showing only the
// toggle would report summaries going out that never do. The settings are recorded
// in failed when they could not be read.
func (s *Server) addDigestSettings(data map[string]any, failed readFailures) {
	data["DigestSigningConfigured"] = s.digestSigning
	dg, found, err := s.dir.GetDigestSettings()
	if !s.noteRead(failed, "digest", "what.digest", err) {
		return
	}
	if !found {
		dg = directory.DigestSettings{IntervalHours: 24}
	}
	data["DigestEnabled"] = dg.Enabled
	data["DigestInterval"] = dg.IntervalHours
	data["DigestBaseURL"] = dg.BaseURL
}

// handleUIToggleGreylist turns greylisting on or off. The MTA applies the change
// within about a minute, no restart.
func (s *Server) handleUIToggleGreylist(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	on := r.FormValue("enabled") == "1"
	if err := s.dir.SetGreylistEnabled(on); err != nil {
		s.render(w, r, "greylist-panel", s.antispamPageData(r, s.failNotice("antispam.greylistFailed", err)))
		return
	}
	done := "antispam.greylistDisabled"
	if on {
		done = "antispam.greylistEnabled"
	}
	s.render(w, r, "greylist-panel", s.antispamPageData(r, okNotice(done)))
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
		s.render(w, r, "greylist-panel", s.antispamPageData(r, errorNotice("antispam.timingsInvalid")))
		return
	}
	t := directory.GreylistTimings{MinDelay: int64(minDelay), UnconfirmedTTL: int64(unconfirmedTTL), ConfirmedTTL: int64(confirmedTTL)}
	if err := s.dir.SetGreylistTimings(t); err != nil {
		s.render(w, r, "greylist-panel", s.antispamPageData(r, s.failNotice("antispam.timingsFailed", err)))
		return
	}
	s.render(w, r, "greylist-panel", s.antispamPageData(r, okNotice("antispam.timingsSaved")))
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
		s.render(w, r, "ratelimit-panel", s.antispamPageData(r, errorNotice("antispam.ratelimitInvalid")))
		return
	}
	st := directory.RateLimitSettings{
		Enabled:       r.FormValue("enabled") == "1",
		Burst:         burst,
		WindowSeconds: window,
	}
	if err := s.dir.SetRateLimitSettings(st); err != nil {
		s.render(w, r, "ratelimit-panel", s.antispamPageData(r, s.failNotice("antispam.ratelimitFailed", err)))
		return
	}
	s.render(w, r, "ratelimit-panel", s.antispamPageData(r, okNotice("antispam.ratelimitSaved")))
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
		s.render(w, r, "message-size-panel", s.antispamPageData(r, s.failNotice("antispam.sizeFailed", err)))
		return
	}
	s.render(w, r, "message-size-panel", s.antispamPageData(r, okNotice("antispam.sizeSaved")))
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
		s.render(w, r, "outbound-panel", s.antispamPageData(r, errorNotice("antispam.outboundInvalid")))
		return
	}
	st := directory.OutboundSettings{
		Enabled:       r.FormValue("enabled") == "1",
		RecipientCap:  recipientCap,
		WindowSeconds: window,
	}
	if err := s.dir.SetOutboundSettings(st); err != nil {
		s.render(w, r, "outbound-panel", s.antispamPageData(r, s.failNotice("antispam.outboundFailed", err)))
		return
	}
	s.render(w, r, "outbound-panel", s.antispamPageData(r, okNotice("antispam.outboundSaved")))
}

// addAutoReplySettings reports the stored out-of-office subject prefix, or the
// MTA's built-in default when none has been saved. The form shows what the MTA
// would actually use, so it is recorded in failed rather than offered when the
// row could not be read.
func (s *Server) addAutoReplySettings(data map[string]any, failed readFailures) {
	ar, found, err := s.dir.GetAutoReplySettings()
	if !s.noteRead(failed, "autoreply", "what.autoreply", err) {
		return
	}
	prefix := ar.SubjectPrefix
	if !found || prefix == "" {
		prefix = directory.DefaultAutoReplySubjectPrefix
	}
	data["AutoReplyPrefix"] = prefix
}

// handleUISaveAutoReply persists the out-of-office subject prefix. It is used
// for every mailbox that stores no subject of its own, which is every mailbox
// configured over EWS or ActiveSync, since neither protocol carries a subject
// field. An empty prefix restores the built-in default rather than sending
// replies with no subject at all.
func (s *Server) handleUISaveAutoReply(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	prefix := strings.TrimSpace(r.PostFormValue("subject_prefix"))
	if prefix == "" {
		prefix = directory.DefaultAutoReplySubjectPrefix
	}
	if err := s.dir.SetAutoReplySettings(directory.AutoReplySettings{SubjectPrefix: prefix}); err != nil {
		s.render(w, r, "autoreply-panel", s.antispamPageData(r, s.failNotice("antispam.autoreplyFailed", err)))
		return
	}
	s.render(w, r, "autoreply-panel", s.antispamPageData(r, okNotice("antispam.autoreplySaved")))
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
		s.render(w, r, "relay-panel", s.antispamPageData(r, errorNotice("antispam.relayInvalid")))
		return
	}
	if err := s.dir.SetRelaySettings(directory.RelaySettings{BackoffSeconds: backoff, MaxAttempts: attempts}); err != nil {
		s.render(w, r, "relay-panel", s.antispamPageData(r, s.failNotice("antispam.relayFailed", err)))
		return
	}
	s.render(w, r, "relay-panel", s.antispamPageData(r, okNotice("antispam.relaySaved")))
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
		s.render(w, r, "digest-panel", s.antispamPageData(r, errorNotice("antispam.digestIntervalInvalid")))
		return
	case baseURL != "" && !validBaseURL(baseURL):
		s.render(w, r, "digest-panel", s.antispamPageData(r, errorNotice("antispam.digestURLInvalid")))
		return
	case enabled && baseURL == "":
		s.render(w, r, "digest-panel", s.antispamPageData(r, errorNotice("antispam.digestURLRequired")))
		return
	}
	st := directory.DigestSettings{Enabled: enabled, IntervalHours: interval, BaseURL: baseURL}
	if err := s.dir.SetDigestSettings(st); err != nil {
		s.render(w, r, "digest-panel", s.antispamPageData(r, s.failNotice("antispam.digestFailed", err)))
		return
	}
	notice := okNotice("antispam.digestSaved")
	if enabled && !s.digestSigning {
		notice = warnNotice("antispam.digestNoSecret")
	}
	s.render(w, r, "digest-panel", s.antispamPageData(r, notice))
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
		s.render(w, r, "scoring-panel", s.antispamPageData(r, errorNotice("antispam.thresholdInvalid")))
		return
	}
	bayesProb, saThreshold := formFloat(r, "bayes_prob"), formFloat(r, "sa_threshold")
	if bayesProb <= 0 || bayesProb > 1 {
		s.render(w, r, "scoring-panel", s.antispamPageData(r, errorNotice("antispam.bayesProbInvalid")))
		return
	}
	if saThreshold <= 0 {
		s.render(w, r, "scoring-panel", s.antispamPageData(r, errorNotice("antispam.saThresholdInvalid")))
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
		s.render(w, r, "scoring-panel", s.antispamPageData(r, s.failNotice("antispam.scoringFailed", err)))
		return
	}
	s.render(w, r, "scoring-panel", s.antispamPageData(r, okNotice("antispam.scoringSaved")))
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
	s.render(w, r, "antispam.html", s.antispamPageData(r, panelNotice{}))
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
		s.render(w, r, "antispam-panel", s.antispamPageData(r, s.failNotice("antispam.retrainQueueFailed", err)))
		return
	}
	s.render(w, r, "antispam-panel", s.antispamPageData(r,
		okNotice(msg("antispam.retrainQueued", strconv.FormatInt(id, 10)))))
}

// trainFolder trains the model on up to limit of a folder's most recent messages
// with the given label, returning the number trained.
func trainFolder(st *objectstore.Store, m *antispam.BayesModel, folder int64, spam bool, limit int) int {
	msgs, err := st.ListMessages(folder)
	if err != nil {
		return 0
	}
	if len(msgs) > retrainSampleCap {
		msgs = msgs[len(msgs)-retrainSampleCap:]
	}
	n := 0
	for i := len(msgs) - 1; i >= 0 && n < limit; i-- {
		raw, err := st.GetMessageRaw(folder, msgs[i].UID)
		if err != nil {
			continue
		}
		m.Train(antispam.MessageText(raw), spam)
		n++
	}
	return n
}
