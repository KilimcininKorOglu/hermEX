package activesync

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

const bobUser = "bob@hermex.test"

// sendServer starts a server with two seeded mailboxes (alice the sender, bob a
// local recipient) so SendMail can deliver locally. It authorizes alice.
func sendServer(t *testing.T) (ts *httptest.Server, aliceDir, bobDir string) {
	t.Helper()
	aliceDir = filepath.Join(t.TempDir(), "alice")
	bobDir = filepath.Join(t.TempDir(), "bob")
	for _, d := range []string{aliceDir, bobDir} {
		st, err := objectstore.Open(d)
		if err != nil {
			t.Fatal(err)
		}
		closeStore(t, st)
	}
	accs := directory.StaticAccounts{
		testUser: {Password: testPass, MailboxPath: aliceDir},
		bobUser:  {Password: "bobpass", MailboxPath: bobDir},
	}
	ts = httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, aliceDir, bobDir
}

// postRaw POSTs a command body and returns the raw (undecoded) response, for
// commands like SendMail whose success reply is an empty body.
func postRaw(t *testing.T, ts *httptest.Server, cmd string, body []byte) (*http.Response, []byte) {
	t.Helper()
	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=" + cmd + "&User=" + testUser + "&DeviceId=dev1&DeviceType=iPhone"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(resp.Body)
	mustNoErr(t, "read the reply", err)
	mustNoErr(t, "close the reply", resp.Body.Close())
	return resp, out
}

// closeStore closes a mailbox store the test opened and fails the test when it
// cannot, since an unclosed store keeps the mailbox marked in use.
func closeStore(t *testing.T, st *objectstore.Store) {
	t.Helper()
	mustNoErr(t, "close the store", st.Close())
}

func folderCount(t *testing.T, dir string, fid int64) int {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, st)
	msgs, err := st.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

const sampleMIME = "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: Hi Bob\r\n" +
	"Date: Mon, 15 Jun 2026 09:00:00 +0000\r\nMessage-ID: <s1@hermex.test>\r\n\r\nHello Bob\r\n"

// TestSendMailDelivers confirms a ComposeMail SendMail delivers to the local
// recipient's Inbox and, with SaveInSentItems, saves a copy to the sender's Sent.
func TestSendMailDelivers(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	sm := wbxml.Elem(wbxml.CMSendMail,
		wbxml.Str(wbxml.CMClientID, "c1"),
		wbxml.Empty(wbxml.CMSaveInSentItems),
		wbxml.Opaque(wbxml.CMMIME, []byte(sampleMIME)))

	resp, out := postRaw(t, ts, "SendMail", wbxml.Marshal(sm))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, out)
	}
	if len(out) != 0 {
		t.Errorf("SendMail success should have an empty body, got %d bytes", len(out))
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob's Inbox has %d messages, want 1", n)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 1 {
		t.Errorf("alice's Sent has %d messages, want 1", n)
	}
}

// TestSendMailNoSentCopy confirms a SendMail without SaveInSentItems delivers
// but saves no Sent copy.
func TestSendMailNoSentCopy(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	sm := wbxml.Elem(wbxml.CMSendMail, wbxml.Opaque(wbxml.CMMIME, []byte(sampleMIME)))

	resp, _ := postRaw(t, ts, "SendMail", wbxml.Marshal(sm))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob's Inbox has %d messages, want 1", n)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("alice's Sent has %d messages, want 0 (SaveInSentItems absent)", n)
	}
}

// TestSendMailRejectsForgedFrom confirms SendMail refuses a message whose From
// header names a same-domain mailbox the authenticated caller may not send as,
// and delivers nothing, closing the DKIM-aligned sender-spoofing gap.
func TestSendMailRejectsForgedFrom(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	forged := "From: bob@hermex.test\r\nTo: bob@hermex.test\r\nSubject: spoof\r\n" +
		"Date: Mon, 15 Jun 2026 09:00:00 +0000\r\nMessage-ID: <f1@hermex.test>\r\n\r\nnot really from bob\r\n"
	sm := wbxml.Elem(wbxml.CMSendMail,
		wbxml.Empty(wbxml.CMSaveInSentItems),
		wbxml.Opaque(wbxml.CMMIME, []byte(forged)))

	resp, out := postRaw(t, ts, "SendMail", wbxml.Marshal(sm))
	wantComposeStatus(t, resp, out, wbxml.CMSendMail, "130")
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("bob's Inbox has %d messages, want 0 (delivery must be refused)", n)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("alice's Sent has %d messages, want 0", n)
	}
}

