package main

import "testing"

// A plain reset performs both steps beside the password change, so the CLI
// answers a compromise the way the admin panel's reset does.
func TestSetPasswordDefaultsToTheFullReset(t *testing.T) {
	req, err := parseSetPassword([]string{"erin@hermex.test", "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if req.email != "erin@hermex.test" || req.password != "s3cret" {
		t.Fatalf("parsed %q / %q", req.email, req.password)
	}
	if !req.forceChange || !req.endSessions {
		t.Fatalf("forceChange=%t endSessions=%t, want both true", req.forceChange, req.endSessions)
	}
}

// Each option turns off its own step and leaves the other one on.
func TestSetPasswordOptionsTurnOffOneStepEach(t *testing.T) {
	for _, tc := range []struct {
		option                  string
		wantForce, wantSessions bool
	}{
		{"--no-force-change", false, true},
		{"--keep-sessions", true, false},
	} {
		req, err := parseSetPassword([]string{"erin@hermex.test", "s3cret", tc.option})
		if err != nil {
			t.Fatalf("%s: %v", tc.option, err)
		}
		if req.forceChange != tc.wantForce || req.endSessions != tc.wantSessions {
			t.Fatalf("%s: forceChange=%t endSessions=%t, want %t/%t",
				tc.option, req.forceChange, req.endSessions, tc.wantForce, tc.wantSessions)
		}
	}
}

// A mistyped option must not be read as the password. Without the guard this
// line parses as a two-argument command whose password is "--keep-session", so
// the account's password becomes the option text and the command reports success.
func TestSetPasswordRefusesAnUnknownOption(t *testing.T) {
	req, err := parseSetPassword([]string{"erin@hermex.test", "--keep-session"})
	if err == nil {
		t.Fatalf("a misspelled option was accepted as the password %q", req.password)
	}
}

// The address and the password are both required; a missing one must not leave
// the command reading an option as the password.
func TestSetPasswordNeedsBothArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"erin@hermex.test"}, {"erin@hermex.test", "a", "b"}} {
		if _, err := parseSetPassword(args); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
}
