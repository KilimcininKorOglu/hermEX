package admin

import (
	"net/http"
	"time"
)

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
	verdicts, err := s.dir.RecentSpamVerdicts(200)
	errMsg := ""
	if err != nil {
		errMsg = "Could not read the spam history: " + err.Error()
	}
	views := make([]spamVerdictView, 0, len(verdicts))
	for _, v := range verdicts {
		views = append(views, spamVerdictView{
			Time: time.Unix(v.Time, 0).Format("2006-01-02 15:04:05"), MailFrom: v.MailFrom,
			RemoteAddr: v.RemoteAddr, Score: v.Score, Spam: v.Spam, Reasons: v.Reasons,
		})
	}
	s.render(w, "spam-history.html", map[string]any{"Nav": "spamhistory", "Verdicts": views, "Error": errMsg})
}
