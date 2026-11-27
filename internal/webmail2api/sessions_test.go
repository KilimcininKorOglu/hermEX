package webmail2api

import (
	"strings"
	"testing"
)

// TestDeviceLabel proves the session list's coarse "Browser on OS" summary, including
// that iPhone matches iOS before its "like Mac OS X" token trips the macOS branch.
func TestDeviceLabel(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/120 Safari/537.36": "Chrome on macOS",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0":            "Firefox on Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Safari/604.1":    "Safari on iOS",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Edg/120":                                  "Edge on Linux",
		"": "Browser",
	}
	for ua, want := range cases {
		if got := deviceLabel(ua); got != want {
			t.Errorf("deviceLabel(%q) = %q, want %q", ua, got, want)
		}
	}
	// A client-chosen header is never echoed: markup, control bytes and an
	// oversized value all reduce to a fixed label.
	for _, ua := range []string{
		"<script>alert(1)</script>",
		"Evil\r\nSet-Cookie: x=1",
		strings.Repeat("A", 64<<10),
	} {
		if got := deviceLabel(ua); got != "Browser" {
			t.Errorf("deviceLabel(%.30q) = %.40q, want the fixed label", ua, got)
		}
	}
}
