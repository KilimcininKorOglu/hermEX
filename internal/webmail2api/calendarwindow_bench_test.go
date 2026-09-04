package webmail2api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// benchCalendar seeds n appointments spread one hour apart across n hours from
// 2026-01-01 and returns a server plus a request helper bound to that mailbox.
func benchCalendar(tb testing.TB, n int) (func(string) []eventJSON, string) {
	tb.Helper()
	dir := tb.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		tb.Fatal(err)
	}
	st.Close()

	secret := []byte("calendar-window-bench-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		tb.Fatal(err)
	}
	do := func(method, target, body string) *httptest.ResponseRecorder {
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		s := base.Add(time.Duration(i) * time.Hour)
		body := fmt.Sprintf(`{"summary":"Appt %d","start":%q,"end":%q}`,
			i, s.Format(time.RFC3339), s.Add(30*time.Minute).Format(time.RFC3339))
		if rec := do(http.MethodPost, "/api/v1/calendar/events", body); rec.Code != http.StatusOK {
			tb.Fatalf("seed %d: status %d %s", i, rec.Code, rec.Body.String())
		}
	}

	list := func(target string) []eventJSON {
		rec := do(http.MethodGet, target, "")
		if rec.Code != http.StatusOK {
			tb.Fatalf("list %q: status %d", target, rec.Code)
		}
		var out struct {
			Events []eventJSON `json:"events"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			tb.Fatal(err)
		}
		return out.Events
	}
	return list, dir
}

// BenchmarkGetEventsWindow measures a narrow range request against a large
// calendar. The window keeps a handful of events; everything the request spends
// beyond that is the cost of deciding which ones.
func BenchmarkGetEventsWindow(b *testing.B) {
	for _, n := range []int{500, 2000} {
		b.Run(fmt.Sprintf("appointments=%d", n), func(b *testing.B) {
			list, _ := benchCalendar(b, n)
			const window = "/api/v1/calendar/events?start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z"
			got := len(list(window))
			b.ResetTimer()
			for b.Loop() {
				list(window)
			}
			b.StopTimer()
			b.ReportMetric(float64(got), "events/op")
		})
	}
}
