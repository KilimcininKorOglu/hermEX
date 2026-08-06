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
