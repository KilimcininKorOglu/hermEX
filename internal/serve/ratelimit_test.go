package serve

import (
	"context"
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
	go hs.Start()
	t.Cleanup(func() { hs.Shutdown(context.Background()) })
	return "http://" + hs.Addr().String() + "/"
}

// TestRateLimitRefusesPastBurst proves the limiter reaches requests end to end through
// serve.New: the burst is served, the next request gets 429, and the refusal carries
// the retry and budget headers a client needs to back off.
func TestRateLimitRefusesPastBurst(t *testing.T) {
	limiter := httplimit.NewLimiter()
	limiter.SetLimits(2, time.Minute)
	limiter.SetEnabled(true)
	url := startLimited(t, okHandler(), limiter)

	for i := range 2 {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 within the burst", i+1, resp.StatusCode)
		}
	}

	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status past the burst = %d, want 429", resp.StatusCode)
	}
	retry, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || retry < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", resp.Header.Get("Retry-After"))
	}
	if got := resp.Header.Get("X-RateLimit-Limit"); got != "2" {
		t.Errorf("X-RateLimit-Limit = %q, want 2", got)
	}
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "0" {
		t.Errorf("X-RateLimit-Remaining = %q, want 0", got)
	}
	if resp.Header.Get("X-RateLimit-Reset") == "" {
		t.Error("X-RateLimit-Reset missing on a refusal")
	}
}

// TestRateLimitAdmitsAfterWindow proves a refused client is served again once its
// window elapses, so the limiter throttles rather than bans.
func TestRateLimitAdmitsAfterWindow(t *testing.T) {
	limiter := httplimit.NewLimiter()
	limiter.SetLimits(1, 200*time.Millisecond)
	limiter.SetEnabled(true)
	url := startLimited(t, okHandler(), limiter)

	if resp, err := http.Get(url); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", resp.StatusCode)
	}

	time.Sleep(250 * time.Millisecond)
	resp, err = http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status after the window elapsed = %d, want 200", resp.StatusCode)
	}
}

// TestNilLimiterNeverRefuses proves a daemon that passes no limiter behaves exactly as
// it did before the middleware existed.
func TestNilLimiterNeverRefuses(t *testing.T) {
	url := startLimited(t, okHandler(), nil)
	for i := range 20 {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 with no limiter", i+1, resp.StatusCode)
		}
	}
}

// TestDisabledLimiterNeverRefuses proves the default (an operator has not enabled the
// limiter) serves every request.
func TestDisabledLimiterNeverRefuses(t *testing.T) {
	limiter := httplimit.NewLimiter()
	limiter.SetLimits(1, time.Minute)
	url := startLimited(t, okHandler(), limiter)
	for i := range 10 {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 while disabled", i+1, resp.StatusCode)
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
	url := startLimited(t, okHandler(), limiter) // no FrontDoor option

	for i := range 5 {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("X-Forwarded-For", "203.0.113."+strconv.Itoa(i))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d = %d; the forwarded address is trusted here, so each claim is a fresh budget", i+1, resp.StatusCode)
		}
	}
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
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("X-Forwarded-For", "203.0.113."+strconv.Itoa(i))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		resp.Body.Close()
		statuses = append(statuses, resp.StatusCode)
	}
	if statuses[0] != http.StatusOK {
		t.Errorf("first request = %d, want 200", statuses[0])
	}
	if statuses[1] != http.StatusTooManyRequests || statuses[2] != http.StatusTooManyRequests {
		t.Errorf("later requests = %v, want 429: the forged addresses must not each get a budget", statuses[1:])
	}
	for _, xff := range seen {
		if xff != "" {
			t.Errorf("handler saw X-Forwarded-For %q, want it stripped at the front door", xff)
		}
	}
}