// wantComposeStatus asserts a ComposeMail failure as [MS-ASCMD] 2.2.3.177.14 and
// .16 shape it: HTTP 200, the command's own element as root, and the Status.
func wantComposeStatus(t *testing.T, resp *http.Response, out []byte, root wbxml.Tag, status string) {
	t.Helper()
	node := composeReply(t, resp, out)
	if node.Tag != root {
		t.Errorf("response root = %#x, want %#x", node.Tag, root)
	}
	if got := node.ChildText(wbxml.CMStatus); got != status {
		t.Errorf("Status = %q, want %q", got, status)
	}
}

// composeReply decodes a ComposeMail failure reply, which is HTTP 200 with a body.
func composeReply(t *testing.T, resp *http.Response, out []byte) *wbxml.Node {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status %d, want 200 with a Status body: %s", resp.StatusCode, out)
	}
	node, err := wbxml.Unmarshal(out)
	if err != nil {
		t.Fatalf("response is not WBXML: %v (%q)", err, out)
	}
	return node
}

// composeStatusOf returns the Status of a ComposeMail failure reply.
func composeStatusOf(t *testing.T, resp *http.Response, out []byte) string {
	t.Helper()
	return composeReply(t, resp, out).ChildText(wbxml.CMStatus)
}

// TestComposeMailRefusalsCarryTheirStatus sends requests the [MS-ASCMD] 2.2.2
// table names a status for, and checks each is refused with that status and
// delivers nothing: no recipient (119), no MIME element (103), bytes that are
// not WBXML past the version byte (102).
func TestComposeMailRefusalsCarryTheirStatus(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	noRcpt := "From: alice@hermex.test\r\nSubject: nobody\r\n\r\nbody\r\n"
	cases := []struct {
		name, status string
		body         []byte
	}{
		{"no recipient", "119", wbxml.Marshal(wbxml.Elem(wbxml.CMSendMail, wbxml.Opaque(wbxml.CMMIME, []byte(noRcpt))))},
		{"no MIME", "103", wbxml.Marshal(wbxml.Elem(wbxml.CMSendMail, wbxml.Empty(wbxml.CMSaveInSentItems)))},
		{"broken WBXML", "102", []byte{0x03, 0x01, 0x6a, 0x00, 0xff, 0xff, 0xff}},
	}
	for _, c := range cases {
		resp, out := postRaw(t, ts, "SendMail", c.body)
		t.Run(c.name, func(t *testing.T) { wantComposeStatus(t, resp, out, wbxml.CMSendMail, c.status) })
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("bob's Inbox has %d messages after refused requests", n)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("alice's Sent has %d messages after refused requests", n)
	}
}

// TestSmartReplyRefusalNamesSmartReply checks a refused SmartReply answers with a
// SmartReply element, not a SendMail one (6.44 SmartReply Response Schema).
func TestSmartReplyRefusalNamesSmartReply(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	forged := "From: bob@hermex.test\r\nTo: bob@hermex.test\r\nSubject: spoof\r\n\r\nx\r\n"
	resp, out := postRaw(t, ts, "SmartReply", wbxml.Marshal(wbxml.Elem(wbxml.CMSmartReply,
		wbxml.Empty(wbxml.CMSaveInSentItems), wbxml.Opaque(wbxml.CMMIME, []byte(forged)))))
	wantComposeStatus(t, resp, out, wbxml.CMSmartReply, "130")
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("bob's Inbox has %d messages after a refused SmartReply", n)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("alice's Sent has %d messages after a refused SmartReply", n)
	}
}

