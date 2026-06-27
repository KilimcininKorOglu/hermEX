package ews

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// seedRaw appends a raw message with caller-supplied headers to a folder.
func seedRaw(t *testing.T, dir string, fid int64, raw string, received time.Time) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AppendMessage(fid, []byte(raw), received, 0); err != nil {
		t.Fatal(err)
	}
}

// seedThread seeds a two-message thread (a root and a reply that references it)
// plus one unrelated message in the Inbox.
func seedThread(t *testing.T, dir string) {
	seedRaw(t, dir, int64(mapi.PrivateFIDInbox),
		"From: Alice <alice@x.test>\r\nMessage-Id: <root@x>\r\nSubject: Project plan\r\n\r\nroot body\r\n",
		time.Unix(1718200000, 0))
	seedRaw(t, dir, int64(mapi.PrivateFIDInbox),
		"From: Bob <bob@x.test>\r\nMessage-Id: <reply@x>\r\nReferences: <root@x>\r\nSubject: Re: Project plan\r\n\r\nreply body\r\n",
		time.Unix(1718200100, 0))
	seedRaw(t, dir, int64(mapi.PrivateFIDInbox),
		"From: Carol <carol@x.test>\r\nMessage-Id: <other@x>\r\nSubject: Lunch?\r\n\r\nunrelated\r\n",
		time.Unix(1718200200, 0))
}

func findConversationBody(distinguished string) string {
	return `<FindConversation xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<ParentFolderId><t:DistinguishedFolderId Id="` + distinguished + `"/></ParentFolderId>` +
		`</FindConversation>`
}

type parsedFindConversation struct {
	Code          string `xml:"Body>FindConversationResponse>ResponseCode"`
	Conversations []struct {
		ConversationID struct {
			ID string `xml:"Id,attr"`
		} `xml:"ConversationId"`
		Topic   string   `xml:"ConversationTopic"`
		Count   int      `xml:"MessageCount"`
		Unread  int      `xml:"UnreadCount"`
		Senders []string `xml:"UniqueSenders>String"`
	} `xml:"Body>FindConversationResponse>Conversations>Conversation"`
}

// TestFindConversationGroups proves a threaded pair collapses to one conversation
// (message count 2) while an unrelated message forms its own.
func TestFindConversationGroups(t *testing.T) {
	ts, dir := seededEWS(t)
	seedThread(t, dir)

	_, body := soapPost(t, ts, wrapRequest(findConversationBody("inbox")), true)
	var p parsedFindConversation
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse FindConversation: %v\n%s", err, body)
	}
	if p.Code != "NoError" {
		t.Fatalf("ResponseCode = %q, want NoError\n%s", p.Code, body)
	}
	if len(p.Conversations) != 2 {
		t.Fatalf("got %d conversations, want 2\n%s", len(p.Conversations), body)
	}

	var thread, single bool
	for _, c := range p.Conversations {
		switch c.Count {
		case 2:
			thread = true
			if c.Topic != "Re: Project plan" {
				t.Errorf("thread topic = %q, want the latest subject", c.Topic)
			}
			if len(c.Senders) != 2 {
				t.Errorf("thread unique senders = %v, want 2", c.Senders)
			}
		case 1:
			single = true
		}
		if c.ConversationID.ID == "" {
			t.Error("conversation missing an id")
		}
	}
	if !thread || !single {
		t.Errorf("expected one 2-message thread and one single: %+v", p.Conversations)
	}
}

// threadConversationID returns the id of the seeded two-message thread.
func threadConversationID(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, body := soapPost(t, ts, wrapRequest(findConversationBody("inbox")), true)
	var p parsedFindConversation
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	for _, c := range p.Conversations {
		if c.Count == 2 {
			return c.ConversationID.ID
		}
	}
	t.Fatalf("no two-message thread in %s", body)
	return ""
}

