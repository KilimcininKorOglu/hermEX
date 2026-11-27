package main

import (
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

type fakeDomains struct {
	list []directory.DomainInfo
	err  error
}

func (f fakeDomains) ListDomains() ([]directory.DomainInfo, error) { return f.list, f.err }

func autoconfigGet(t *testing.T, dir domainLister, host, target string) *httptest.ResponseRecorder {
	t.Helper()
	passed := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	withAutoconfig("mail.example.com", dir, nil, passed).ServeHTTP(rec, req)
	return rec
}

// goneWriter is a response writer whose client has disconnected: every body write
// fails.
type goneWriter struct{ *httptest.ResponseRecorder }

func (goneWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

// WriteString overrides the recorder's own, which io.WriteString would otherwise
// call and so bypass the failing Write.
func (goneWriter) WriteString(string) (int, error) { return 0, errors.New("client gone") }

// TestAutoconfigRecordsAFailedWrite proves a document the client never received
// leaves an autoconfig.write.fail event, so a client that reports no settings has
// a cause in the operator's log.
func TestAutoconfigRecordsAFailedWrite(t *testing.T) {
	sink := &eventSink{}
	req := httptest.NewRequest(http.MethodGet, autoconfigPath+"?emailaddress=a@example.com", nil)
	h := withAutoconfig("mail.example.com", hostedExample, logging.New(sink), http.NotFoundHandler())
	h.ServeHTTP(goneWriter{httptest.NewRecorder()}, req)

	for _, e := range sink.events {
		if e.Name == "autoconfig.write.fail" && strings.Contains(e.Err, "client gone") {
			return
		}
	}
	t.Fatalf("events = %+v, want an autoconfig.write.fail naming the error", sink.events)
}

var hostedExample = fakeDomains{list: []directory.DomainInfo{{Name: "example.com"}, {Name: "off.example", Status: 1}}}

// A client asking for a hosted domain gets the clientConfig 1.1 document with
// IMAP 993/SSL, POP3 995/SSL, SMTP 465/SSL and 587/STARTTLS on the server host.
func TestAutoconfigAdvertisesTLSEndpoints(t *testing.T) {
	for _, c := range []struct{ host, target string }{
		{"autoconfig.example.com", "/mail/config-v1.1.xml?emailaddress=alice%40example.com"},
		{"autoconfig.example.com", "/mail/config-v1.1.xml"},
		{"example.com", "/.well-known/autoconfig/mail/config-v1.1.xml?emailaddress=Bob%40Example.COM"},
	} {
		rec := autoconfigGet(t, hostedExample, c.host, c.target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s%s: status %d", c.host, c.target, rec.Code)
		}
		checkClientConfig(t, rec.Body.Bytes())
	}
}

// checkClientConfig asserts the document names example.com and exactly the four
// TLS endpoints on mail.example.com, signing in with the full address.
func checkClientConfig(t *testing.T, body []byte) {
	t.Helper()
	var cfg clientConfig
	if err := xml.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Version != "1.1" || cfg.Provider.Domain != "example.com" {
		t.Fatalf("version %q domain %q", cfg.Version, cfg.Provider.Domain)
	}
	want := map[string]bool{"imap/993/SSL": true, "pop3/995/SSL": true, "smtp/465/SSL": true, "smtp/587/STARTTLS": true}
	for _, s := range append(cfg.Provider.Incoming, cfg.Provider.Outgoing...) {
		key := s.Type + "/" + strconv.Itoa(s.Port) + "/" + s.SocketType
		if !want[key] || s.Hostname != "mail.example.com" || s.Username != "%EMAILADDRESS%" {
			t.Fatalf("unexpected server %+v", s)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing servers %v", want)
	}
}

// The server must not advertise itself for a domain it does not host, nor
// for a suspended one, nor when the address names no domain.
func TestAutoconfigRefusesForeignOrInactiveDomain(t *testing.T) {
	for _, target := range []string{
		"/mail/config-v1.1.xml?emailaddress=x%40other.example",
		"/mail/config-v1.1.xml?emailaddress=x%40off.example",
		"/mail/config-v1.1.xml?emailaddress=noat",
	} {
		if rec := autoconfigGet(t, hostedExample, "autoconfig.example.com", target); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", target, rec.Code)
		}
	}
}

// A directory failure answers 500 without the error text; other paths pass through.
func TestAutoconfigDirectoryFailureAndPassThrough(t *testing.T) {
	rec := autoconfigGet(t, fakeDomains{err: errors.New("db secret detail")}, "autoconfig.example.com", "/mail/config-v1.1.xml")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if rec := autoconfigGet(t, hostedExample, "mail.example.com", "/inbox"); rec.Code != http.StatusTeapot {
		t.Fatalf("pass-through status %d", rec.Code)
	}
}
