package admin

import "hermex/internal/logging"

// auditSettingChange records that an operator changed a security-relevant setting.
// The panel otherwise logs only failures, so a control that is safe by default (AV
// scanning, MTA-STS enforcement, TLS mode) could be weakened with no trace of who
// did it or the prior value. This is the positive audit event that answers "who
// turned this off, and when": it names the actor, the setting, and the before/after
// values carried in fields. The emit is best-effort and never blocks the save.
func (s *Server) auditSettingChange(actor, setting string, fields logging.Fields) {
	if fields == nil {
		fields = logging.Fields{}
	}
	fields["setting"] = setting
	s.logger.Emit(logging.Event{
		Level:     logging.LevelInfo,
		Subsystem: logging.Admin,
		Name:      "setting.change",
		User:      actor,
		Fields:    fields,
	})
}

// auditOld is the value an audit entry records as a setting's previous state: the
// stored value, or "unreadable" when reading it failed, so the entry never claims
// an empty previous value that was not stored.
func auditOld(v any, err error) any {
	if err != nil {
		return "unreadable"
	}
	return v
}
