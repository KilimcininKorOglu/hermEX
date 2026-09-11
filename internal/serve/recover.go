package serve

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"

	"hermex/internal/logging"
)

// panicStackBytes bounds the stack trace a recovered panic records. A trace long
// enough to matter fits well inside it, and a bounded copy keeps one panic from
// writing a megabyte into the log store.
const panicStackBytes = 8 << 10

// recoverMiddleware turns a panicking handler into a 500 and an operator-visible
// event, instead of a request that dies silently.
//
// net/http already recovers a handler panic, so the process survives either way.
// What it does NOT do is tell the operator through the central log or answer the
// client: it writes the trace to the server's own ErrorLog and drops the
// connection, so the request appears in no access log and the client sees a reset.
// This records the panic where every other failure is recorded, and answers the
// status the client can act on.
//
// http.ErrAbortHandler is re-panicked untouched, because it is the documented way a
// handler asks net/http to drop the connection without logging anything.
func recoverMiddleware(h http.Handler, logger *logging.Logger, subsystem logging.Subsystem) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if errors.Is(asError(v), http.ErrAbortHandler) {
				panic(v)
			}
			logPanic(logger, subsystem, r, v)
			// The handler may already have written a status. WriteHeader then logs a
			// duplicate-header warning and changes nothing, which is the right outcome:
			// the client keeps the partial response it was given.
			w.WriteHeader(http.StatusInternalServerError)
		}()
		h.ServeHTTP(w, r)
	})
}

// asError renders a recovered value as an error so a sentinel comparison works.
func asError(v any) error {
	if err, ok := v.(error); ok {
		return err
	}
	return fmt.Errorf("%v", v)
}

// logPanic records one recovered handler panic with the stack that caused it.
func logPanic(logger *logging.Logger, subsystem logging.Subsystem, r *http.Request, v any) {
	buf := make([]byte, panicStackBytes)
	buf = buf[:runtime.Stack(buf, false)]
	logger.Emit(logging.Event{
		Level:      logging.LevelError,
		Subsystem:  subsystem,
		Name:       "handler.panic",
		RemoteAddr: ClientAddr(r),
		Err:        fmt.Sprintf("%v", v),
		// The path is the request line's, never a header the client controls, so it
		// names the surface without carrying anything the caller chose to inject.
		Fields: logging.Fields{"method": r.Method, "path": r.URL.Path, "stack": string(buf)},
	})
}