// TestSmartResponseToAMissingSourceSendsNothing names reply and forward sources
// that are not a message of the caller's mailbox: [MS-ASCMD] 2.2.2 answers 150
// ItemNotFound, and nothing may be delivered for a reply to nothing.
func TestSmartResponseToAMissingSourceSendsNothing(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	uid, srcID := seedSource(t, aliceDir, 0)
	inbox := strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)
	sent := strconv.FormatInt(int64(mapi.PrivateFIDSentItems), 10)
	item := strconv.FormatUint(uint64(uid), 10)
	src := func(folder, it string) *wbxml.Node {
		return wbxml.Elem(wbxml.CMSource, wbxml.Str(wbxml.CMFolderID, folder), wbxml.Str(wbxml.CMItemID, it))
	}
	cases := map[string]*wbxml.Node{
		"no source":              nil,
		"unknown item":           src(inbox, "9999"),
		"item of another folder": src(sent, item),
		"non-numeric item":       src(inbox, "1;DROP"),
		"negative folder":        src("-1", item),
		"LongId without colon":   wbxml.Elem(wbxml.CMSource, wbxml.Str(wbxml.CMLongId, "nonsense")),
	}
	for name, source := range cases {
		for _, cmd := range []struct {
			name string
			tag  wbxml.Tag
		}{{"SmartReply", wbxml.CMSmartReply}, {"SmartForward", wbxml.CMSmartForward}} {
			kids := []*wbxml.Node{wbxml.Opaque(wbxml.CMMIME, []byte(sampleMIME))}
			if source != nil {
				kids = append(kids, source)
			}
			resp, out := postRaw(t, ts, cmd.name, wbxml.Marshal(wbxml.Elem(cmd.tag, kids...)))
			t.Run(name+"/"+cmd.name, func(t *testing.T) { wantComposeStatus(t, resp, out, cmd.tag, "150") })
		}
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("bob's Inbox has %d messages; a reply to a missing source was sent", n)
	}
	if v, ok := sourceVerb(t, aliceDir, srcID); ok {
		t.Errorf("the real message was marked with verb %d by requests that named another source", v)
	}
}

// TestSmartForwardOfAnAppointment forwards a calendar item by the ServerId a
// calendar Sync hands the device (its object id, not an IMAP uid): the SmartForward
// page names an appointment as a forward source, so it is sent and marked. The
// same id named under another folder is not that folder's item and answers 150.
func TestSmartForwardOfAnAppointment(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	st, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.Appointment")
	props.Set(mapi.PrSubject, "Planning")
	id, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props})
	closeStore(t, st)
	if err != nil {
		t.Fatal(err)
	}
	sid := strconv.FormatInt(id, 10)
	forward := func(folder int64) (*http.Response, []byte) {
		return postRaw(t, ts, "SmartForward", wbxml.Marshal(wbxml.Elem(wbxml.CMSmartForward,
			wbxml.Elem(wbxml.CMSource,
				wbxml.Str(wbxml.CMFolderID, strconv.FormatInt(folder, 10)),
				wbxml.Str(wbxml.CMItemID, sid)),
			wbxml.Opaque(wbxml.CMMIME, []byte(sampleMIME)))))
	}

	resp, out := forward(mapi.PrivateFIDContacts)
	wantComposeStatus(t, resp, out, wbxml.CMSmartForward, "150")

	resp, out = forward(mapi.PrivateFIDCalendar)
	if resp.StatusCode != http.StatusOK || len(out) != 0 {
		t.Fatalf("forward of an appointment: HTTP %d, body %q; want an empty 200", resp.StatusCode, out)
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob's Inbox has %d messages, want the one forward", n)
	}
	if v, ok := sourceVerb(t, aliceDir, id); !ok || v != mapi.NoteVerbForward {
		t.Errorf("appointment PidTagLastVerbExecuted = %d (present=%v), want %d", v, ok, mapi.NoteVerbForward)
	}
	// 2.2.1.19: a forwarded appointment rides as the original message attached.
	got := firstRaw(t, bobDir, int64(mapi.PrivateFIDInbox))
	for _, want := range []string{"message/rfc822", "Planning.eml", "Subject: Planning"} {
		if !strings.Contains(got, want) {
			t.Errorf("forward of an appointment lacks %q:\n%s", want, got)
		}
	}
}

// TestSendMailLegacyRawMIME confirms the pre-14 path (a raw-MIME body, no WBXML
// envelope) also delivers.
func TestSendMailLegacyRawMIME(t *testing.T) {
	ts, _, bobDir := sendServer(t)
	resp, _ := postRaw(t, ts, "SendMail", []byte(sampleMIME))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob's Inbox has %d messages, want 1", n)
	}
}

