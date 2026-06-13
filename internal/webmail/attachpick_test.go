package webmail

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// seedInbox appends a raw message to the mailbox INBOX and returns its UID, so a
// test can reference it through the attach-item picker.
func seedInbox(t *testing.T, path, raw string) uint32 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}

// collectParts returns every part of the tree for which match returns true.
func collectParts(p *mime.Part, match func(*mime.Part) bool) []*mime.Part {
	var out []*mime.Part
	var walk func(*mime.Part)
	walk = func(n *mime.Part) {
		if match(n) {
			out = append(out, n)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(p)
	return out
}

// TestAttachPickListsMessages checks the picker fragment: choosing a folder
// lists its messages as attach checkboxes carrying the "uid:folder" value the
// submit path reads, while an unchosen folder renders only the empty hint.
func TestAttachPickListsMessages(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedInbox(t, path, "From: Bob <bob@example.com>\r\nSubject: pick me\r\n\r\nhello")
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, page := get(t, c, ts.URL+"/attachpick?pickfolder=INBOX")
	if wantVal := `value="` + itoa(uid) + `:INBOX"`; !strings.Contains(page, wantVal) {
		t.Errorf("picker did not offer the seeded message (%s):\n%s", wantVal, page)
	}
	if !strings.Contains(page, "pick me") {
		t.Errorf("picker did not show the message subject:\n%s", page)
	}
	if !strings.Contains(page, `name="attachmsg"`) {
		t.Errorf("picker checkbox missing the attachmsg name:\n%s", page)
	}

	// No folder chosen: the empty hint, never a checkbox.
	if _, empty := get(t, c, ts.URL+"/attachpick"); strings.Contains(empty, `name="attachmsg"`) {
		t.Errorf("no-folder picker should be empty:\n%s", empty)
	}
}

// TestComposeAttachItem checks that messages selected in the attach-item picker
// ride along as message/rfc822 attachments: a send carrying two picked messages
// produces a multipart/mixed Sent copy with two message/rfc822 parts, each
// re-synthesized from the picked message (subject + body present).
func TestComposeAttachItem(t *testing.T) {
	path := emptyMailbox(t)
	u1 := seedInbox(t, path, "From: a@example.com\r\nSubject: first picked\r\n\r\nbody one")
	u2 := seedInbox(t, path, "From: b@example.com\r\nSubject: second picked\r\n\r\nbody two")
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := postForm(t, c, ts.URL+"/compose", url.Values{
		"action":    {"send"},
		"to":        {"alice@hermex.test"},
		"subject":   {"carrying items"},
		"body":      {"see attached messages"},
		"attachmsg": {itoa(u1) + ":INBOX", itoa(u2) + ":INBOX"},
	}); code != 200 {
		t.Fatalf("send with attach-items = %d", code)
	}

	raw := folderRaw(t, path, "Sent")
	root := mime.ParseStructure([]byte(raw))
	if root.Type != "multipart" || root.Subtype != "mixed" {
		t.Fatalf("Sent copy is %s/%s, want multipart/mixed:\n%s", root.Type, root.Subtype, raw)
	}
	embeds := collectParts(root, func(p *mime.Part) bool { return p.Type == "message" && p.Subtype == "rfc822" })
	if len(embeds) != 2 {
		t.Fatalf("want 2 message/rfc822 attachments, got %d:\n%s", len(embeds), raw)
	}
	var joined string
	for _, e := range embeds {
		ec, _ := e.DecodedContent()
		joined += string(ec)
	}
	for _, want := range []string{"first picked", "body one", "second picked", "body two"} {
		if !strings.Contains(joined, want) {
			t.Errorf("embedded messages missing %q:\n%s", want, joined)
		}
	}
}

// TestAttachItemDraftRoundTripNoDuplicate guards the attach-item × draft
// interaction. A pick is carried as a checked form field, not embedded into a
// saved draft, so repeated autosaves (each re-submitting the still-checked pick)
// must not grow the draft, and the final send must carry exactly one copy — never
// the accumulating stack a draft-embedded pick would produce. The picked message
// is multipart on purpose: its re-synthesized bytes differ by boundary on every
// read, so a content-equality dedupe could not catch the duplication — only
// keeping picks out of the draft does.
func TestAttachItemDraftRoundTripNoDuplicate(t *testing.T) {
	path := emptyMailbox(t)
	mp := "From: a@example.com\r\n" +
		"Subject: multipart pick\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b0\"\r\n\r\n" +
		"--b0\r\nContent-Type: text/plain\r\n\r\nthe body\r\n" +
		"--b0\r\nContent-Type: text/plain\r\n" +
		"Content-Disposition: attachment; filename=\"att.txt\"\r\n\r\nattached payload\r\n" +
		"--b0--\r\n"
	u1 := seedInbox(t, path, mp)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	rfc822 := func(p *mime.Part) bool { return p.Type == "message" && p.Subtype == "rfc822" }
	embedsIn := func(raw string) int { return len(collectParts(mime.ParseStructure([]byte(raw)), rfc822)) }

	// Savedraft #1: a fresh draft while the pick is checked.
	postForm(t, c, ts.URL+"/compose", url.Values{
		"action": {"savedraft"}, "subject": {"draft with item"}, "body": {"b"},
		"attachmsg": {itoa(u1) + ":INBOX"},
	})
	d := folderMsgs(t, path, draftFID)
	if len(d) != 1 {
		t.Fatalf("after savedraft #1 Drafts has %d, want 1", len(d))
	}
	c1 := embedsIn(msgRaw(t, path, draftFID, d[0].UID))
	if c1 > 1 {
		t.Fatalf("a single pick embedded %d copies into the draft", c1)
	}

	// Savedraft #2 (autosave): the pick is still checked and the draft is carried,
	// so the re-read and the still-checked field both reference the same message.
	postForm(t, c, ts.URL+"/compose", url.Values{
		"action": {"savedraft"}, "draftuid": {itoa(d[0].UID)}, "draftfolder": {"Drafts"},
		"subject": {"draft with item"}, "body": {"b edited"},
		"attachmsg": {itoa(u1) + ":INBOX"},
	})
	d = folderMsgs(t, path, draftFID)
	if len(d) != 1 {
		t.Fatalf("after savedraft #2 Drafts has %d, want 1", len(d))
	}
	if c2 := embedsIn(msgRaw(t, path, draftFID, d[0].UID)); c2 != c1 {
		t.Fatalf("autosave grew the draft's embedded messages from %d to %d (accumulation bug)", c1, c2)
	}

	// Send the reopened draft with the pick still checked: exactly one copy.
	postForm(t, c, ts.URL+"/compose", url.Values{
		"action": {"send"}, "draftuid": {itoa(d[0].UID)}, "draftfolder": {"Drafts"},
		"to": {"alice@hermex.test"}, "subject": {"draft with item"}, "body": {"final"},
		"attachmsg": {itoa(u1) + ":INBOX"},
	})
	if n := embedsIn(folderRaw(t, path, "Sent")); n != 1 {
		t.Fatalf("Sent carries %d embedded copies of the pick, want exactly 1 (double-count bug)", n)
	}
}

// TestAttachItemRidesScheduledSend checks that an attach-item pick is carried by a
// scheduled send, not just an immediate one: the message filed in the Outbox for
// later release embeds the picked message as message/rfc822.
func TestAttachItemRidesScheduledSend(t *testing.T) {
	path := emptyMailbox(t)
	u1 := seedInbox(t, path, "From: a@example.com\r\nSubject: scheduled item\r\n\r\nbody")
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	future := time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")
	postForm(t, c, ts.URL+"/compose", url.Values{
		"action": {"sendlater"}, "sendat": {future},
		"to": {"alice@hermex.test"}, "subject": {"later with item"}, "body": {"b"},
		"attachmsg": {itoa(u1) + ":INBOX"},
	})

	outbox := folderMsgs(t, path, int64(mapi.PrivateFIDOutbox))
	if len(outbox) != 1 {
		t.Fatalf("Outbox has %d, want 1", len(outbox))
	}
	raw := msgRaw(t, path, int64(mapi.PrivateFIDOutbox), outbox[0].UID)
	embeds := collectParts(mime.ParseStructure([]byte(raw)), func(p *mime.Part) bool {
		return p.Type == "message" && p.Subtype == "rfc822"
	})
	if len(embeds) != 1 {
		t.Fatalf("scheduled message carries %d embedded picks, want 1:\n%s", len(embeds), raw)
	}
	if ec, _ := embeds[0].DecodedContent(); !strings.Contains(string(ec), "scheduled item") {
		t.Errorf("scheduled embed lost the picked message:\n%s", ec)
	}
}
