package activesync

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// These tests take their expectations from [MS-ASCMD] 2.2.1.19/2.2.1.20 (the server
// sends the full text of the original), 2.2.3.148 (with ReplaceMime the server
// MUST NOT include the original's body or attachments) and the chosen form: the
// original follows the device's text, and a forward carries its attachments.

const replyMIME = "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: Re: Original\r\n" +
	"Date: Mon, 15 Jun 2026 09:00:00 +0000\r\nMessage-ID: <r1@hermex.test>\r\n" +
	"In-Reply-To: <orig@hermex.test>\r\nReferences: <orig@hermex.test>\r\n\r\nsounds good\r\n"

// seedWithAttachment files a source message carrying a named attachment.
func seedWithAttachment(t *testing.T, dir string) uint32 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	raw := "From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Original\r\n" +
		"Message-ID: <orig@hermex.test>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nhi alice\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=plan.pdf\r\n\r\nPDFDATA\r\n--b--\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}

// firstRaw returns the bytes of the first message in a mailbox folder.
func firstRaw(t *testing.T, dir string, fid int64) string {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	msgs, err := st.ListMessages(fid)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("folder %d holds no message (%v)", fid, err)
	}
	raw, err := st.GetMessageRaw(fid, msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func smartRequest(tag wbxml.Tag, uid uint32, mime string, replace bool) []byte {
	kids := []*wbxml.Node{
		wbxml.Elem(wbxml.CMSource,
			wbxml.Str(wbxml.CMFolderID, strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)),
			wbxml.Str(wbxml.CMItemID, strconv.FormatUint(uint64(uid), 10))),
		wbxml.Empty(wbxml.CMSaveInSentItems),
	}
	if replace {
		kids = append(kids, wbxml.Empty(wbxml.CMReplaceMime))
	}
	kids = append(kids, wbxml.Opaque(wbxml.CMMIME, []byte(mime)))
	return wbxml.Marshal(wbxml.Elem(tag, kids...))
}

// TestSmartReplyAddsTheOriginal sends a reply without ReplaceMime: bob receives
// the device's text followed by the original's, the device's identity headers
// survive the rebuild, the original's attachment does not ride on a reply, and the
// Sent copy is the message that was sent.
func TestSmartReplyAddsTheOriginal(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	uid := seedWithAttachment(t, aliceDir)
	resp, out := postRaw(t, ts, "SmartReply", smartRequest(wbxml.CMSmartReply, uid, replyMIME, false))
	if resp.StatusCode != 200 || len(out) != 0 {
		t.Fatalf("HTTP %d, body %q", resp.StatusCode, out)
	}
	got := firstRaw(t, bobDir, int64(mapi.PrivateFIDInbox))
	for _, want := range []string{"sounds good", "hi alice", "Message-ID: <r1@hermex.test>",
		"In-Reply-To: <orig@hermex.test>", "References: <orig@hermex.test>", "alice@hermex.test", "Mon, 15 Jun 2026 09:00:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("delivered reply lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "sounds good") > strings.Index(got, "hi alice") {
		t.Error("the original comes before the device's text")
	}
	if strings.Contains(got, "plan.pdf") {
		t.Error("a reply carries the original's attachment")
	}
	if sent := firstRaw(t, aliceDir, int64(mapi.PrivateFIDSentItems)); !strings.Contains(sent, "hi alice") {
		t.Errorf("the Sent copy is not the message that was sent:\n%s", sent)
	}
}

// TestSmartForwardCarriesTheOriginalAttachments forwards without ReplaceMime: the
// forward holds the original's text and its attachment.
func TestSmartForwardCarriesTheOriginalAttachments(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	uid := seedWithAttachment(t, aliceDir)
	fwd := strings.Replace(replyMIME, "Re: Original", "Fw: Original", 1)
	resp, out := postRaw(t, ts, "SmartForward", smartRequest(wbxml.CMSmartForward, uid, fwd, false))
	if resp.StatusCode != 200 || len(out) != 0 {
		t.Fatalf("HTTP %d, body %q", resp.StatusCode, out)
	}
	got := firstRaw(t, bobDir, int64(mapi.PrivateFIDInbox))
	for _, want := range []string{"sounds good", "hi alice", "plan.pdf"} {
		if !strings.Contains(got, want) {
			t.Errorf("delivered forward lacks %q:\n%s", want, got)
		}
	}
}

// TestSmartReplyWithReplaceMimeSendsTheDeviceMessage checks 2.2.3.148: with
// ReplaceMime nothing of the original is added.
func TestSmartReplyWithReplaceMimeSendsTheDeviceMessage(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	uid := seedWithAttachment(t, aliceDir)
	resp, out := postRaw(t, ts, "SmartForward", smartRequest(wbxml.CMSmartForward, uid, replyMIME, true))
	if resp.StatusCode != 200 || len(out) != 0 {
		t.Fatalf("HTTP %d, body %q", resp.StatusCode, out)
	}
	got := firstRaw(t, bobDir, int64(mapi.PrivateFIDInbox))
	if strings.Contains(got, "hi alice") || strings.Contains(got, "plan.pdf") {
		t.Errorf("ReplaceMime forward carries the original:\n%s", got)
	}
	if !strings.Contains(got, "sounds good") {
		t.Errorf("ReplaceMime forward lost the device's own text:\n%s", got)
	}
}

// TestSmartReplyLeavesASignedMessageAlone sends a multipart/signed reply without
// ReplaceMime: adding text would break the signature, so the signed part goes out
// byte for byte.
func TestSmartReplyLeavesASignedMessageAlone(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	uid := seedWithAttachment(t, aliceDir)
	signedPart := "Content-Type: text/plain\r\n\r\nsigned words\r\n"
	signed := "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: Re: Original\r\n" +
		"Message-ID: <s9@hermex.test>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256; boundary=s\r\n\r\n" +
		"--s\r\n" + signedPart + "--s\r\nContent-Type: application/pkcs7-signature\r\n\r\nSIG\r\n--s--\r\n"
	resp, out := postRaw(t, ts, "SmartReply", smartRequest(wbxml.CMSmartReply, uid, signed, false))
	if resp.StatusCode != 200 || len(out) != 0 {
		t.Fatalf("HTTP %d, body %q", resp.StatusCode, out)
	}
	got := firstRaw(t, bobDir, int64(mapi.PrivateFIDInbox))
	if strings.Contains(got, "hi alice") || !bytes.Contains([]byte(got), []byte(signedPart)) {
		t.Errorf("the signed message was changed:\n%s", got)
	}
}
