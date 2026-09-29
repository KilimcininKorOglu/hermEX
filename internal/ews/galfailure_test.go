package ews

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// failingGAL is a directory whose address book cannot be read.
type failingGAL struct {
	directory.StaticAccounts
}

func (failingGAL) SearchGAL(string, string, int) ([]directory.GALEntry, error) {
	return nil, errors.New("directory unavailable")
}

// TestAddressBookFailureIsReported proves an address-book read failure answers
// as a server error on every EWS operation that reads the address book. They
// used to answer it as an unknown name, a person not found or a missing photo,
// so a directory outage looked like an empty directory and was recorded nowhere.
func TestAddressBookFailureIsReported(t *testing.T) {
	accs := failingGAL{directory.StaticAccounts{
		"alice@hermex.test": {Password: testPass, MailboxPath: t.TempDir()},
	}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)

	for name, req := range map[string]string{
		"ResolveNames": resolveReq("bob"),
		"FindPeople":   wrapRequest(`<FindPeople xmlns="` + nsMessages + `"><QueryString>bob</QueryString></FindPeople>`),
		"GetPersona": wrapRequest(`<GetPersona xmlns="` + nsMessages + `"><EmailAddress xmlns="` + nsTypes +
			`"><EmailAddress>bob@hermex.test</EmailAddress></EmailAddress></GetPersona>`),
		"GetUserPhoto": wrapRequest(`<GetUserPhoto xmlns="` + nsMessages + `"><Email>bob@hermex.test</Email><SizeRequested>HR48x48</SizeRequested></GetUserPhoto>`),
	} {
		_, body := soapPost(t, ts, req, true)
		if !strings.Contains(body, "ErrorInternalServerError") {
			t.Errorf("%s on a failing address book = %s, want ErrorInternalServerError", name, body)
		}
	}
}
