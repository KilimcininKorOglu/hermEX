package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// HealthTarget names a daemon and the URL of its /healthz endpoint for the Live
// status monitor. cmd/admin populates the set from the configuration.
type HealthTarget struct {
	Name string
	URL  string
}

// SetHealthTargets configures the daemons the Live status page probes. It is a
// native daemon-health monitor: hermEX runs no nginx, so instead of an
// nginx-vhost status page the admin probes each daemon's own /healthz.
func (s *Server) SetHealthTargets(t []HealthTarget) { s.healthTargets = t }

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

// probeHealth probes every configured target concurrently with a short timeout,
// classifying each daemon as Up (200 and healthy), Degraded (reachable but a
// readiness check failed), or Down (unreachable). Results keep target order.
func (s *Server) probeHealth(ctx context.Context) []healthResult {
	out := make([]healthResult, len(s.healthTargets))
	client := &http.Client{Timeout: 3 * time.Second}
	var wg sync.WaitGroup
	for i, t := range s.healthTargets {
		wg.Add(1)
		go func(i int, t HealthTarget) {
			defer wg.Done()
			out[i] = probeOne(ctx, client, t)
		}(i, t)
	}
	wg.Wait()
	return out
}

// probeOne performs a single /healthz GET and classifies the response.
func probeOne(ctx context.Context, client *http.Client, t HealthTarget) healthResult {
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
	var st struct {
		Version string `json:"version"`
		Uptime  int64  `json:"uptime_seconds"`
		OK      bool   `json:"ok"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	r.Version, r.Uptime = st.Version, st.Uptime
	if resp.StatusCode == http.StatusOK && st.OK {
		r.Status = "Up"
	} else {
		r.Status = "Degraded"
		if resp.StatusCode != http.StatusServiceUnavailable {
			r.Err = resp.Status
		}
	}
	return r
}

// handleUIStatus renders the Live status page (system admins; read-only tier may
// view), probing every configured daemon.
func (s *Server) handleUIStatus(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "status.html", map[string]any{
		"Nav": "status", "CSRF": csrfCookieValue(r),
		"Results": s.probeHealth(r.Context()), "Configured": len(s.healthTargets) > 0,
	})
}

// handleUIStatusPanel renders just the status table (the page polls it to refresh
// live).
func (s *Server) handleUIStatusPanel(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "status-panel", map[string]any{
		"Results": s.probeHealth(r.Context()), "Configured": len(s.healthTargets) > 0,
	})
}

// handleGetStatus returns the probe results as JSON (system admins).
func (s *Server) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	res := s.probeHealth(r.Context())
	if res == nil {
		res = []healthResult{}
	}
	writeJSON(w, res)
}
