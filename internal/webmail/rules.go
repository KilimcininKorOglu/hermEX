package webmail

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// rulesFolder is the folder whose rules the editor manages. Rules run on the
// inbox, matching where delivery applies them.
const rulesFolder = int64(mapi.PrivateFIDInbox)

// rulesPage is the data for the rules editor page.
type rulesPage struct {
	User      string
	Rules     []ruleView
	Folders   []folderView // move-action target choices
	Err       string       // a problem with the last submission, shown as a notice
	Ran       bool         // a run-now just completed; show the result
	Evaluated int
	Affected  int
}

// ruleView is one rule row, with a human-readable summary of its condition and
// actions.
type ruleView struct {
	ID      int64
	Name    string
	Enabled bool
	Summary string
}

// handleRulesForm redirects the former standalone rules page to its tab on the
// unified settings page (the inbox-rules editor now lives there); the POST
// endpoint below still serves the forms.
func (s *Server) handleRulesForm(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings?tab=rules", http.StatusSeeOther)
}

// buildRulesPage loads the inbox-rules section: the stored rules with summaries
// and the folder list for a move action's target. Best-effort — a read failure
// yields an empty section rather than failing the whole settings page.
func (s *Server) buildRulesPage(st *objectstore.Store, sess *session) rulesPage {
	page := rulesPage{User: sess.user}
	folders, err := st.ListFolders()
	if err != nil {
		return page
	}
	page.Folders = buildFolderViews(folders)
	names := folderNamesByID(page.Folders)

	rules, err := st.ListRules(rulesFolder)
	if err != nil {
		return page
	}
	for _, ru := range rules {
		page.Rules = append(page.Rules, ruleView{
			ID:      ru.ID,
			Name:    ru.Name,
			Enabled: ru.Enabled(),
			Summary: describeRule(ru, names),
		})
	}
	return page
}

// handleRulesSubmit applies one rules action — add, delete, enable/disable, or
// run-now — then redirects back to the editor (post/redirect/get).
func (s *Server) handleRulesSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()

	switch r.FormValue("action") {
	case "add":
		cond, ok := buildCondition(r)
		if !ok {
			http.Redirect(w, r, "/settings?tab=rules&err=condition", http.StatusSeeOther)
			return
		}
		act, ok := buildAction(r)
		if !ok {
			http.Redirect(w, r, "/settings?tab=rules&err=action", http.StatusSeeOther)
			return
		}
		if _, err := st.AddRule(objectstore.Rule{
			FolderID:  rulesFolder,
			Name:      strings.TrimSpace(r.FormValue("name")),
			State:     mapi.RuleStateEnabled,
			Condition: cond,
			Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{act}},
		}); err != nil {
			http.Redirect(w, r, "/settings?tab=rules&err=save", http.StatusSeeOther)
			return
		}
	case "delete":
		if id, err := strconv.ParseInt(r.FormValue("id"), 10, 64); err == nil {
			st.DeleteRule(id)
		}
	case "toggle":
		if id, err := strconv.ParseInt(r.FormValue("id"), 10, 64); err == nil {
			st.SetRuleEnabled(id, r.FormValue("enabled") == "1")
		}
	case "run":
		res, err := st.RunRules(rulesFolder)
		if err != nil {
			http.Redirect(w, r, "/settings?tab=rules&err=run", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/settings?tab=rules&ran=1&affected=%d&evaluated=%d", res.Affected, res.Evaluated), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings?tab=rules", http.StatusSeeOther)
}

// buildCondition assembles a RESTRICTION from the add-rule form's condition
// fields, reporting ok=false when the chosen field has no usable value.
func buildCondition(r *http.Request) (mapi.Restriction, bool) {
	switch r.FormValue("condfield") {
	case "subject":
		if v := strings.TrimSpace(r.FormValue("condvalue")); v != "" {
			return objectstore.RuleSubjectContains(v), true
		}
	case "from":
		if v := strings.TrimSpace(r.FormValue("condvalue")); v != "" {
			return objectstore.RuleFromContains(v), true
		}
	case "body":
		if v := strings.TrimSpace(r.FormValue("condvalue")); v != "" {
			return objectstore.RuleBodyContains(v), true
		}
	case "importance":
		return objectstore.RuleImportanceIs(importanceFromForm(r.FormValue("condimportance"))), true
	case "sensitivity":
		return objectstore.RuleSensitivityIs(sensitivityFromForm(r.FormValue("condsensitivity"))), true
	case "size":
		if kb, err := strconv.Atoi(strings.TrimSpace(r.FormValue("condsize"))); err == nil && kb >= 0 {
			return objectstore.RuleSizeAtLeast(kb * 1024), true
		}
	}
	return mapi.Restriction{}, false
}

// buildAction assembles a single rule action from the add-rule form's action
// fields, reporting ok=false on an unrecognized or incomplete action.
func buildAction(r *http.Request) (mapi.ActionBlock, bool) {
	switch r.FormValue("actiontype") {
	case "markread":
		return objectstore.RuleMarkReadAction(), true
	case "delete":
		return objectstore.RuleDeleteAction(), true
	case "move":
		if id, err := strconv.ParseInt(r.FormValue("actiontarget"), 10, 64); err == nil {
			return objectstore.RuleMoveAction(id), true
		}
	case "copy":
		if id, err := strconv.ParseInt(r.FormValue("actiontarget"), 10, 64); err == nil {
			return objectstore.RuleCopyAction(id), true
		}
	}
	return mapi.ActionBlock{}, false
}

