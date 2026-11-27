package serve

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"hermex/internal/config"
	"hermex/internal/httplimit"
	"hermex/internal/logging"
)

// startLimited starts a plaintext server carrying limiter and returns its base URL.
func startLimited(t *testing.T, h http.Handler, limiter *httplimit.Limiter, opts ...Option) string {
	t.Helper()
	hs, err := New("127.0.0.1:0", h, &config.Config{}, nil, logging.System, limiter, opts...)
	if err != nil {
		t.Fatal(err)
	}
	runServer(t, hs)
	return "http://" + hs.Addr().String() + "/"
}

// statusOf sends req, closes the response body and returns the status.
func statusOf(t *testing.T, req *http.Request) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	closeBody(t, resp)
	return resp.StatusCode
}

// TestRateLimitRefusesPastBurst proves the limiter reaches requests end to end through
// serve.New: the burst is served, the next request gets 429, and the refusal carries
// the retry and budget headers a client needs to back off.
func TestRateLimitRefusesPastBurst(t *testing.T) {
	limiter := httplimit.NewLimiter()
	limiter.SetLimits(2, time.Minute)
	limiter.SetEnabled(true)
	url := startLimited(t, okHandler(t), limiter)

	for i := range 2 {
		if got := statusOf(t, mustGet(t, url)); got != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 within the burst", i+1, got)
		}
	}

	resp, err := http.DefaultClient.Do(mustGet(t, url))
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status past the burst = %d, want 429", resp.StatusCode)
	}
	retry, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	wantEq(t, err == nil && retry >= 1, true, "Retry-After is a positive number of seconds")
	wantEq(t, resp.Header.Get("X-RateLimit-Limit"), "2", "X-RateLimit-Limit")
	wantEq(t, resp.Header.Get("X-RateLimit-Remaining"), "0", "X-RateLimit-Remaining")
	wantEq(t, resp.Header.Get("X-RateLimit-Reset") != "", true, "the refusal carries X-RateLimit-Reset")
}

// TestRateLimitAdmitsAfterWindow proves a refused client is served again once its
// window elapses, so the limiter throttles rather than bans.
func TestRateLimitAdmitsAfterWindow(t *testing.T) {
	limiter := httplimit.NewLimiter()
	limiter.SetLimits(1, 200*time.Millisecond)
	limiter.SetEnabled(true)
	url := startLimited(t, okHandler(t), limiter)

	wantEq(t, statusOf(t, mustGet(t, url)), http.StatusOK, "first request")
	if got := statusOf(t, mustGet(t, url)); got != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", got)
	}

	time.Sleep(250 * time.Millisecond)
	wantEq(t, statusOf(t, mustGet(t, url)), http.StatusOK, "status after the window elapsed")
}

// TestNilLimiterNeverRefuses proves a daemon that passes no limiter behaves exactly as
// it did before the middleware existed.
func TestNilLimiterNeverRefuses(t *testing.T) {
	url := startLimited(t, okHandler(t), nil)
	for i := range 20 {
		if got := statusOf(t, mustGet(t, url)); got != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 with no limiter", i+1, got)
		}
	}
}

// TestDisabledLimiterNeverRefuses proves the default (an operator has not enabled the
// limiter) serves every request.
func TestDisabledLimiterNeverRefuses(t *testing.T) {
	limiter := httplimit.NewLimiter()
	limiter.SetLimits(1, time.Minute)
	url := startLimited(t, okHandler(t), limiter)
	for i := range 10 {
		if got := statusOf(t, mustGet(t, url)); got != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 while disabled", i+1, got)
		}
	}
}

// TestForgedForwardedForEvadesLimiterWithoutFrontDoor documents why the front door
// needs the strip: a server that accepts the client's X-Forwarded-For keys on whatever
// address the client claims, so a new claim per request escapes the budget entirely.
func TestForgedForwardedForEvadesLimiterWithoutFrontDoor(t *testing.T) {
	limiter := httplimit.NewLimiter()
	limiter.SetLimits(1, time.Minute)
	limiter.SetEnabled(true)
	url := startLimited(t, okHandler(t), limiter) // no FrontDoor option

	for i := range 5 {
		req := mustGet(t, url)
		req.Header.Set("X-Forwarded-For", "203.0.113."+strconv.Itoa(i))
		if got := statusOf(t, req); got != http.StatusOK {
			t.Fatalf("request %d = %d; the forwarded address is trusted here, so each claim is a fresh budget", i+1, got)
		}
	}
}

// TestRateLimitCountsAnIPv6SubnetAsOneClient sends requests, as the gateway forwards
// them, from a fresh address of one /64 each time. A host is routed a whole /64
// (RFC 6177), so keyed per address it had a fresh budget per request; the second
// request from that /64 must be refused and a neighbouring /64 still served.
func TestRateLimitCountsAnIPv6SubnetAsOneClient(t *testing.T) {
	limiter := httplimit.NewLimiter()
	limiter.SetLimits(1, time.Minute)
	limiter.SetEnabled(true)
	url := startLimited(t, okHandler(t), limiter)

	get := func(client string) int {
		t.Helper()
		req := mustGet(t, url)
		req.Header.Set("X-Forwarded-For", client)
		return statusOf(t, req)
	}
	wantEq(t, get("2001:db8:1:2::1"), http.StatusOK, "the first request from the /64")
	wantEq(t, get("2001:db8:1:2::2"), http.StatusTooManyRequests, "a second address of the same /64")
	wantEq(t, get("2001:db8:1:3::1"), http.StatusOK, "the neighbouring /64")
}

// TestFrontDoorStripsForgedForwardedFor proves the outermost server drops the client's
// X-Forwarded-For: the handler never sees the forged header, and the limiter counts
// every request against the real peer, so a per-request forgery no longer escapes the
// budget.
func TestFrontDoorStripsForgedForwardedFor(t *testing.T) {
	limiter := httplimit.NewLimiter()
	limiter.SetLimits(1, time.Minute)
	limiter.SetEnabled(true)
	var seen []string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-Forwarded-For"))
		w.WriteHeader(http.StatusOK)
	})
	url := startLimited(t, h, limiter, FrontDoor())

	statuses := make([]int, 0, 3)
	for i := range 3 {
		req := mustGet(t, url)
		req.Header.Set("X-Forwarded-For", "203.0.113."+strconv.Itoa(i))
		statuses = append(statuses, statusOf(t, req))
	}
	if statuses[0] != http.StatusOK {
		t.Errorf("first request = %d, want 200", statuses[0])
	}
	if statuses[1] != http.StatusTooManyRequests || statuses[2] != http.StatusTooManyRequests {
		t.Errorf("later requests = %v, want 429: the forged addresses must not each get a budget", statuses[1:])
	}
	// The refused requests never reached the handler: only the admitted one is seen.
	wantEq(t, len(seen), 1, "requests the handler served")
	for _, xff := range seen {
		if xff != "" {
			t.Errorf("handler saw X-Forwarded-For %q, want it stripped at the front door", xff)
		}
	}
}