func getConversationItemsBody(id string) string {
	return `<GetConversationItems xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<ItemShape><t:BaseShape>IdOnly</t:BaseShape></ItemShape>` +
		`<Conversations><t:Conversation><t:ConversationId Id="` + id + `"/></t:Conversation></Conversations>` +
		`</GetConversationItems>`
}

type parsedGetConvItems struct {
	Nodes []struct {
		InternetMessageID string   `xml:"InternetMessageId"`
		Subjects          []string `xml:"Items>Message>Subject"`
	} `xml:"Body>GetConversationItemsResponse>ResponseMessages>GetConversationItemsResponseMessage>Conversation>ConversationNodes>ConversationNode"`
}

// TestGetConversationItems proves a conversation's messages come back as nodes,
// oldest first.
func TestGetConversationItems(t *testing.T) {
	ts, dir := seededEWS(t)
	seedThread(t, dir)
	id := threadConversationID(t, ts)

	_, body := soapPost(t, ts, wrapRequest(getConversationItemsBody(id)), true)
	var p parsedGetConvItems
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetConversationItems: %v\n%s", err, body)
	}
	if len(p.Nodes) != 2 {
		t.Fatalf("got %d conversation nodes, want 2\n%s", len(p.Nodes), body)
	}
	// Oldest first: the root precedes the reply.
	if got := p.Nodes[0].Subjects; len(got) != 1 || got[0] != "Project plan" {
		t.Errorf("first node subject = %v, want [Project plan]", got)
	}
	if p.Nodes[0].InternetMessageID != "<root@x>" {
		t.Errorf("first node InternetMessageId = %q, want <root@x>", p.Nodes[0].InternetMessageID)
	}
}

func applyConversationActionBody(action, id, destDistinguished string) string {
	dest := ""
	if destDistinguished != "" {
		dest = `<t:DestinationFolderId><t:DistinguishedFolderId Id="` + destDistinguished + `"/></t:DestinationFolderId>`
	}
	return `<ApplyConversationAction xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<ConversationActions><t:ConversationAction>` +
		`<t:Action>` + action + `</t:Action>` +
		`<t:ConversationId Id="` + id + `"/>` +
		dest +
		`</t:ConversationAction></ConversationActions></ApplyConversationAction>`
}

// TestApplyConversationActionMove proves moving a conversation relocates every one
// of its messages, leaving the unrelated message in place.
func TestApplyConversationActionMove(t *testing.T) {
	ts, dir := seededEWS(t)
	seedThread(t, dir)
	id := threadConversationID(t, ts)

	_, body := soapPost(t, ts, wrapRequest(applyConversationActionBody("Move", id, "junkemail")), true)
	if !strings.Contains(body, `ResponseClass="Success"`) {
		t.Fatalf("ApplyConversationAction Move failed: %s", body)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDJunk)); n != 2 {
		t.Errorf("Junk has %d messages, want 2 (the moved thread)", n)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("Inbox has %d messages, want 1 (the unrelated message)", n)
	}
}

// TestApplyConversationActionSetReadState proves SetReadState marks every message
// of a conversation read.
func TestApplyConversationActionSetReadState(t *testing.T) {
	ts, dir := seededEWS(t)
	seedThread(t, dir)
	id := threadConversationID(t, ts)

	body := strings.Replace(applyConversationActionBody("SetReadState", id, ""),
		"<t:ConversationId", "<t:IsRead>true</t:IsRead><t:ConversationId", 1)
	_, resp := soapPost(t, ts, wrapRequest(body), true)
	if !strings.Contains(resp, `ResponseClass="Success"`) {
		t.Fatalf("SetReadState failed: %s", resp)
	}

	seen := folderSeen(t, dir, int64(mapi.PrivateFIDInbox))
	read := 0
	for _, s := range seen {
		if s {
			read++
		}
	}
	if read != 2 {
		t.Errorf("%d of %d inbox messages read, want the 2 thread messages read", read, len(seen))
	}
}
