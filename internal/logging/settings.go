package logging

// SettingsReadFailed records a daemon's failed read of an operator-editable
// settings row. Every settings family polls its row about once a minute and, on a
// read failure, deliberately leaves the running tuning in place so the mail path
// keeps serving. That choice is right, but it makes a lasting directory outage
// invisible: the limits an operator configured quietly stop tracking the row. This
// is the one call that puts the failure in the central store instead of only on
// stderr.
//
// One event name across every family, with the family in a field, so an operator
// queries all of them at once. daemon names the process, family names the settings
// row ("login-lockout", "http-rate-limit", "size-limits", ...), and detail says what
// was kept instead.
func SettingsReadFailed(l *Logger, daemon, family, detail string, err error) {
	f := Fields{"daemon": daemon, "settings": family, "detail": detail}
	if err != nil {
		f["error"] = err.Error()
	}
	l.Warn(System, "settings.read.fail", f)
}
