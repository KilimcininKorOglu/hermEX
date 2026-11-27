package mta

import (
	"errors"
	"testing"

	"hermex/internal/logging"
)

// TestApplyInstallsTheStoredPrefix is the re-injection proof. A settings row is
// only operator-tunable when the consumer re-reads what the poll updates, so this
// drives the same Apply the poll calls and then reads the composed subject.
func TestApplyInstallsTheStoredPrefix(t *testing.T) {
	t.Cleanup(func() { SetAutoReplyPrefix("") })

	stored := AutoReplySettings{SubjectPrefix: "Otomatik yanıt"}
	read := func() (AutoReplySettings, bool, error) { return stored, true, nil }

	ApplyAutoReplySettings("test", nil, read)
	if got := composeAutoReplySubject("", "Q3 budget"); got != "Otomatik yanıt: Q3 budget" {
		t.Fatalf("subject = %q, want the stored prefix", got)
	}

	// The operator edits the row; the next poll must reach the reply, without a
	// restart.
	stored.SubjectPrefix = "Out of office"
	ApplyAutoReplySettings("test", nil, read)
	if got := composeAutoReplySubject("", "Q3 budget"); got != "Out of office: Q3 budget" {
		t.Errorf("subject = %q, want the edited prefix", got)
	}

	// Clearing the stored prefix restores the built-in default.
	stored.SubjectPrefix = ""
	ApplyAutoReplySettings("test", nil, read)
	if got := composeAutoReplySubject("", ""); got != DefaultAutoReplyPrefix {
		t.Errorf("empty stored prefix gave %q, want %q", got, DefaultAutoReplyPrefix)
	}
}

// TestApplyKeepsTheRunningPrefixOnAReadError covers the failure the operator
// never sees: a transient database error must not silently change the wording of
// every reply the deployment sends.
func TestApplyKeepsTheRunningPrefixOnAReadError(t *testing.T) {
	t.Cleanup(func() { SetAutoReplyPrefix("") })
	SetAutoReplyPrefix("Otomatik yanıt")
	sink := &captureSink{}

	ApplyAutoReplySettings("test", logging.New(sink), func() (AutoReplySettings, bool, error) {
		return AutoReplySettings{}, false, errors.New("database is down")
	})
	if got := composeAutoReplySubject("", ""); got != "Otomatik yanıt" {
		t.Errorf("subject = %q, want the last applied prefix", got)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if _, ok := findEvent(sink.events, "settings.read.fail"); !ok {
		t.Error("settings read failure was not logged")
	}
}

// TestApplyRestoresTheDefaultWhenNoRowExists keeps a deployment that has never
// opened the admin form on the wording every reply carried before the setting
// existed.
func TestApplyRestoresTheDefaultWhenNoRowExists(t *testing.T) {
	t.Cleanup(func() { SetAutoReplyPrefix("") })
	SetAutoReplyPrefix("Otomatik yanıt")

	ApplyAutoReplySettings("test", nil, func() (AutoReplySettings, bool, error) {
		return AutoReplySettings{}, false, nil
	})
	if got := composeAutoReplySubject("", ""); got != DefaultAutoReplyPrefix {
		t.Errorf("subject = %q, want %q", got, DefaultAutoReplyPrefix)
	}
}
