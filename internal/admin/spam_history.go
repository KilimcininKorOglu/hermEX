package admin

import (
	"net/http"
	"time"

	"hermex/internal/directory"
)

// defaultSpamHistoryRetainDisplay mirrors the directory's built-in retention bound,
// shown on the page until an operator saves one. The directory's own constant is
// unexported, so this restates the same value for display only.
const defaultSpamHistoryRetainDisplay = 10000

// spamVerdictView is one recorded spam verdict rendered for the Spam History page.
type spamVerdictView struct {
	Time       string
	MailFrom   string
	RemoteAddr string
	Score      int
	Spam       bool
	Reasons    string
}

// handleUISpamHistory renders the Spam History page: the most recent inbound
// messages the MTA scored, with the score and the reasons each one fired, so an
// admin can review what was filed as spam and debug a false positive.
func (s *Server) handleUISpamHistory(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, r, "spam-history.html", s.spamHistoryPageData(r, panelNotice{}))
}

// spamHistoryPageData builds the Spam History page model: the recent verdicts and
// the editable retention bound (the stored value, or the built-in default when none
// has been saved). notice surfaces a confirmation on the retention panel after a
// save; CSRF is included so that panel's htmx form can post.
func (s *Server) spamHistoryPageData(r *http.Request, notice panelNotice) map[string]any {
	verdicts, err := s.dir.RecentSpamVerdicts(200)
	views := make([]spamVerdictView, 0, len(verdicts))
	for _, v := range verdicts {
		views = append(views, spamVerdictView{
			Time: time.Unix(v.Time, 0).Format("2006-01-02 15:04:05"), MailFrom: v.MailFrom,
			RemoteAddr: v.RemoteAddr, Score: v.Score, Spam: v.Spam, Reasons: v.Reasons,
		})
	}
	failed := readFailures{}
	data := map[string]any{
		"Nav": "spamhistory", "Verdicts": views, "VerdictsError": s.listFailure("what.spamHistory", err),
		"Notice": notice, "CSRF": csrfCookieValue(r), "ReadFailed": failed,
	}
	s.fillSpamRetention(data, failed)
	return data
}

// fillSpamRetention sets the spam-history retention bound on a page-data map: the
// stored value, or the built-in default when none has been saved. A failed read is
// recorded in failed rather than shown as the default. The Spam History and Settings
// pages share it.
func (s *Server) fillSpamRetention(data map[string]any, failed readFailures) {
	st, found, err := s.dir.GetSpamHistorySettings()
	if !s.noteRead(failed, "spam-retention", "what.spamRetention", err) {
		return
	}
	retain := defaultSpamHistoryRetainDisplay
	if found {
		retain = st.Retain
	}
	data["Retain"] = retain
}

// handleUISaveSpamRetention persists the spam-history retention bound (how many of
// the most recent scored verdicts to keep). The MTA applies the change within about
// a minute, no restart. A value below 1 is rejected so the table is never pruned to
// nothing.
func (s *Server) handleUISaveSpamRetention(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	retain := formInt(r, "retain")
	if retain < 1 {
		s.render(w, r, "retention-panel", s.spamHistoryPageData(r, errorNotice("spamHistory.retentionInvalid")))
		return
	}
	if err := s.dir.SetSpamHistorySettings(directory.SpamHistorySettings{Retain: retain}); err != nil {
		s.render(w, r, "retention-panel", s.spamHistoryPageData(r, s.failNotice("spamHistory.retentionFailed", err)))
		return
	}
	s.render(w, r, "retention-panel", s.spamHistoryPageData(r, okNotice("spamHistory.retentionSaved")))
}
