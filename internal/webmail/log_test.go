package webmail

import (
	"sync"
	"testing"

	"hermex/internal/logging"
)

// captureSink records every event for assertion.
type captureSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *captureSink) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *captureSink) has(name string) (logging.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// TestSmimeOperationsLog proves the webmail S/MIME compose path emits smime.sign
// and smime.encrypt events under the smime subsystem, tagged with the acting user
// — the central-log coverage for S/MIME crypto.
func TestSmimeOperationsLog(t *testing.T) {
	sink := &captureSink{}
	srv := &Server{Logger: logging.New(sink)}
	key, cert := genWebmailIdentity(t, "alice@hermex.test")
	sess := &session{user: "alice@hermex.test", smimeKey: key, smimeCert: cert}
	raw := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: hi\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nHello.\r\n")

	if _, err := srv.applySmime(sess, nil, raw, []string{"bob@hermex.test"}, true, false); err != nil {
		t.Fatal(err)
	}
	if e, ok := sink.has("smime.sign"); !ok {
		t.Error("no smime.sign event")
	} else if e.User != "alice@hermex.test" || e.Subsystem != logging.SMIME {
		t.Errorf("smime.sign = user %q subsystem %q, want alice/smime", e.User, e.Subsystem)
	}

	st := openSmimeStore(t)
	if err := st.PutRecipientCert("bob@hermex.test", cert.Raw); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.applySmime(sess, st, raw, []string{"bob@hermex.test"}, false, true); err != nil {
		t.Fatal(err)
	}
	if e, ok := sink.has("smime.encrypt"); !ok {
		t.Error("no smime.encrypt event")
	} else if e.User != "alice@hermex.test" {
		t.Errorf("smime.encrypt user = %q, want alice", e.User)
	}
}
