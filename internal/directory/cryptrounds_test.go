package directory

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

// TestNewHashesCarryTheCurrentWorkFactor is the defect itself. The scheme's own
// default is 5000 rounds, which a commodity GPU strips off a leaked database in
// minutes, and the hash generator took that default by passing no salt at all.
func TestNewHashesCarryTheCurrentWorkFactor(t *testing.T) {
	h, err := sqlCryptNewHash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$6$rounds=") {
		t.Fatalf("hash %q carries no rounds parameter, so it was made at the scheme default", h)
	}
	rounds, ok := cryptRoundsOf(h)
	if !ok || rounds != cryptRounds {
		t.Errorf("hash was made at %d rounds (ok=%v), want %d", rounds, ok, cryptRounds)
	}
	if !sqlCryptVerify("correct horse battery staple", h) {
		t.Error("the hash does not verify its own password")
	}
	if sqlCryptVerify("wrong password", h) {
		t.Error("the hash verifies a wrong password")
	}
}

// TestDecoyMatchesTheCurrentWorkFactor guards the timing oracle the decoy exists to
// close. A login naming no usable account is verified against the decoy so failure
// costs what a wrong password costs. Raising the real factor without raising the
// decoy makes the decoy a fraction of the cost, and the difference enumerates which
// addresses exist.
func TestDecoyMatchesTheCurrentWorkFactor(t *testing.T) {
	rounds, ok := cryptRoundsOf(decoyPasswordHash)
	if !ok {
		t.Fatalf("the decoy is not a sha512-crypt hash: %q", decoyPasswordHash)
	}
	if rounds != cryptRounds {
		t.Errorf("the decoy costs %d rounds against a real hash's %d, so a failed lookup is measurably cheaper than a wrong password", rounds, cryptRounds)
	}
}

