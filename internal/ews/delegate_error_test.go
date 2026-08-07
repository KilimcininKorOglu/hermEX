package ews

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// delegateSink records the events a delegate mutation emits.
type delegateSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *delegateSink) Write(e logging.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *delegateSink) find(name string) (logging.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// TestDelegateWriteFailureIsSanitizedAndRecorded proves the two halves that were
// both missing. MessageText is serialized straight into the response body, so the
// raw store error went to the mailbox owner, naming internals they can do nothing
// with; and nothing on this path logged, so the operator had no record that the
// delegate had failed to be configured at all.
func TestDelegateWriteFailureIsSanitizedAndRecorded(t *testing.T) {
	sink := &delegateSink{}
	accs := directory.StaticAccounts{}
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.Logger = logging.New(sink)

	const secret = "objectstore: open /var/lib/hermex/alice/objects.sqlite3: permission denied"
	msg := srv.delegateWriteFailed("bob@hermex.test", errors.New(secret))

	if strings.Contains(msg.MessageText, secret) || strings.Contains(msg.MessageText, "sqlite3") {
		t.Errorf("the response carries the raw error: %q", msg.MessageText)
	}
	if msg.MessageText == "" {
		t.Error("the response says nothing at all about the failure")
	}
	if msg.ResponseCode != "ErrorInternalServerError" || msg.ResponseClass != "Error" {
		t.Errorf("response = %s/%s, want Error/ErrorInternalServerError", msg.ResponseClass, msg.ResponseCode)
	}
	// The failure has to name which delegate it was, or a batch reports a failure
	// with no way to tell whose.
	if msg.DelegateUser == nil || msg.DelegateUser.UserId.PrimarySmtpAddress != "bob@hermex.test" {
		t.Errorf("the failure does not name the delegate: %+v", msg.DelegateUser)
	}

	e, ok := sink.find("operation.fail")
	if !ok {
		t.Fatal("the failure was not recorded, so the operator never learns of it")
	}
	if e.Err != secret {
		t.Errorf("logged error = %q, want the full detail", e.Err)
	}
	if e.Fields["delegate"] != "bob@hermex.test" {
		t.Errorf("the log line does not name the delegate: %v", e.Fields)
	}
	if e.Level != logging.LevelError || e.Subsystem != logging.EWS {
		t.Errorf("event = %s/%s, want error/ews", e.Level, e.Subsystem)
	}
}

// TestDelegateWriteFailureLoggingIsOptional proves a server with no logger still
// answers. Recording a failure must never be able to turn it into a second one.
func TestDelegateWriteFailureLoggingIsOptional(t *testing.T) {
	accs := directory.StaticAccounts{}
	srv := NewServer(accs, accs, "mail.hermex.test")
	if msg := srv.delegateWriteFailed("bob@hermex.test", errors.New("boom")); msg.ResponseCode == "" {
		t.Error("no response was produced without a logger")
	}
}

// TestSuccessfulDelegateAddSaysNothingExtra guards the other direction: a delegate
// that was configured must come back clean, with no message text to explain.
func TestSuccessfulDelegateAddSaysNothingExtra(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], nil)

	body := wrapRequest(`<AddDelegate xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<Mailbox><t:EmailAddress>` + testUser + `</t:EmailAddress></Mailbox>` +
		`<DelegateUsers><t:DelegateUser><t:UserId>` +
		`<t:PrimarySmtpAddress>bob@hermex.test</t:PrimarySmtpAddress>` +
		`</t:UserId><t:DelegatePermissions>` +
		`<t:CalendarFolderPermissionLevel>Editor</t:CalendarFolderPermissionLevel>` +
		`</t:DelegatePermissions></t:DelegateUser></DelegateUsers>` +
		`</AddDelegate>`)
	_, out := soapPost(t, ts, body, true)

	if !strings.Contains(out, "NoError") {
		t.Fatalf("AddDelegate did not succeed: %s", out)
	}
	if strings.Contains(out, "MessageText") {
		t.Errorf("a successful add carries message text: %s", out)
	}
}
