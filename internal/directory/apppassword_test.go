package directory

import (
	"errors"
	"path/filepath"
	"testing"

	"hermex/internal/totp"
)

// twoAccounts provisions two mailboxes, which is what proves a credential is
// bound to the account that minted it.
func twoAccounts(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	if _, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test")); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"u@acme.test", "v@acme.test"} {
		if _, err := d.CreateUser(u, "pw", filepath.Join(root, u)); err != nil {
			t.Fatal(err)
		}
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
	_, hashes, err := totp.NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
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
	if want, _ := d.Authenticate("u@acme.test", "pw"); path != want {
		t.Errorf("the credential resolved to %q, want the account's own mailbox %q", path, want)
	}
	// It is the account's, not anyone else's.
	if _, ok := d.AuthenticateProtocol("v@acme.test", secret); ok {
		t.Error("one account's credential signed in another")
	}
	if _, ok := d.AuthenticateProtocol("u@acme.test", "not-a-credential"); ok {
		t.Error("an unknown credential was accepted")
	}
}

// TestEnrollingClosesTheAccountPasswordOnTheProtocols is the security property
// the whole slice exists for. A second factor that leaves the account password
// working on IMAP protects only the web surfaces, and an attacker holding the
// password just uses IMAP instead.
func TestEnrollingClosesTheAccountPasswordOnTheProtocols(t *testing.T) {
	d := twoAccounts(t)

	// Before enrolling, the account password is what a mail client uses.
	if _, ok := d.AuthenticateProtocol("u@acme.test", "pw"); !ok {
		t.Fatal("an unenrolled account could not sign in with its own password")
	}

	enableSecondFactor(t, d, "u@acme.test")

	if _, ok := d.AuthenticateProtocol("u@acme.test", "pw"); ok {
		t.Error("the account password still works on a client protocol after enrolling")
	}
	// The account password is still the account password everywhere a second
	// factor CAN be asked for: the webmail and panel logins go through
	// Authenticate, which is unchanged.
	if _, ok := d.Authenticate("u@acme.test", "pw"); !ok {
		t.Error("enrolling broke the account password on the web login")
	}
	// And the other account is untouched.
	if _, ok := d.AuthenticateProtocol("v@acme.test", "pw"); !ok {
		t.Error("enrolling one account closed another account's protocol login")
	}

	secret, err := d.CreateAppPassword("u@acme.test", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.AuthenticateProtocol("u@acme.test", secret); !ok {
		t.Error("the enrolled account cannot reach its mail from a client at all")
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
	if _, ok := d.AuthenticateProtocol("u@acme.test", phone); ok {
		t.Error("a revoked credential still signs in")
	}
	if _, ok := d.AuthenticateProtocol("u@acme.test", laptop); !ok {
		t.Error("revoking one credential broke another")
	}
}

// TestOneAccountCannotRevokeAnothersCredential is the scoping: the id alone must
// not be enough, or anyone could work through the id space.
func TestOneAccountCannotRevokeAnothersCredential(t *testing.T) {
	d := twoAccounts(t)
	phone, _, phoneID := twoCredentials(t, d)

	if ok, err := d.DeleteAppPassword("v@acme.test", phoneID); err != nil || ok {
		t.Fatalf("another account revoked the credential: ok %v err %v", ok, err)
	}
	if _, ok := d.AuthenticateProtocol("u@acme.test", phone); !ok {
		t.Error("the credential was removed by another account's revoke")
	}
}

// TestTheListingIsNewestFirst keeps the settings page showing the credential the
// user just made at the top, where they look for it.
func TestTheListingIsNewestFirst(t *testing.T) {
	d := twoAccounts(t)
	twoCredentials(t, d)
	list, err := d.ListAppPasswords("u@acme.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "Laptop" || list[1].Name != "Phone" {
		t.Errorf("listing = %+v", list)
	}
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
		if _, ok := d.AuthenticateProtocol("u@acme.test", form); !ok {
			t.Errorf("the credential was refused as %q", form)
		}
	}
	if _, ok := d.AuthenticateProtocol("u@acme.test", ""); ok {
		t.Error("an empty credential was accepted")
	}
}

// TestTheCredentialListIsBounded keeps an account from accumulating an unbounded
// set of ways in that its owner has stopped reading.
func TestTheCredentialListIsBounded(t *testing.T) {
	d := twoAccounts(t)
	for i := range MaxAppPasswords {
		if _, err := d.CreateAppPassword("u@acme.test", "client"); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	if _, err := d.CreateAppPassword("u@acme.test", "one too many"); !errors.Is(err, ErrTooManyAppPasswords) {
		t.Errorf("mint past the cap = %v, want ErrTooManyAppPasswords", err)
	}
}

// TestSQLDirectorySatisfiesAppPasswordStore is what the callers type-assert to.
func TestSQLDirectorySatisfiesAppPasswordStore(t *testing.T) {
	var _ AppPasswordStore = (*SQLDirectory)(nil)
	var _ ClientAuthenticator = (*SQLDirectory)(nil)
}

// TestAuthenticateClientFallsBackWithoutTheCapability keeps a directory that has
// no app passwords working exactly as it did, which is what every test double in
// the protocol packages is.
func TestAuthenticateClientFallsBackWithoutTheCapability(t *testing.T) {
	accs := StaticAccounts{"a@acme.test": {Password: "pw", MailboxPath: "/tmp/a"}}
	if path, ok := AuthenticateClient(accs, "a@acme.test", "pw"); !ok || path != "/tmp/a" {
		t.Errorf("AuthenticateClient = %q, %v", path, ok)
	}
	if _, ok := AuthenticateClient(accs, "a@acme.test", "wrong"); ok {
		t.Error("a wrong password was accepted")
	}
}
