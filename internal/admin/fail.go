package admin

import (
	"net/http"

	"hermex/internal/logging"
)

// fail logs the full internal error server-side and answers the client with msg
// alone, so raw driver and OS error text never reaches an admin-panel caller. The
// errors these handlers surface come straight out of the directory: a MariaDB error
// naming tables, columns and constraints, or an os.MkdirAll error naming a mailbox
// path on disk. An admin-panel account is not necessarily a system administrator, so
// that text is a schema and filesystem map handed to a scoped account.
//
// msg is a fixed string the handler chooses; it must never be built from err.
// serve.New's logMiddleware already emits a per-request event carrying method, path,
// status and RemoteAddr, so this records the failing error text alone. A 5xx logs at
// error level, a 4xx (client fault) at warn. It mirrors internal/dav's davError and
// internal/ews's soapFault.
func (s *Server) fail(w http.ResponseWriter, msg string, err error, status int) {
	level := logging.LevelError
	if status < http.StatusInternalServerError {
		level = logging.LevelWarn
	}
	s.logger.Emit(logging.Event{
		Level:     level,
		Subsystem: logging.Admin,
		Name:      "request.fail",
		Fields:    logging.Fields{"status": status},
		Err:       err.Error(),
	})
	http.Error(w, msg, status)
}

// notice is fail's counterpart for the HTMX panel path, which answers a failure by
// re-rendering a panel with a notice string rather than writing an HTTP error. It
// records the full error and returns msg, so the operator sees what failed while the
// driver text naming tables and constraints, and the store text naming mailbox paths
// on disk, stay server-side.
//
// msg is a fixed string the handler chooses; it must never be built from err.
func (s *Server) notice(msg string, err error) string {
	s.logger.Emit(logging.Event{
		Level:     logging.LevelError,
		Subsystem: logging.Admin,
		Name:      "panel.fail",
		Err:       err.Error(),
	})
	return msg
}

// panelNotice is the message a re-rendered settings panel shows above its form:
// what was saved, or why nothing was. Kind is the status class it renders with
// (the "notice" template). The page announces an "ok" notice as a toast and then
// removes it, while a "warn" or "error" notice also stays beside the form, so a
// refused or failed save never reads as a saved one.
type panelNotice struct {
	Text string
	Kind string
}

// okNotice acknowledges a change that took effect.
func okNotice(text string) panelNotice { return panelNotice{Text: text, Kind: "ok"} }

// warnNotice reports a change that took effect with a caveat, or a requested
// action that found nothing to act on.
func warnNotice(text string) panelNotice { return panelNotice{Text: text, Kind: "warn"} }

// errorNotice reports a change that was refused, and why.
func errorNotice(text string) panelNotice { return panelNotice{Text: text, Kind: "error"} }

// failNotice is notice for a panel: it records err server-side and reports msg
// as a failed change.
func (s *Server) failNotice(msg string, err error) panelNotice {
	return errorNotice(s.notice(msg, err))
}

// readFailures maps a section of a page to the message that replaces its form
// when the section's stored values could not be read. That form would show empty
// or default values, and saving it would overwrite what is stored, so the section
// shows the failure instead. A template reads it as
// {{with index .ReadFailed "section"}}.
type readFailures map[string]string

// noteRead records a failed read of what under section and reports whether the
// read succeeded. The message is built from what alone; err is recorded
// server-side, as notice does.
func (s *Server) noteRead(failed readFailures, section, what string, err error) bool {
	if err == nil {
		return true
	}
	failed[section] = s.notice("Could not read "+what+". The form is hidden so a save cannot overwrite it.", err)
	return false
}

// listFailure returns the message a table shows in place of its rows when the list
// could not be read, or "" when the read succeeded. The table must not show a failed
// read as an empty list; err is recorded server-side, as notice does.
func (s *Server) listFailure(what string, err error) string {
	if err == nil {
		return ""
	}
	return s.notice("Could not read "+what+".", err)
}
