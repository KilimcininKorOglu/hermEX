package admin

import (
	"fmt"
	"net/http"
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
	if st, found, err := s.dir.GetAntispamSettings(); err == nil && found {
		w = weightsFromSettings(st)
		threshold, zones = st.Threshold, st.Zones
	}
	data["Weights"] = w
	data["Threshold"] = threshold
	data["Zones"] = zones

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
	data["SAThreshold"] = antispam.SAScoreThreshold

	if on, err := s.dir.GetGreylistEnabled(); err == nil {
		data["GreylistEnabled"] = on
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
