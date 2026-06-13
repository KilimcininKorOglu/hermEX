package webmail

import (
	"html/template"
	"net/url"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// seedMsg appends a simple text message (From/optional To/Subject/body) to a
// folder by fixed id and returns its UID. whenUnix sets the internal date (for
// ordering assertions); flags sets the index flag bits (e.g. a draft).
func seedMsg(t *testing.T, path string, fid int64, subject, to, body string, whenUnix, flags int64) uint32 {
	t.Helper()
	var b strings.Builder
	b.WriteString("From: sender@example.com\r\n")
	if to != "" {
		b.WriteString("To: " + to + "\r\n")
	}
	b.WriteString("Subject: " + subject + "\r\n\r\n")
	b.WriteString(body)
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(fid, []byte(b.String()), time.Unix(whenUnix, 0), flags)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}

// searchURL builds a /search request URL with the given parameters.
func searchURL(base, q, field, scope, folder string) string {
	return base + "/search?" + url.Values{
		"q": {q}, "field": {field}, "scope": {scope}, "scopefolder": {folder},
	}.Encode()
}

// --- Unit: matchMessage (the cheap-first per-term AND matcher) ---

// TestMatchMessageSubjectScopeNeverFetches checks that the Subject scope matches
// from the index alone and never reads the body, whether it hits or misses.
func TestMatchMessageSubjectScopeNeverFetches(t *testing.T) {
	fetched := false
	fetch := func() (string, bool) { fetched = true; return "", true }

	if m, e := matchMessage([]string{"hello"}, "Hello World", "", "subject", fetch); !m || e || fetched {
		t.Fatalf("subject hit: matched=%v err=%v fetched=%v, want true,false,false", m, e, fetched)
	}
	fetched = false
	if m, e := matchMessage([]string{"invoice"}, "Hello World", "", "subject", fetch); m || e || fetched {
		t.Fatalf("subject miss must not fetch: matched=%v err=%v fetched=%v", m, e, fetched)
	}
}

// TestMatchMessageAllScopeBodyOnly checks an "all" search matching only via the
// expensive text (not subject/sender).
func TestMatchMessageAllScopeBodyOnly(t *testing.T) {
	fetch := func() (string, bool) { return "the invoice is attached", true }
	if m, e := matchMessage([]string{"invoice"}, "Hello", "", "all", fetch); !m || e {
		t.Fatalf("body-only all-scope: matched=%v err=%v, want true,false", m, e)
	}
}

// TestMatchMessageMultiTermSplit is the trap case: one term matches a cheap field
// and another only the expensive text; AND across the split must still match, and
// a missing term must not.
func TestMatchMessageMultiTermSplit(t *testing.T) {
	fetchHas := func() (string, bool) { return "please see the invoice", true }
	if m, _ := matchMessage([]string{"hello", "invoice"}, "Hello World", "", "all", fetchHas); !m {
		t.Errorf("multi-term split across cheap+expensive should match")
	}
	fetchLacks := func() (string, bool) { return "please see the attachment", true }
	if m, _ := matchMessage([]string{"hello", "invoice"}, "Hello World", "", "all", fetchLacks); m {
		t.Errorf("AND must exclude a message missing one term")
	}
}

// TestMatchMessageCaseInsensitive checks case-folding of the cheap fields.
func TestMatchMessageCaseInsensitive(t *testing.T) {
	fetch := func() (string, bool) { return "", true }
	if m, _ := matchMessage([]string{"hello"}, "HELLO WORLD", "", "subject", fetch); !m {
		t.Errorf("match must be case-insensitive")
	}
}

// TestMatchMessageFailSoft checks that an expensive-read failure on an
// otherwise-unmatched message reports fetchErrored (so the folder is flagged
// incomplete), not a false match.
func TestMatchMessageFailSoft(t *testing.T) {
	fetch := func() (string, bool) { return "", false }
	if m, e := matchMessage([]string{"invoice"}, "Hello", "", "all", fetch); m || !e {
		t.Fatalf("fail-soft: matched=%v fetchErrored=%v, want false,true", m, e)
	}
}

// --- Integration: the /search endpoint ---

// TestSearchInFolderSubject checks a Subject-scope in-folder search returns the
// matching message and excludes non-matches.
func TestSearchInFolderSubject(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Quarterly report", "", "body one", 100, 0)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Lunch plans", "", "body two", 200, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	code, page := get(t, c, searchURL(ts.URL, "report", "subject", "folder", "INBOX"))
	if code != 200 {
		t.Fatalf("search = %d", code)
	}
	if !strings.Contains(page, "Quarterly report") {
		t.Errorf("subject match missing:\n%s", page)
	}
	if strings.Contains(page, "Lunch plans") {
		t.Errorf("non-matching subject must not appear:\n%s", page)
	}
}

// TestSearchAllFieldsBodyOnly checks the body is searched only in the "all"
// scope, not in "subject".
func TestSearchAllFieldsBodyOnly(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Hello", "", "the secret pineapple is here", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if _, page := get(t, c, searchURL(ts.URL, "pineapple", "all", "folder", "INBOX")); !strings.Contains(page, "Hello") {
		t.Errorf("body term not found with field=all:\n%s", page)
	}
	if _, page := get(t, c, searchURL(ts.URL, "pineapple", "subject", "folder", "INBOX")); !strings.Contains(page, "No messages match") {
		t.Errorf("subject scope must not match a body-only term:\n%s", page)
	}
}

