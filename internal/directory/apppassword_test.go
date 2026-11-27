package directory

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"hermex/internal/logging"
	"hermex/internal/totp"
)

// twoAccounts provisions two mailboxes, which is what proves a credential is
// bound to the account that minted it.
func twoAccounts(t *testing.T) *SQLDirectory {
	t.Helper()
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "acme.test")
	for _, u := range []string{"u@acme.test", "v@acme.test"} {
		seedUser(t, d, root, u, "password1")
	}
	return d
}

// enableSecondFactor turns a second factor on for an account.
func enableSecondFactor(t *testing.T, d *SQLDirectory, user string) {
	t.Helper()
	secret, err := totp.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.BeginTOTPEnrollment(user, secret); err != nil {
		t.Fatal(err)
	}
	plain, hashes, err := totp.NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) == 0 {
		t.Fatal("NewRecoveryCodes returned no recovery codes")
	}
	if err := d.ActivateTOTP(user, 0, hashes); err != nil {
		t.Fatal(err)
	}
}

// TestAnAppPasswordSignsInOnTheProtocols is the point of the feature: IMAP,
// POP3, SMTP submission, ActiveSync, EWS, DAV and MAPI cannot ask for a code, so
// an enrolled account needs a credential they can use.
func TestAnAppPasswordSignsInOnTheProtocols(t *testing.T) {
	d := twoAccounts(t)
	secret, err := d.CreateAppPassword("u@acme.test", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	path, ok := d.AuthenticateProtocol("u@acme.test", secret)
	if !ok {
		t.Fatal("a freshly minted credential was refused")
	}
	want, wantOK := d.Authenticate("u@acme.test", "password1")
	if !wantOK || path != want {
		t.Errorf("the credential resolved to %q, want the account's own mailbox (%q, %v)", path, want, wantOK)
	}
	// It is the account's, not anyone else's.
	if p, ok := d.AuthenticateProtocol("v@acme.test", secret); ok || p != "" {
		t.Errorf("one account's credential signed in another (%q, %v)", p, ok)
	}
	if p, ok := d.AuthenticateProtocol("u@acme.test", "not-a-credential"); ok || p != "" {
		t.Errorf("an unknown credential was accepted (%q, %v)", p, ok)
	}
}

// TestEnrollingClosesTheAccountPasswordOnTheProtocols is the security property
// the whole slice exists for. A second factor that leaves the account password
// working on IMAP protects only the web surfaces, and an attacker holding the
// password just uses IMAP instead.
func wantAuthProto(t *testing.T, d *SQLDirectory, user, pass string, wantOK bool) {
	t.Helper()
	p, ok := d.AuthenticateProtocol(user, pass)
	wantEq(t, "AuthenticateProtocol("+user+") ok", ok, wantOK)
	wantEq(t, "AuthenticateProtocol("+user+") non-empty path", p != "", wantOK)
}

func TestEnrollingClosesTheAccountPasswordOnTheProtocols(t *testing.T) {
	d := twoAccounts(t)

	// Before enrolling, the account password is what a mail client uses.
	wantAuthProto(t, d, "u@acme.test", "password1", true)

	enableSecondFactor(t, d, "u@acme.test")

	wantAuthProto(t, d, "u@acme.test", "password1", false)
	// The account password is still the account password everywhere a second
	// factor CAN be asked for: the webmail and panel logins go through
	// Authenticate, which is unchanged.
	p, ok := d.Authenticate("u@acme.test", "password1")
	wantEq(t, "web login ok after enrolling", ok, true)
	wantEq(t, "web login path non-empty", p != "", true)
	// And the other account is untouched.
	wantAuthProto(t, d, "v@acme.test", "password1", true)

	secret, err := d.CreateAppPassword("u@acme.test", "Phone")
	mustNoErr(t, "CreateAppPassword", err)
	wantAuthProto(t, d, "u@acme.test", secret, true)
}

// TestASuspendedAccountsCredentialStopsWorking suspends an account that holds an
// app password. Suspending is how an operator ends a compromised account, so
// every credential the account minted must stop signing in with it.
func TestASuspendedAccountsCredentialStopsWorking(t *testing.T) {
	d := twoAccounts(t)
	secret, err := d.CreateAppPassword("u@acme.test", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.UpdateUser("u@acme.test", UserUpdate{Status: afUserSuspended, POP3IMAP: true}); err != nil || !ok {
		t.Fatalf("suspend = ok %v err %v", ok, err)
	}
	if p, ok := d.AuthenticateProtocol("u@acme.test", secret); ok || p != "" {
		t.Errorf("a suspended account's app password still signs in (%q, %v)", p, ok)
	}
	if p, ok := d.AuthenticateProtocol("v@acme.test", "password1"); !ok || p == "" {
		t.Errorf("suspending one account closed another account's login (%q, %v)", p, ok)
	}
}

// TestAForcedPasswordChangeClosesAppPasswords flags an account that holds an app
// password for a forced change. An admin reset closes every client protocol
// until the owner sets a new password, so the credential must stop signing in
// too, and work again once the flag is cleared.
func TestAForcedPasswordChangeClosesAppPasswords(t *testing.T) {
	d := twoAccounts(t)
	secret, err := d.CreateAppPassword("u@acme.test", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if found, err := d.RequirePasswordChange("u@acme.test", true); err != nil || !found {
		t.Fatalf("RequirePasswordChange(true) = (%v, %v)", found, err)
	}
	if p, ok := d.AuthenticateProtocol("u@acme.test", secret); ok || p != "" {
		t.Errorf("an app password signed in while a password change is forced (%q, %v)", p, ok)
	}
	if found, err := d.RequirePasswordChange("u@acme.test", false); err != nil || !found {
		t.Fatalf("RequirePasswordChange(false) = (%v, %v)", found, err)
	}
	if p, ok := d.AuthenticateProtocol("u@acme.test", secret); !ok || p == "" {
		t.Errorf("the app password stayed closed after the flag was cleared (%q, %v)", p, ok)
	}
}

// twoCredentials mints a phone and a laptop credential and returns them with the
// phone's row id.
func twoCredentials(t *testing.T, d *SQLDirectory) (phone, laptop string, phoneID int64) {
	t.Helper()
	phone, err := d.CreateAppPassword("u@acme.test", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	laptop, err = d.CreateAppPassword("u@acme.test", "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	list, err := d.ListAppPasswords("u@acme.test")
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d entries, err %v", len(list), err)
	}
	for _, p := range list {
		if p.Name == "Phone" {
			phoneID = p.ID
		}
	}
	return phone, laptop, phoneID
}

// TestRevokingOneCredentialLeavesTheOthers is why each program gets its own: a
// lost phone must not cost the user every other client.
func TestRevokingOneCredentialLeavesTheOthers(t *testing.T) {
	d := twoAccounts(t)
	phone, laptop, phoneID := twoCredentials(t, d)

	if ok, err := d.DeleteAppPassword("u@acme.test", phoneID); err != nil || !ok {
		t.Fatalf("revoke = ok %v err %v", ok, err)
	}
	if p, ok := d.AuthenticateProtocol("u@acme.test", phone); ok || p != "" {
		t.Errorf("a revoked credential still signs in (%q, %v)", p, ok)
	}
	if p, ok := d.AuthenticateProtocol("u@acme.test", laptop); !ok || p == "" {
		t.Errorf("revoking one credential broke another (%q, %v)", p, ok)
	}
}

// TestOneAccountCannotRevokeAnothersCredential is the scoping: the id alone must
// not be enough, or anyone could work through the id space.
func TestOneAccountCannotRevokeAnothersCredential(t *testing.T) {
	d := twoAccounts(t)
	phone, laptop, phoneID := twoCredentials(t, d)

	if ok, err := d.DeleteAppPassword("v@acme.test", phoneID); err != nil || ok {
		t.Fatalf("another account revoked the credential: ok %v err %v", ok, err)
	}
	if p, ok := d.AuthenticateProtocol("u@acme.test", phone); !ok || p == "" {
		t.Errorf("the credential was removed by another account's revoke (%q, %v)", p, ok)
	}
	if p, ok := d.AuthenticateProtocol("u@acme.test", laptop); !ok || p == "" {
		t.Errorf("sibling credential stopped working (%q, %v)", p, ok)
	}
}

// TestTheListingIsNewestFirst keeps the settings page showing the credential the
// user just made at the top, where they look for it.
func TestTheListingIsNewestFirst(t *testing.T) {
	d := twoAccounts(t)
	phone, laptop, phoneID := twoCredentials(t, d)
	wantEq(t, "the two secrets differ", phone != laptop, true)
	list, err := d.ListAppPasswords("u@acme.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "Laptop" || list[1].Name != "Phone" {
		t.Fatalf("listing = %+v", list)
	}
	wantEq(t, "the older row is the phone's", list[1].ID, phoneID)
}

// TestALongNonASCIINameIsStored names a credential the way a Turkish user labels
// a device. The name column holds 64 bytes; a cut inside a character made
// MariaDB refuse the row, so minting failed for that user.
func TestALongNonASCIINameIsStored(t *testing.T) {
	d := twoAccounts(t)
	secret, err := d.CreateAppPassword("u@acme.test", "x"+strings.Repeat("ş", 40))
	mustNoErr(t, "mint a credential with a long non-ASCII name", err)
	wantEq(t, "a secret was shown", secret != "", true)
	list, err := d.ListAppPasswords("u@acme.test")
	mustNoErr(t, "list the credentials", err)
	if len(list) != 1 {
		t.Fatalf("listing = %+v, want the one credential", list)
	}
	wantEq(t, "the name is cut on a character", list[0].Name, "x"+strings.Repeat("ş", 31))
}

// TestACredentialIsAcceptedAsItWasShown keeps a user who typed it into a client
// by hand from being defeated by capitalisation or the spaces it is displayed in.
func TestACredentialIsAcceptedAsItWasShown(t *testing.T) {
	d := twoAccounts(t)
	secret, err := d.CreateAppPassword("u@acme.test", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{
		secret,
		secret[:4] + " " + secret[4:],
		" " + secret + " ",
	} {
		if p, ok := d.AuthenticateProtocol("u@acme.test", form); !ok || p == "" {
			t.Errorf("the credential was refused as %q (%q, %v)", form, p, ok)
		}
	}
	if p, ok := d.AuthenticateProtocol("u@acme.test", ""); ok || p != "" {
		t.Errorf("an empty credential was accepted (%q, %v)", p, ok)
	}
}

// TestTheCredentialListIsBounded keeps an account from accumulating an unbounded
// set of ways in that its owner has stopped reading.
func TestTheCredentialListIsBounded(t *testing.T) {
	d := twoAccounts(t)
	for i := range MaxAppPasswords {
		secret, err := d.CreateAppPassword("u@acme.test", "client")
		if err != nil || secret == "" {
			t.Fatalf("mint %d: secret=%q err=%v", i, secret, err)
		}
	}
	if secret, err := d.CreateAppPassword("u@acme.test", "one too many"); !errors.Is(err, ErrTooManyAppPasswords) || secret != "" {
		t.Errorf("mint past the cap = (%q, %v), want empty and ErrTooManyAppPasswords", secret, err)
	}
	list, err := d.ListAppPasswords("u@acme.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != MaxAppPasswords {
		t.Errorf("credentials after the refused mint = %d, want %d", len(list), MaxAppPasswords)
	}
}

// TestConcurrentMintsStayWithinTheCap races mints at an account one short of the
// cap. The count and the insert must be one step, or every racer reads 19 and
// all of them insert.
func TestConcurrentMintsStayWithinTheCap(t *testing.T) {
	d := twoAccounts(t)
	// One race rarely interleaves; many rounds make an unlocked count fail every run.
	for round := range 20 {
		left := countRows(t, d.db, "app passwords", `SELECT COUNT(*) FROM app_passwords`)
		execRows(t, "clear the app passwords", left)(d.db.Exec(`DELETE FROM app_passwords`))
		for i := range MaxAppPasswords - 1 {
			secret, err := d.CreateAppPassword("u@acme.test", "client")
			if err != nil || secret == "" {
				t.Fatalf("round %d mint %d: secret=%q err=%v", round, i, secret, err)
			}
		}
		raceMintsAtTheCap(t, d)
	}
}

// raceMintsAtTheCap starts many mints at once against an account one short of
// the cap and requires exactly one to succeed.
func raceOneMint(start *sync.WaitGroup, d *SQLDirectory, errs chan<- error) {
	start.Wait()
	secret, err := d.CreateAppPassword("u@acme.test", "racer")
	if err == nil && secret == "" {
		errs <- errors.New("CreateAppPassword succeeded with empty secret")
		return
	}
	if err != nil && secret != "" {
		errs <- errors.New("CreateAppPassword returned a secret on error")
		return
	}
	errs <- err
}

func raceMintsAtTheCap(t *testing.T, d *SQLDirectory) {
	t.Helper()
	const racers = 50
	errs := make(chan error, racers)
	var start sync.WaitGroup
	start.Add(1)
	for range racers {
		go raceOneMint(&start, d, errs)
	}
	start.Done()
	minted := 0
	for range racers {
		err := <-errs
		if err == nil {
			minted++
		} else if !errors.Is(err, ErrTooManyAppPasswords) {
			t.Errorf("a racing mint failed with %v", err)
		}
	}
	list, err := d.ListAppPasswords("u@acme.test")
	mustNoErr(t, "ListAppPasswords", err)
	wantEq(t, "racing mints succeeded", minted, 1)
	wantEq(t, "stored credentials after race", len(list), MaxAppPasswords)
}

// The callers type-assert to these interfaces; the compiler checks that
// SQLDirectory still satisfies them.
var (
	_ AppPasswordStore    = (*SQLDirectory)(nil)
	_ ClientAuthenticator = (*SQLDirectory)(nil)
)

// TestAuthenticateClientFallsBackWithoutTheCapability keeps a directory that has
// no app passwords working exactly as it did, which is what every test double in
// the protocol packages is.
func TestAuthenticateClientFallsBackWithoutTheCapability(t *testing.T) {
	accs := StaticAccounts{"a@acme.test": {Password: "password1", MailboxPath: "/tmp/a"}}
	if path, ok := AuthenticateClient(accs, "a@acme.test", "password1"); !ok || path != "/tmp/a" {
		t.Errorf("AuthenticateClient = %q, %v", path, ok)
	}
	if p, ok := AuthenticateClient(accs, "a@acme.test", "wrong"); ok || p != "" {
		t.Errorf("a wrong password was accepted (%q, %v)", p, ok)
	}
}

// TestEnrollingClosesTheAccountPasswordForEveryLoginName: a password login
// resolves an alias and an altname to the account, so the second factor must too.
// Otherwise an attacker holding the account password signs in to IMAP with the
// alias instead of the username and the second factor protects nothing.
func TestEnrollingClosesTheAccountPasswordForEveryLoginName(t *testing.T) {
	d := twoAccounts(t)
	mustNoErr(t, "CreateAlias", d.CreateAlias("sales@acme.test", "u@acme.test"))
	found, err := d.SetAltnames("u@acme.test", []string{"uu@acme.test"})
	mustNoErr(t, "SetAltnames", err)
	wantEq(t, "SetAltnames found user", found, true)
	for _, name := range []string{"sales@acme.test", "uu@acme.test"} {
		wantAuthProto(t, d, name, "password1", true)
	}

	enableSecondFactor(t, d, "u@acme.test")

	for _, name := range []string{"u@acme.test", "sales@acme.test", "uu@acme.test", "SALES@acme.test "} {
		wantAuthProto(t, d, name, "password1", false)
	}
	secret, err := d.CreateAppPassword("u@acme.test", "Phone")
	mustNoErr(t, "CreateAppPassword", err)
	wantAuthProto(t, d, "sales@acme.test", secret, true)
}

// TestAppPasswordLastUsedFailureIsLogged proves a failing last_used_at update
// does not break authentication and records a directory warning event.
func TestAppPasswordLastUsedFailureIsLogged(t *testing.T) {
	d := twoAccounts(t)
	sink := &captureSink{}
	d.SetLogger(logging.New(sink))

	secret, err := d.CreateAppPassword("u@acme.test", "Phone")
	mustNoErr(t, "mint app password", err)
	installFailTrigger(t, d.db, "test_app_pw_upd_fail", "UPDATE", "app_passwords")

	path, ok := d.AuthenticateAppPassword("u@acme.test", secret)
	if !ok || path == "" {
		t.Fatalf("AuthenticateAppPassword = (%q, %v), want login to succeed despite failed last_used_at write", path, ok)
	}
	e := wantWarnEvent(t, sink, "directory.apppassword_last_used.fail")
	if _, hasID := e.Fields["id"]; !hasID {
		t.Errorf("event missing id field: %+v", e.Fields)
	}
}
