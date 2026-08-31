package ssrfguard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIsPublicIP locks the address block: loopback, link-local (including the cloud
// metadata address), private, unspecified, and multicast are refused; routable
// addresses are allowed.
func TestIsPublicIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", // loopback
		"169.254.169.254", "169.254.1.1", "fe80::1", // link-local incl. metadata
		"10.0.0.1", "172.16.0.1", "192.168.1.1", "fc00::1", // private
		"0.0.0.0", "::", // unspecified
		"224.0.0.1", // multicast
	}
	for _, s := range blocked {
		if IsPublicIP(net.ParseIP(s)) {
			t.Errorf("IsPublicIP(%s) = true, want false (SSRF block)", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "203.0.113.5", "2606:4700:4700::1111"} {
		if !IsPublicIP(net.ParseIP(s)) {
			t.Errorf("IsPublicIP(%s) = false, want true (routable)", s)
		}
	}
}

// TestValidateURL locks the first gate: only absolute http(s) URLs with a host, and
// http only when explicitly allowed.
func TestValidateURL(t *testing.T) {
	if err := ValidateURL("", false); err == nil {
		t.Error("empty URL must be rejected")
	}
	if err := ValidateURL("http://cb.test/x", false); err == nil {
		t.Error("http must be rejected when not allowed")
	}
	if err := ValidateURL("http://cb.test/x", true); err != nil {
		t.Errorf("http must be allowed with allowHTTP: %v", err)
	}
	if err := ValidateURL("ftp://cb.test/x", false); err == nil {
		t.Error("non-http scheme must be rejected")
	}
	if err := ValidateURL("https:///x", false); err == nil {
		t.Error("URL without a host must be rejected")
	}
	if err := ValidateURL("https://cb.test/x", false); err != nil {
		t.Errorf("a valid https URL must pass: %v", err)
	}
}

// TestValidateURLRejectsInternalAddressLiterals proves a host written as an address
// is range-checked here rather than left for dial time. The dial guard would refuse
// it either way, but a caller that stores the URL first would otherwise keep a
// destination it can never deliver to.
func TestValidateURLRejectsInternalAddressLiterals(t *testing.T) {
	for _, raw := range []string{
		"https://127.0.0.1:8081/push",
		"https://[::1]/push",
		"https://169.254.169.254/latest/meta-data/",
		"https://10.0.0.5/push",
		"https://192.168.1.1/push",
	} {
		if err := ValidateURL(raw, false); err == nil {
			t.Errorf("ValidateURL(%s) accepted an internal address literal", raw)
		}
		if err := ValidateURL(raw, true); err != nil {
			t.Errorf("ValidateURL(%s, allowInternal) = %v, want accepted", raw, err)
		}
	}
	if err := ValidateURL("https://203.0.113.5/push", false); err != nil {
		t.Errorf("a routable address literal must pass: %v", err)
	}
}

// TestGuardedDialRefusesLoopback proves the dial-time block is what actually stops
// the request. The scheme gate alone would pass a loopback URL, so this is the check
// that closes the window a name resolving to an internal address opens.
func TestGuardedDialRefusesLoopback(t *testing.T) {
	dial := GuardedDial(false)
	if _, err := dial(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("a loopback dial was allowed")
	} else if !strings.Contains(err.Error(), "non-public") {
		t.Errorf("dial error = %v, want the guard's refusal", err)
	}
}

// TestGuardedDialAllowsInternalWhenPermitted proves the escape hatch works, so an
// internal or development deployment can still be wired.
func TestGuardedDialAllowsInternalWhenPermitted(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()

	conn, err := GuardedDial(true)(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("an internal dial was refused with allowInternal set: %v", err)
	}
	_ = conn.Close()
}

// TestClientRefusesInternalTarget proves the guard reaches real traffic: a request
// to a loopback server fails at the transport rather than being served.
func TestClientRefusesInternalTarget(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()

	if _, err := Client(false).Get(ts.URL); err == nil {
		t.Error("a request to a loopback server was allowed")
	}
	if _, err := Client(true).Get(ts.URL); err != nil {
		t.Errorf("the same request was refused with allowInternal set: %v", err)
	}
}

// TestClientDoesNotFollowRedirects proves a redirect cannot be used to escape the
// guard: the first hop is checked, so following one would let the target choose a
// second, unchecked destination.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.test/elsewhere", http.StatusFound)
	}))
	defer ts.Close()

	resp, err := Client(true).Get(ts.URL)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Errorf("status = %d, want the redirect returned unfollowed", resp.StatusCode)
		}
		return
	}
	if !strings.Contains(err.Error(), "redirects are not followed") {
		t.Errorf("error = %v, want the redirect refusal", err)
	}
}