// TestStripBcc removes the Bcc field in each form RFC 5322 allows and a device
// may send: folded onto a continuation line (2.2.3), with whitespace before the
// colon (the obsolete syntax of 4.5.3), and in a message with no body, which has
// no empty line after the header (3.5), with CRLF and with the bare LF some
// devices write. A field whose name only starts with Bcc stays, and a body line
// that reads like a Bcc field is body, not header, so it stays too.
func TestStripBcc(t *testing.T) {
	cases := []struct{ name, raw, want string }{
		{"folded",
			"To: bob@hermex.test\r\nBcc: carol@hermex.test,\r\n dan@hermex.test\r\nSubject: x\r\n\r\nbody\r\nBcc: body line\r\n",
			"To: bob@hermex.test\r\nSubject: x\r\n\r\nbody\r\nBcc: body line\r\n"},
		{"space before colon",
			"To: bob@hermex.test\r\nBCC : erin@hermex.test\r\nBcc-Note: kept\r\nSubject: x\r\n\r\nbody\r\n",
			"To: bob@hermex.test\r\nBcc-Note: kept\r\nSubject: x\r\n\r\nbody\r\n"},
		{"no body, CRLF",
			"To: bob@hermex.test\r\nBcc: hidden@hermex.test\r\nSubject: x\r\n",
			"To: bob@hermex.test\r\nSubject: x\r\n"},
		{"no body, LF",
			"To: bob@hermex.test\nBcc: hidden@hermex.test\nSubject: x\n",
			"To: bob@hermex.test\nSubject: x\n"},
	}
	for _, c := range cases {
		if got := string(stripBcc([]byte(c.raw))); got != c.want {
			t.Errorf("%s: stripBcc =\n%q\nwant\n%q", c.name, got, c.want)
		}
	}
}

// TestRecipientsOf confirms To, Cc, and Bcc addresses are all collected.
func TestRecipientsOf(t *testing.T) {
	raw := []byte("To: bob@hermex.test\r\nCc: carol@hermex.test\r\nBcc: dan@hermex.test\r\nSubject: x\r\n\r\nbody")
	got := recipientsOf(raw)
	want := map[string]bool{"bob@hermex.test": true, "carol@hermex.test": true, "dan@hermex.test": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want 3 recipients", got)
	}
	for _, a := range got {
		if !want[a] {
			t.Errorf("unexpected recipient %q", a)
		}
	}
}

// seedSource appends one message to a mailbox's Inbox with the given flags and
// returns its UID and object id, so a reply/forward test can reference it as the
// source and read its markers back.
func seedSource(t *testing.T, dir string, flags int64) (uid uint32, id int64) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, st)
	raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Original\r\n" +
		"Message-ID: <orig@hermex.test>\r\n\r\nhi alice\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), flags)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID, info.ID
}

// sourceVerb reads the PidTagLastVerbExecuted value back from a source message.
func sourceVerb(t *testing.T, dir string, id int64) (int32, bool) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, st)
	props, err := st.GetMessageProperties(id, mapi.PrLastVerbExecuted)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := props.Get(mapi.PrLastVerbExecuted)
	if !ok {
		return 0, false
	}
	n, ok := v.(int32)
	return n, ok
}

// TestSmartReplyMarksSource proves a SmartReply stamps the reply verb
// (NOTEIVERB_REPLYTOSENDER) and the \Answered flag onto the source message named
// by the WBXML Source, while preserving the source's existing \Seen flag (the
// flag mask is rewritten whole, so a merge bug would clear it).
func TestSmartReplyMarksSource(t *testing.T) {
	ts, aliceDir, _ := sendServer(t)
	srcUID, srcID := seedSource(t, aliceDir, objectstore.FlagSeen)
	inboxFID := strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)

	sm := wbxml.Elem(wbxml.CMSmartReply,
		wbxml.Elem(wbxml.CMSource,
			wbxml.Str(wbxml.CMFolderID, inboxFID),
			wbxml.Str(wbxml.CMItemID, strconv.FormatUint(uint64(srcUID), 10))),
		wbxml.Opaque(wbxml.CMMIME, []byte(sampleMIME)))
	resp, out := postRaw(t, ts, "SmartReply", wbxml.Marshal(sm))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, out)
	}

	st, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := st.MessageFlags(int64(mapi.PrivateFIDInbox), srcUID)
	closeStore(t, st)
	if err != nil {
		t.Fatal(err)
	}
	if flags&objectstore.FlagAnswered == 0 {
		t.Error("source not marked \\Answered after SmartReply")
	}
	if flags&objectstore.FlagSeen == 0 {
		t.Error("source's \\Seen flag was cleared (flag mask not merged)")
	}
	if v, ok := sourceVerb(t, aliceDir, srcID); !ok || v != mapi.NoteVerbReplyToSender {
		t.Errorf("source PidTagLastVerbExecuted = %d (present=%v), want %d", v, ok, mapi.NoteVerbReplyToSender)
	}
}

// TestMarkingAVanishedSourceIsReported: a source that is gone by the time the
// send finishes (deleted on another device) cannot be marked, and the failure is
// returned so the send path records it rather than claiming the mark landed.
func TestMarkingAVanishedSourceIsReported(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer closeStore(t, st)
	for _, src := range []replySource{
		{fid: int64(mapi.PrivateFIDInbox), uid: 4242},
		{fid: int64(mapi.PrivateFIDCalendar), objID: 4242},
	} {
		if err := markReplyForwardSource(st, src); err == nil {
			t.Errorf("marking the missing source %+v reported success", src)
		}
	}
}

