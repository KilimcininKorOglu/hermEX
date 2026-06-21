// Package health serves a daemon's liveness and readiness over a small HTTP
// listener so the admin Live status monitor can report each daemon as up, down,
// or degraded. Every daemon mounts Handler on its own health address; the admin
// probes each daemon's /healthz and renders the result.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"hermex/internal/lifecycle"
)

// Check is an optional readiness probe — typically a database ping — that reports
// whether a dependency the daemon needs is reachable. A nil error means healthy.
type Check struct {
	Name  string
	Probe func(ctx context.Context) error
}

// Status is the JSON a daemon reports at /healthz: its identity, how long it has
// been running, whether every readiness check passed, and per-check detail.
type Status struct {
	Service string            `json:"service"`
	Version string            `json:"version"`
	Uptime  int64             `json:"uptime_seconds"`
	OK      bool              `json:"ok"`
	Checks  map[string]string `json:"checks,omitempty"`
}

// Handler serves GET /healthz for one daemon. It answers 200 when every
// readiness check passes and 503 when any fails, so the monitor distinguishes a
// healthy daemon from a live-but-degraded one (for example, listener up but the
// directory database unreachable). started anchors the reported uptime.
func Handler(service, version string, started time.Time, checks ...Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		st := Status{
			Service: service,
			Version: version,
			Uptime:  int64(time.Since(started).Seconds()),
			OK:      true,
		}
		if len(checks) > 0 {
			st.Checks = make(map[string]string, len(checks))
			for _, c := range checks {
				if err := c.Probe(r.Context()); err != nil {
					st.OK = false
					st.Checks[c.Name] = err.Error()
				} else {
					st.Checks[c.Name] = "ok"
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if !st.OK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(&st)
	})
	return mux
}

// component runs the health Handler on its own HTTP listener as a daemon
// lifecycle component (it satisfies lifecycle.Component structurally, so this
// package need not import lifecycle).
type component struct {
	srv *http.Server
}

// Component wraps the health Handler in a daemon component listening on addr. A
// daemon adds it to the components it hands to lifecycle.Run; ListenAndServe
// returns http.ErrServerClosed after Shutdown, which the lifecycle runner treats
// as the normal stop path.
func Component(addr string, h http.Handler) *component {
	return &component{srv: &http.Server{Addr: addr, Handler: h}}
}

// Start runs the health listener until Shutdown is called.
func (c *component) Start() error { return c.srv.ListenAndServe() }

// Shutdown gracefully stops the health listener within ctx's deadline.
func (c *component) Shutdown(ctx context.Context) error { return c.srv.Shutdown(ctx) }

// Components is the daemon-main convenience: it returns the health component for
// service on addr when addr is non-empty, or nil when health is disabled, so a
// main can append it to its lifecycle components in one line. Uptime is measured
// from this call, which a main makes at startup. checks are the daemon's
// readiness probes (typically a directory database ping).
func Components(addr, service string, checks ...Check) []lifecycle.Component {
	if addr == "" {
		return nil
	}
	return []lifecycle.Component{Component(addr, Handler(service, "", time.Now(), checks...))}
}