// TestSearchAllFieldsRecipient checks a To-header term matches in the "all" scope.
func TestSearchAllFieldsRecipient(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Greetings", "bob@example.com", "hi", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if _, page := get(t, c, searchURL(ts.URL, "bob", "all", "folder", "INBOX")); !strings.Contains(page, "Greetings") {
		t.Errorf("recipient (To) term not found with field=all:\n%s", page)
	}
}

// TestSearchCrossFolderMailOnly checks an "all folders" search spans mail folders
// (Inbox + Sent), excludes PIM folders (Notes), and orders newest-first.
func TestSearchCrossFolderMailOnly(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "alpha inbox widget", "", "x", 100, 0)
	seedMsg(t, path, int64(mapi.PrivateFIDSentItems), "alpha sent widget", "", "x", 200, 0)
	seedMsg(t, path, int64(mapi.PrivateFIDNotes), "alpha note widget", "", "x", 300, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, page := get(t, c, searchURL(ts.URL, "widget", "subject", "all", ""))
	if !strings.Contains(page, "alpha inbox widget") || !strings.Contains(page, "alpha sent widget") {
		t.Errorf("cross-folder search missed an Inbox or Sent hit:\n%s", page)
	}
	if strings.Contains(page, "alpha note widget") {
		t.Errorf("a PIM (Notes) folder must be excluded from cross-folder mail search:\n%s", page)
	}
	if strings.Index(page, "alpha sent widget") > strings.Index(page, "alpha inbox widget") {
		t.Errorf("results not newest-first (Sent@200 should precede Inbox@100):\n%s", page)
	}
}

// TestSearchMultiTermAND checks all whitespace terms must be present.
func TestSearchMultiTermAND(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "project alpha kickoff", "", "x", 100, 0)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "project beta status", "", "x", 200, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, page := get(t, c, searchURL(ts.URL, "project alpha", "subject", "folder", "INBOX"))
	if !strings.Contains(page, "project alpha kickoff") {
		t.Errorf("multi-term should match the message with both terms:\n%s", page)
	}
	if strings.Contains(page, "project beta status") {
		t.Errorf("multi-term AND must exclude a message missing a term:\n%s", page)
	}
}

// TestSearchEmptyQueryNoScan checks an empty query shows the prompt and lists
// nothing (no folder scan).
func TestSearchEmptyQueryNoScan(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "anything here", "", "x", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, page := get(t, c, searchURL(ts.URL, "", "all", "all", ""))
	if !strings.Contains(page, "Enter a search term") {
		t.Errorf("empty query should show the prompt:\n%s", page)
	}
	if strings.Contains(page, "anything here") {
		t.Errorf("empty query must not list any message:\n%s", page)
	}
}

// TestSearchDraftLinksToCompose checks a draft hit links to the compose editor,
// not the reader (reuses messageViewFrom's Draft flag).
func TestSearchDraftLinksToCompose(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDDraft), "draft widget", "", "x", 100, objectstore.FlagSeen|objectstore.FlagDraft)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, page := get(t, c, searchURL(ts.URL, "widget", "subject", "folder", "Drafts"))
	if !strings.Contains(page, "action=editdraft") {
		t.Errorf("a draft hit must link to the compose editor:\n%s", page)
	}
}

// TestSearchEscapesQuery checks the echoed query is HTML-escaped (no reflected
// markup).
func TestSearchEscapesQuery(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, page := get(t, c, searchURL(ts.URL, "<script>alert(1)</script>", "subject", "folder", "INBOX"))
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Errorf("query must be HTML-escaped, not reflected raw:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Errorf("escaped query expected in the page:\n%s", page)
	}
}

// TestMailPageHasSearchForm checks the mailbox toolbar exposes the search form.
func TestMailPageHasSearchForm(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if _, page := get(t, c, ts.URL+"/mail?folder=INBOX"); !strings.Contains(page, `action="/search"`) {
		t.Errorf("mail page must include the search form:\n%s", page)
	}
}

// TestSearchTruncatedNotice checks the search view renders the incomplete-search
// notice when folders failed to scan (forcing a real mid-search read error is
// impractical — the store re-synthesizes a missing eml — so the fail-soft path is
// proven at the unit level and the notice rendering here).
func TestSearchTruncatedNotice(t *testing.T) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	v := searchView{Searched: true, Query: "x", Truncated: []string{"Archive/2026"}}
	if err := tmpl.ExecuteTemplate(&b, "search", v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "could not be fully searched") || !strings.Contains(b.String(), "Archive/2026") {
		t.Errorf("truncated notice missing:\n%s", b.String())
	}
}
