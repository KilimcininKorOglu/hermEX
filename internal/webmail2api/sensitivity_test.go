package webmail2api

import (
	"strings"
	"testing"
)

// TestBuildOutgoingSensitivity proves the compose Sensitivity field maps onto
// PR_SENSITIVITY and is emitted as the RFC 2156 Sensitivity header by Export,
// mirroring the server-rendered webmail's compose path. A normal/empty value
// omits the header, so an ordinary message carries no sensitivity marking.
func TestBuildOutgoingSensitivity(t *testing.T) {
	srv := &Server{hostname: "mail.hermex.test"}
	cases := []struct {
		value  string
		header string // expected Sensitivity header value; "" means the header is absent
	}{
		{"", ""},
		{"normal", ""},
		{"personal", "Personal"},
		{"private", "Private"},
		{"confidential", "Company-Confidential"},
	}
	for _, tc := range cases {
		raw, err := srv.buildOutgoing("alice@hermex.test", sendRequest{
			To:          []string{"bob@hermex.test"},
			Subject:     "hi",
			Body:        "body",
			Sensitivity: tc.value,
		})
		if err != nil {
			t.Fatalf("buildOutgoing(sensitivity=%q): %v", tc.value, err)
		}
		if got := sensitivityHeader(string(raw)); got != tc.header {
			t.Errorf("sensitivity=%q → header %q, want %q", tc.value, got, tc.header)
		}
	}
}

// sensitivityHeader returns the Sensitivity header value of a raw RFC 5322 message,
// or "" when absent. It reads only the header block (before the first blank line)
// so a body mention of the word cannot be mistaken for the field.
func sensitivityHeader(raw string) string {
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		raw = raw[:i]
	} else if i := strings.Index(raw, "\n\n"); i >= 0 {
		raw = raw[:i]
	}
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if v, ok := strings.CutPrefix(line, "Sensitivity:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
