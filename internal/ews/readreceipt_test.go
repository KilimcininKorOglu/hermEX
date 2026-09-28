package ews

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// receiptRequest is a message from bob that asks for a read receipt, as delivered:
// its Return-Path is bob, so an automatic receipt may answer it.
func receiptRequest(msgID string) string {
	return "Return-Path: <bob@hermex.test>\r\nFrom: Bob <bob@hermex.test>\r\nTo: Alice <alice@hermex.test>\r\n" +
		"Message-Id: <" + msgID + "@hermex.test>\r\nSubject: Did you read this\r\n" +
		"Disposition-Notification-To: bob@hermex.test\r\n\r\nplease confirm\r\n"
}

// receiptServer builds an EWS server for alice whose Inbox holds the given
// messages, with bob as a local mailbox the receipts are delivered to. It returns
// the server, alice's mailbox dir and bob's.
func receiptServer(t *testing.T, raws ...string) (ts *httptest.Server, aliceDir, bobDir string) {
	t.Helper()
	aliceDir, bobDir = t.TempDir(), filepath.Join(t.TempDir(), "bob")
	for _, raw := range raws {
		seedRaw(t, aliceDir, int64(mapi.PrivateFIDInbox), raw, time.Unix(1718200000, 0))
	}
	accs := directory.StaticAccounts{
		testUser:          {Password: testPass, MailboxPath: aliceDir},
		"bob@hermex.test": {Password: "x", MailboxPath: bobDir},
	}
	ts = httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, aliceDir, bobDir
}

// receiptsDelivered counts the read receipts in bob's Inbox.
func receiptsDelivered(t *testing.T, bobDir string) int {
	t.Helper()
	st, err := objectstore.Open(bobDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// receiptItemID returns the item id FindItem reports first for alice's Inbox.
func receiptItemID(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	return firstInboxItemID(t, fi)
}

// setIsReadReq builds an UpdateItem that sets IsRead, with the given extra
// attributes on the UpdateItem element.
func setIsReadReq(itemID string, read bool, attrs string) string {
	v := "false"
	if read {
		v = "true"
	}
	return wrapRequest(`<UpdateItem ConflictResolution="AutoResolve" ` + attrs + ` xmlns="` + nsMessages + `">` +
		`<ItemChanges><t:ItemChange xmlns:t="` + nsTypes + `">` +
		`<t:ItemId Id="` + itemID + `"/>` +
		`<t:Updates><t:SetItemField><t:FieldURI FieldURI="message:IsRead"/>` +
		`<t:Message><t:IsRead>` + v + `</t:IsRead></t:Message></t:SetItemField></t:Updates>` +
		`</t:ItemChange></ItemChanges></UpdateItem>`)
}

// TestUpdateItemIsReadSendsReceipt proves marking a message read through UpdateItem
// sends the receipt it asks for once: marking it unread and read again sends no
// second one, because the request was consumed.
func TestUpdateItemIsReadSendsReceipt(t *testing.T) {
	ts, _, bobDir := receiptServer(t, receiptRequest("r1"))
	id := receiptItemID(t, ts)

	_, out := soapPost(t, ts, setIsReadReq(id, true, ""), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem failed: %s", out)
	}
	wantEq(t, "receipts after the first read", receiptsDelivered(t, bobDir), 1)
	soapPost(t, ts, setIsReadReq(id, false, ""), true)
	soapPost(t, ts, setIsReadReq(id, true, ""), true)
	wantEq(t, "receipts after reading it again", receiptsDelivered(t, bobDir), 1)
}

// TestUpdateItemSuppressReadReceipts proves SuppressReadReceipts marks the message
// read without a receipt.
func TestUpdateItemSuppressReadReceipts(t *testing.T) {
	ts, aliceDir, bobDir := receiptServer(t, receiptRequest("r1"))
	id := receiptItemID(t, ts)

	soapPost(t, ts, setIsReadReq(id, true, `SuppressReadReceipts="true"`), true)
	wantEq(t, "receipts after a suppressed read", receiptsDelivered(t, bobDir), 0)
	if seen := folderSeen(t, aliceDir, int64(mapi.PrivateFIDInbox)); len(seen) != 1 || !seen[0] {
		t.Errorf("read state after a suppressed read = %v, want read", seen)
	}
}

// TestSuppressReadReceiptResponseObject proves a SuppressReadReceipt response object
// declines the receipt, so marking the message read afterwards sends nothing.
func TestSuppressReadReceiptResponseObject(t *testing.T) {
	ts, _, bobDir := receiptServer(t, receiptRequest("r1"))
	id := receiptItemID(t, ts)

	req := wrapRequest(`<CreateItem MessageDisposition="SendAndSaveCopy" xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<Items><t:SuppressReadReceipt><t:ReferenceItemId Id="` + id + `"/></t:SuppressReadReceipt></Items></CreateItem>`)
	_, out := soapPost(t, ts, req, true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("SuppressReadReceipt failed: %s", out)
	}
	soapPost(t, ts, setIsReadReq(id, true, ""), true)
	wantEq(t, "receipts after a declined request", receiptsDelivered(t, bobDir), 0)
}

// markAllReadWith builds a MarkAllItemsAsRead of the Inbox with the given
// SuppressReadReceipts value.
func markAllReadWith(suppress string) string {
	return wrapRequest(`<MarkAllItemsAsRead xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<ReadFlag>true</ReadFlag><SuppressReadReceipts>` + suppress + `</SuppressReadReceipts>` +
		`<FolderIds><t:DistinguishedFolderId Id="inbox"/></FolderIds></MarkAllItemsAsRead>`)
}

// TestMarkAllItemsAsReadReceipts proves a bulk read sends one receipt per message
// that asks for one, and none when SuppressReadReceipts is true.
func TestMarkAllItemsAsReadReceipts(t *testing.T) {
	ts, _, bobDir := receiptServer(t, receiptRequest("r1"), receiptRequest("r2"), plainMessage)
	soapPost(t, ts, markAllReadWith("false"), true)
	wantEq(t, "receipts after a bulk read", receiptsDelivered(t, bobDir), 2)

	ts, _, bobDir = receiptServer(t, receiptRequest("r1"))
	soapPost(t, ts, markAllReadWith("true"), true)
	wantEq(t, "receipts after a suppressed bulk read", receiptsDelivered(t, bobDir), 0)
}

// TestConversationSetReadStateSendsReceipts proves marking a conversation read
// sends the receipts its messages ask for.
func TestConversationSetReadStateSendsReceipts(t *testing.T) {
	ts, _, bobDir := receiptServer(t,
		receiptRequest("root"),
		"Return-Path: <bob@hermex.test>\r\nFrom: Bob <bob@hermex.test>\r\nMessage-Id: <reply@hermex.test>\r\nReferences: <root@hermex.test>\r\n"+
			"Subject: Re: Did you read this\r\nDisposition-Notification-To: bob@hermex.test\r\n\r\nand this\r\n")
	id := threadConversationID(t, ts)
	body := strings.Replace(applyConversationActionBody("SetReadState", id, ""),
		"<t:ConversationId", "<t:IsRead>true</t:IsRead><t:ConversationId", 1)
	_, out := soapPost(t, ts, wrapRequest(body), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("SetReadState failed: %s", out)
	}
	wantEq(t, "receipts after marking the conversation read", receiptsDelivered(t, bobDir), 2)
}