// importanceFromForm maps the importance select value to a PR_IMPORTANCE level.
func importanceFromForm(v string) int {
	switch v {
	case "high":
		return mapi.ImportanceHigh
	case "low":
		return mapi.ImportanceLow
	default:
		return mapi.ImportanceNormal
	}
}

// sensitivityFromForm maps the sensitivity select value to a PR_SENSITIVITY level.
func sensitivityFromForm(v string) int {
	switch v {
	case "personal":
		return mapi.SensitivityPersonal
	case "private":
		return mapi.SensitivityPrivate
	case "confidential":
		return mapi.SensitivityConfidential
	default:
		return mapi.SensitivityNone
	}
}

// sensitivityName labels a PR_SENSITIVITY level for a rule summary.
func sensitivityName(level int) string {
	switch level {
	case mapi.SensitivityPersonal:
		return "personal"
	case mapi.SensitivityPrivate:
		return "private"
	case mapi.SensitivityConfidential:
		return "confidential"
	default:
		return "normal"
	}
}

// errNotice maps an err query token to a human-readable notice, or "" for none.
func errNotice(code string) string {
	switch code {
	case "condition":
		return "Could not add the rule: choose a condition and fill in its value."
	case "action":
		return "Could not add the rule: choose an action (and a target folder for a move)."
	case "save":
		return "Could not save the rule."
	case "run":
		return "Could not apply rules."
	default:
		return ""
	}
}

// folderNamesByID maps each folder id to its full display path, for naming a
// move action's target in a rule summary.
func folderNamesByID(fv []folderView) map[int64]string {
	m := make(map[int64]string, len(fv))
	for _, f := range fv {
		m[f.ID] = f.Path
	}
	return m
}

// describeRule renders a rule as a sentence: "If <condition>, <actions>." It
// recognizes the curated condition and action vocabulary the editor produces and
// falls back to a neutral placeholder for anything else (e.g. a rule authored by
// another client), so an unrecognized rule still lists without misdescribing it.
func describeRule(ru objectstore.Rule, folderNames map[int64]string) string {
	return fmt.Sprintf("If %s, %s.", describeCondition(ru.Condition), describeActions(ru.Actions, folderNames))
}

func describeCondition(r mapi.Restriction) string {
	switch r.Type {
	case mapi.ResAnd:
		kids, _ := r.Value.([]mapi.Restriction)
		parts := make([]string, 0, len(kids))
		for _, k := range kids {
			parts = append(parts, describeCondition(k))
		}
		return strings.Join(parts, " and ")
	case mapi.ResContent:
		c, ok := r.Value.(mapi.ContentRestriction)
		if !ok {
			return "(custom condition)"
		}
		val, _ := c.PropVal.Value.(string)
		switch c.PropTag {
		case mapi.PrSubject:
			return fmt.Sprintf("the subject contains %q", val)
		case mapi.PrSenderSmtpAddress:
			return fmt.Sprintf("the sender contains %q", val)
		case mapi.PrBody:
			return fmt.Sprintf("the body contains %q", val)
		}
	case mapi.ResProperty:
		pr, ok := r.Value.(mapi.PropertyRestriction)
		if !ok {
			return "(custom condition)"
		}
		switch pr.PropTag {
		case mapi.PrImportance:
			n, _ := pr.PropVal.Value.(int32)
			return "the importance is " + importanceName(int(n))
		case mapi.PrSensitivity:
			n, _ := pr.PropVal.Value.(int32)
			return "the sensitivity is " + sensitivityName(int(n))
		case mapi.PrMessageSize:
			n, _ := pr.PropVal.Value.(int32)
			return fmt.Sprintf("the size is at least %d KB", n/1024)
		}
	}
	return "(custom condition)"
}

func describeActions(a mapi.RuleActions, folderNames map[int64]string) string {
	parts := make([]string, 0, len(a.Blocks))
	for _, b := range a.Blocks {
		switch b.Type {
		case mapi.OpMarkAsRead:
			parts = append(parts, "mark it as read")
		case mapi.OpDelete:
			parts = append(parts, "delete it")
		case mapi.OpMove:
			parts = append(parts, "move it to "+moveTargetName(b, folderNames))
		case mapi.OpCopy:
			parts = append(parts, "copy it to "+moveTargetName(b, folderNames))
		default:
			parts = append(parts, "(custom action)")
		}
	}
	if len(parts) == 0 {
		return "(no action)"
	}
	return strings.Join(parts, " and ")
}

// moveTargetName resolves a move action's destination folder name for a summary.
func moveTargetName(b mapi.ActionBlock, folderNames map[int64]string) string {
	mc, ok := b.Data.(mapi.MoveCopyAction)
	if !ok {
		return "another folder"
	}
	svr, ok := mc.FolderEID.(mapi.SVREID)
	if !ok {
		return "another folder"
	}
	if name := folderNames[int64(svr.FolderID)]; name != "" {
		return name
	}
	return "another folder"
}

// importanceName labels a PR_IMPORTANCE level for a rule summary.
func importanceName(level int) string {
	switch level {
	case mapi.ImportanceHigh:
		return "high"
	case mapi.ImportanceLow:
		return "low"
	default:
		return "normal"
	}
}
