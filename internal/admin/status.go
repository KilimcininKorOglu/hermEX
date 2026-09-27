package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"hermex/internal/directory"
)

// healthResult is one daemon's probe outcome rendered on the Live status page.
type healthResult struct {
	Name      string
	URL       string
	Status    string // "Up", "Degraded", or "Down"
	LatencyMS int64
	Version   string
	Uptime    int64
	Err       string
}

// UptimeText renders Uptime in its two largest units.
func (h healthResult) UptimeText() string {
	return durationMsg(h.Uptime)
}

// probeHealth reads the stored targets and probes each concurrently with a short
// timeout, classifying each daemon as Up (200 and healthy), Degraded (reachable
// but a readiness check failed), or Down (unreachable). Results keep target
// order. The targets are read on every call, so a target added or removed in the
// panel is probed from the next refresh without a restart.
func (s *Server) probeHealth(ctx context.Context) ([]healthResult, error) {
	targets, err := s.dir.ListHealthTargets()
	if err != nil {
		return nil, err
	}
	out := make([]healthResult, len(targets))
	client := &http.Client{Timeout: 3 * time.Second}
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t directory.HealthTarget) {
			defer wg.Done()
			out[i] = probeOne(ctx, client, t)
		}(i, t)
	}
	wg.Wait()
	return out, nil
}

// healthStatus is the JSON a daemon's /healthz answers.
type healthStatus struct {
	Version string            `json:"version"`
	Uptime  int64             `json:"uptime_seconds"`
	OK      bool              `json:"ok"`
	Checks  map[string]string `json:"checks"`
}

// probeOne performs a single /healthz GET and classifies the response. Its error
// text is shown in the monitor rather than sanitized: the target URL is the
// operator's own configuration, and the reason a probe failed ("connection refused",
// "no such host", the readiness check that did not pass) is the signal the monitor
// exists to report, not a server internal.
func probeOne(ctx context.Context, client *http.Client, t directory.HealthTarget) healthResult {
	r := healthResult{Name: t.Name, URL: t.URL, Status: "Down"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	start := time.Now()
	resp, err := client.Do(req)
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer resp.Body.Close()
	var st healthStatus
	decodeErr := json.NewDecoder(resp.Body).Decode(&st)
	r.Version, r.Uptime = st.Version, st.Uptime
	if resp.StatusCode == http.StatusOK && st.OK {
		r.Status = "Up"
		return r
	}
	r.Status = "Degraded"
	r.Err = degradedReason(resp, st, decodeErr)
	return r
}

// degradedReason explains a reachable daemon that is not healthy: the readiness
// checks that failed, or, when the answer names none, its HTTP status and why its
// body could not be read.
func degradedReason(resp *http.Response, st healthStatus, decodeErr error) string {
	if failed := failedChecks(st.Checks); failed != "" {
		return failed
	}
	if decodeErr != nil {
		return resp.Status + ": " + decodeErr.Error()
	}
	return resp.Status
}

// failedChecks lists the readiness checks that did not answer "ok", as
// "name: error" in name order, or "" when none failed.
func failedChecks(checks map[string]string) string {
	var failed []string
	for name, result := range checks {
		if result != "ok" {
			failed = append(failed, name+": "+result)
		}
	}
	sort.Strings(failed)
	return strings.Join(failed, "; ")
}

// statusData is the Live status table's model: the probe results, or the
// message that replaces them when the targets could not be read.
func (s *Server) statusData(r *http.Request) map[string]any {
	results, err := s.probeHealth(r.Context())
	return map[string]any{"Results": results, "ResultsError": s.listFailure("what.healthTargets", err)}
}

// handleUIStatus renders the Live status page (system admins; read-only tier may
// view), probing every stored daemon and listing the targets for editing.
func (s *Server) handleUIStatus(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	data := s.statusData(r)
	data["Nav"], data["CSRF"] = "status", csrfCookieValue(r)
	s.addHealthTargets(data)
	s.render(w, r, "status.html", data)
}

// handleUIStatusPanel renders just the status table (the page polls it to refresh
// live).
func (s *Server) handleUIStatusPanel(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, r, "status-panel", s.statusData(r))
}

// addHealthTargets adds the stored targets, or the message that replaces their
// table, to a page model.
func (s *Server) addHealthTargets(data map[string]any) {
	targets, err := s.dir.ListHealthTargets()
	data["Targets"], data["TargetsError"] = targets, s.listFailure("what.healthTargets", err)
}

// renderTargetsPanel re-renders the targets table with an optional error and
// asks the page to refresh the status table, so the change shows in both at once.
func (s *Server) renderTargetsPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	data := map[string]any{"CSRF": csrfCookieValue(r), "Error": errMsg}
	s.addHealthTargets(data)
	w.Header().Set("HX-Trigger", "health-targets-changed")
	s.render(w, r, "status-targets", data)
}

// handleUIAddHealthTarget stores a target from the panel form (system
// administrators with write authority) and returns the refreshed targets table.
func (s *Server) handleUIAddHealthTarget(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	t := directory.HealthTarget{Name: r.PostFormValue("name"), URL: r.PostFormValue("url")}
	errMsg := ""
	_, err := s.dir.AddHealthTarget(t)
	switch {
	case errors.Is(err, directory.ErrInvalidHealthTarget):
		errMsg = "status.targetInvalid"
	case errors.Is(err, directory.ErrHealthTargetExists):
		errMsg = "status.targetExists"
	case err != nil:
		errMsg = s.notice("status.targetAddFailed", err)
	}
	s.renderTargetsPanel(w, r, errMsg)
}

// handleUIDeleteHealthTarget removes a target and returns the refreshed targets
// table. A target that is already gone is reported, not treated as a failure.
func (s *Server) handleUIDeleteHealthTarget(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	errMsg := ""
	if err != nil {
		errMsg = "status.targetGone"
	} else if gone, derr := s.dir.DeleteHealthTarget(id); derr != nil {
		errMsg = s.notice("status.targetDeleteFailed", derr)
	} else if !gone {
		errMsg = "status.targetGone"
	}
	s.renderTargetsPanel(w, r, errMsg)
}

// handleGetStatus returns the probe results as JSON (system admins).
func (s *Server) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	res, err := s.probeHealth(r.Context())
	if err != nil {
		s.fail(w, "could not read the health targets", err, http.StatusInternalServerError)
		return
	}
	if res == nil {
		res = []healthResult{}
	}
	writeJSON(w, res)
}
