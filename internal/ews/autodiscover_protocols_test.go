package ews

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
)

type poxReply struct {
	Response struct {
		Account struct {
			Protocol []struct {
				TypeAttr  string `xml:"Type,attr"`
				Version   string `xml:"Version,attr"`
				Type      string `xml:"Type"`
				Server    string `xml:"Server"`
				Port      int    `xml:"Port"`
				SSL       string `xml:"SSL"`
				LoginName string `xml:"LoginName"`
				MailStore struct {
					ExternalURL string `xml:"ExternalUrl"`
				} `xml:"MailStore"`
				AddressBook struct {
					ExternalURL string `xml:"ExternalUrl"`
				} `xml:"AddressBook"`
			} `xml:"Protocol"`
		} `xml:"Account"`
	} `xml:"Response"`
}

func poxPost(t *testing.T, url string, mapiHTTP bool) poxReply {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/autodiscover/autodiscover.xml", strings.NewReader("<Autodiscover/>"))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	if mapiHTTP {
		req.Header.Set("X-MapiHttpCapability", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out poxReply
	if err := xml.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// [MS-OXDSCLI] 2.2.4.1.1.2.3: the Outlook response lists every provider the
// client may use. IMAP, POP3 and SMTP appear over implicit TLS on 993, 995 and
// 465, signing in with the caller's address, beside Outlook Anywhere.
func TestAutodiscoverListsInternetProtocols(t *testing.T) {
	ts := newTestServer(t)
	reply := poxPost(t, ts.URL, false)
	want := map[string]int{"EXPR": 0, "IMAP": 993, "POP3": 995, "SMTP": 465}
	for _, p := range reply.Response.Account.Protocol {
		if p.TypeAttr == "mapiHttp" {
			t.Fatal("mapiHttp offered to a client that did not send X-MapiHttpCapability")
		}
		port, ok := want[p.Type]
		if !ok || p.Port != port || p.SSL != "On" {
			t.Fatalf("unexpected protocol %+v", p)
		}
		if port != 0 && p.LoginName != testUser {
			t.Fatalf("%s LoginName %q, want %q", p.Type, p.LoginName, testUser)
		}
		delete(want, p.Type)
	}
	if len(want) != 0 {
		t.Fatalf("missing protocols %v", want)
	}
}

// [MS-OXDSCLI] 2.2.2.1: a client that sends X-MapiHttpCapability gets the
// mapiHttp provider first, with the emsmdb and nspi URLs for its mailbox.
func TestAutodiscoverOffersMapiHTTPWhenAsked(t *testing.T) {
	ts := newTestServer(t)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/autodiscover/autodiscover.xml", strings.NewReader("<Autodiscover/>"))
	mustNoErr(t, "build unauth mapiHttp request", err)
	req.Header.Set("X-MapiHttpCapability", "1")
	unauthResp, err := http.DefaultClient.Do(req)
	mustNoErr(t, "unauth mapiHttp probe", err)
	defer closeBody(t, unauthResp)
	wantEq(t, "unauth mapiHttp status", unauthResp.StatusCode, http.StatusUnauthorized)

	p := poxPost(t, ts.URL, true).Response.Account.Protocol
	if len(p) == 0 || p[0].TypeAttr != "mapiHttp" || p[0].Version != "1" {
		t.Fatalf("first protocol %+v, want mapiHttp version 1", p)
	}
	if !strings.Contains(p[0].MailStore.ExternalURL, "/mapi/emsmdb/?MailboxId=") ||
		!strings.Contains(p[0].AddressBook.ExternalURL, "/mapi/nspi/?MailboxId=") {
		t.Fatalf("mapiHttp URLs %+v", p[0])
	}
}