// TestCryptRoundsOfReadsStoredHashes pins the parser the upgrade decision rests on.
func TestCryptRoundsOfReadsStoredHashes(t *testing.T) {
	for _, tc := range []struct {
		name, stored string
		want         int
		wantOK       bool
	}{
		{"explicit rounds", "$6$rounds=600000$salt$hash", 600000, true},
		{"no rounds parameter", "$6$salt$hash", sha512_crypt.RoundsDefault, true},
		{"md5-crypt", "$1$salt$hash", 0, false},
		{"empty", "", 0, false},
		{"unparseable rounds", "$6$rounds=abc$salt$hash", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := cryptRoundsOf(tc.stored)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("cryptRoundsOf(%q) = (%d, %v), want (%d, %v)", tc.stored, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestNeedsRehashCoversEveryWeakerHash proves nothing weaker than the current
// factor is treated as good enough, including the md5-crypt hashes the verifier
// still accepts for interoperability.
func TestNeedsRehashCoversEveryWeakerHash(t *testing.T) {
	for _, stored := range []string{
		"$6$salt$hash",               // the 5000-round default
		"$6$rounds=5000$salt$hash",   // the same, stated
		"$6$rounds=210000$salt$hash", // raised, but not to here
		"$1$salt$hash",               // md5-crypt
		"",                           // no hash at all
	} {
		if !needsRehash(stored) {
			t.Errorf("%q is treated as strong enough", stored)
		}
	}
	for _, stored := range []string{
		"$6$rounds=600000$salt$hash",
		"$6$rounds=1000000$salt$hash", // an operator who went further keeps it
	} {
		if needsRehash(stored) {
			t.Errorf("%q would be needlessly re-hashed", stored)
		}
	}
}

// TestLoginUpgradesAnOldHash is the half that reaches the people who are already
// users. The factor is baked into each hash, so raising it alone changes nothing
// for an existing account until its password is set again, which for most accounts
// is never.
func TestLoginUpgradesAnOldHash(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := d.CreateDomain("acme.test", filepath.Join(t.TempDir(), "dom")); err != nil {
		t.Fatal(err)
	}
	const user, pass = "alice@acme.test", "correct horse battery staple"
	if _, err := d.CreateUser(user, pass, filepath.Join(t.TempDir(), "alice")); err != nil {
		t.Fatal(err)
	}

	// Put the account back on a hash made at the old default, the state every
	// account created before this change is in.
	weak, err := sha512_crypt.New().Generate([]byte(pass), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(weak, "$6$") || strings.Contains(weak, "rounds=") {
		t.Fatalf("the fixture is not an old-style hash: %q", weak)
	}
	if _, err := db.Exec(`UPDATE users SET password = ? WHERE username = ?`, weak, user); err != nil {
		t.Fatal(err)
	}

	if _, ok := d.Authenticate(user, pass); !ok {
		t.Fatal("the old hash no longer authenticates, so raising the factor broke existing logins")
	}

	var stored string
	if err := db.QueryRow(`SELECT password FROM users WHERE username = ?`, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if needsRehash(stored) {
		t.Errorf("the stored hash is still below the current factor after a successful login: %q", stored)
	}
	// The upgrade must not have changed what the password is.
	if _, ok := d.Authenticate(user, pass); !ok {
		t.Error("the re-hashed account no longer accepts its own password")
	}
	if _, ok := d.Authenticate(user, "wrong password"); ok {
		t.Error("the re-hashed account accepts a wrong password")
	}
}

// TestWrongPasswordDoesNotUpgrade proves the re-hash is gated on a successful
// verify. Re-storing on a failed login would be a write an unauthenticated caller
// can trigger at will, and would need a plaintext nobody proved.
func TestWrongPasswordDoesNotUpgrade(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := d.CreateDomain("acme.test", filepath.Join(t.TempDir(), "dom")); err != nil {
		t.Fatal(err)
	}
	const user, pass = "bob@acme.test", "correct horse battery staple"
	if _, err := d.CreateUser(user, pass, filepath.Join(t.TempDir(), "bob")); err != nil {
		t.Fatal(err)
	}
	weak, err := sha512_crypt.New().Generate([]byte(pass), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET password = ? WHERE username = ?`, weak, user); err != nil {
		t.Fatal(err)
	}

	if _, ok := d.Authenticate(user, "not the password"); ok {
		t.Fatal("a wrong password authenticated")
	}
	var stored string
	if err := db.QueryRow(`SELECT password FROM users WHERE username = ?`, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != weak {
		t.Error("a failed login rewrote the stored hash")
	}
}

// TestUpgradeIsOncePerAccount proves a logged-in account is not re-hashed on every
// request. The re-hash costs a full hash generation, which on the login path would
// otherwise be paid forever rather than once.
func TestUpgradeIsOncePerAccount(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := d.CreateDomain("acme.test", filepath.Join(t.TempDir(), "dom")); err != nil {
		t.Fatal(err)
	}
	const user, pass = "carol@acme.test", "correct horse battery staple"
	if _, err := d.CreateUser(user, pass, filepath.Join(t.TempDir(), "carol")); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Authenticate(user, pass); !ok {
		t.Fatal("login failed")
	}
	var first string
	if err := db.QueryRow(`SELECT password FROM users WHERE username = ?`, user).Scan(&first); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, ok := d.Authenticate(user, pass); !ok {
		t.Fatal("second login failed")
	}
	elapsed := time.Since(start)

	var second string
	if err := db.QueryRow(`SELECT password FROM users WHERE username = ?`, user).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Error("an already-current hash was rewritten, so every login pays for a re-hash")
	}
	// One verify, not a verify plus a generate. The margin is generous so the
	// assertion is about the missing second hash, not about absolute speed.
	if budget := 3 * hashCost(t); elapsed > budget {
		t.Errorf("a repeat login took %s against a single hash's %s, which is the re-hash running again", elapsed, budget)
	}
}

// hashCost measures one hash generation on this machine, so the assertion above
// scales with the host rather than with a number picked on one.
func hashCost(t *testing.T) time.Duration {
	t.Helper()
	start := time.Now()
	if _, err := sqlCryptNewHash("timing sample"); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}