// TestSmartForwardMarksSource proves a SmartForward stamps the forward verb
// (NOTEIVERB_FORWARD) on the source and, since there is no IMAP forwarded flag,
// leaves \Answered unset.
func TestSmartForwardMarksSource(t *testing.T) {
	ts, aliceDir, _ := sendServer(t)
	srcUID, srcID := seedSource(t, aliceDir, objectstore.FlagSeen)
	inboxFID := strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)

	sm := wbxml.Elem(wbxml.CMSmartForward,
		wbxml.Elem(wbxml.CMSource,
			wbxml.Str(wbxml.CMFolderID, inboxFID),
			wbxml.Str(wbxml.CMItemID, strconv.FormatUint(uint64(srcUID), 10))),
		wbxml.Opaque(wbxml.CMMIME, []byte(sampleMIME)))
	resp, out := postRaw(t, ts, "SmartForward", wbxml.Marshal(sm))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, out)
	}

	if v, ok := sourceVerb(t, aliceDir, srcID); !ok || v != mapi.NoteVerbForward {
		t.Errorf("source PidTagLastVerbExecuted = %d (present=%v), want %d", v, ok, mapi.NoteVerbForward)
	}
	st, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := st.MessageFlags(int64(mapi.PrivateFIDInbox), srcUID)
	closeStore(t, st)
	if err != nil {
		t.Fatal(err)
	}
	if flags&objectstore.FlagAnswered != 0 {
		t.Error("SmartForward set \\Answered; a forward must not mark the source replied")
	}
}

// TestSmartReplyURLQuerySource proves the source can also come from the URL query
// (ItemId/CollectionId), the only carrier when the body is legacy raw MIME with no
// WBXML envelope to hold a Source element.
func TestSmartReplyURLQuerySource(t *testing.T) {
	ts, aliceDir, _ := sendServer(t)
	srcUID, srcID := seedSource(t, aliceDir, objectstore.FlagSeen)
	inboxFID := strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)

	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=SmartReply&User=" + testUser +
		"&DeviceId=dev1&DeviceType=iPhone&ItemId=" + strconv.FormatUint(uint64(srcUID), 10) +
		"&CollectionId=" + inboxFID
	req, err := http.NewRequest("POST", url, bytes.NewReader([]byte(sampleMIME)))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustNoErr(t, "close the reply", resp.Body.Close())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}

	st, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := st.MessageFlags(int64(mapi.PrivateFIDInbox), srcUID)
	closeStore(t, st)
	if err != nil {
		t.Fatal(err)
	}
	if flags&objectstore.FlagAnswered == 0 {
		t.Error("URL-query source not marked \\Answered")
	}
	if v, ok := sourceVerb(t, aliceDir, srcID); !ok || v != mapi.NoteVerbReplyToSender {
		t.Errorf("source verb = %d (present=%v), want %d", v, ok, mapi.NoteVerbReplyToSender)
	}
}

// TestSendMailLeavesTheOnlyCopyToAnExclusiveMailbox sends as bob, who granted alice
// send-as, files his own copy and asks for it to be the only one: alice's Sent Items
// stays empty although the device asked for a copy.
func TestSendMailLeavesTheOnlyCopyToAnExclusiveMailbox(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	st, err := objectstore.Open(bobDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSendAs([]string{testUser}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSentCopyConfig(objectstore.SentCopyConfig{ForSendAs: true, Exclusive: true}); err != nil {
		t.Fatal(err)
	}
	closeStore(t, st)
	asBob := "From: bob@hermex.test\r\nTo: bob@hermex.test\r\nSubject: team\r\n" +
		"Date: Mon, 15 Jun 2026 09:00:00 +0000\r\nMessage-ID: <x1@hermex.test>\r\n\r\nhi\r\n"
	sm := wbxml.Elem(wbxml.CMSendMail,
		wbxml.Empty(wbxml.CMSaveInSentItems),
		wbxml.Opaque(wbxml.CMMIME, []byte(asBob)))
	if resp, out := postRaw(t, ts, "SendMail", wbxml.Marshal(sm)); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, out)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("alice's Sent has %d messages, want none", n)
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDSentItems)); n != 1 {
		t.Errorf("bob's Sent has %d messages, want his copy", n)
	}
}
