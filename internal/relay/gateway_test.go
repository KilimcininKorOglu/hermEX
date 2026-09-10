package relay

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// gatewayWorker builds a worker whose spool holds one queued message from the given sender
// and whose gateway dialer points at addr. Its router fails, so a delivery that reaches the
// mail-exchanger path instead of the gateway is unmistakable.
func gatewayWorker(t *testing.T, addr, from string) (*Worker, time.Time) {
	t.Helper()
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	raw := []byte("From: " + from + "\r\nSubject: out\r\n\r\nhi bob\r\n")
	if err := sp.Enqueue(from, []string{"bob@remote.test"}, raw, t0); err != nil {
		t.Fatal(err)
	}
	w := &Worker{
		Spool:         sp,
		HeloName:      "mx.test",
		Router:        func(string) ([]string, error) { return nil, errNoMailExchanger },
		GatewayDialer: func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
	}
	return w, t0
}

// errNoMailExchanger fails the direct path, so any test that reaches it fails loudly.
var errNoMailExchanger = &net.DNSError{Err: "no mail exchanger in this test", IsNotFound: true}

// TestGatewayDeliversThroughTheConfiguredHost is the load-bearing case: with a gateway
// installed the message reaches it, and the recipient domain's mail exchangers are never
// resolved.
func TestGatewayDeliversThroughTheConfiguredHost(t *testing.T) {
	be, addr := startSink(t)
	w, t0 := gatewayWorker(t, addr, "alice@hermex.test")
	w.SetGateways(&Gateway{Host: "gateway.test", Port: 587}, nil)

	sent, err := w.ProcessDue(context.Background(), t0)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	if sent != 1 || len(be.recorded()) != 1 {
		t.Errorf("settled %d and the gateway received %d messages, want 1 and 1", sent, len(be.recorded()))
	}
}

// TestGatewayPerDomainOverridesTheGlobal proves a domain's own gateway wins for mail whose
// envelope sender is in that domain.
func TestGatewayPerDomainOverridesTheGlobal(t *testing.T) {
	_, addr := startSink(t)
	w, t0 := gatewayWorker(t, addr, "alice@tenant.test")
	var dialed string
	w.GatewayDialer = func(a string) (net.Conn, error) { dialed = a; return net.Dial("tcp", addr) }
	w.SetGateways(&Gateway{Host: "global.test", Port: 25},
		map[string]Gateway{"tenant.test": {Host: "tenant-gw.test", Port: 2525}})

	if _, err := w.ProcessDue(context.Background(), t0); err != nil {
		t.Fatalf("process: %v", err)
	}

	if dialed != "tenant-gw.test:2525" {
		t.Errorf("dialed %q, want the domain's own gateway tenant-gw.test:2525", dialed)
	}
}

// TestGatewayUsesTheGlobalForAnUnlistedDomain proves a domain with no override falls back
// to the global gateway.
func TestGatewayUsesTheGlobalForAnUnlistedDomain(t *testing.T) {
	_, addr := startSink(t)
	w, t0 := gatewayWorker(t, addr, "alice@other.test")
	var dialed string
	w.GatewayDialer = func(a string) (net.Conn, error) { dialed = a; return net.Dial("tcp", addr) }
	w.SetGateways(&Gateway{Host: "global.test", Port: 25},
		map[string]Gateway{"tenant.test": {Host: "tenant-gw.test", Port: 2525}})

	if _, err := w.ProcessDue(context.Background(), t0); err != nil {
		t.Fatalf("process: %v", err)
	}

	if dialed != "global.test:25" {
		t.Errorf("dialed %q, want the global gateway global.test:25", dialed)
	}
}

// TestGatewayRequiresSTARTTLSWhenConfigured is the security case: a gateway configured for
// STARTTLS that does not offer it must fail the delivery rather than send in the clear.
func TestGatewayRequiresSTARTTLSWhenConfigured(t *testing.T) {
	be, addr := startSink(t)
	w, t0 := gatewayWorker(t, addr, "alice@hermex.test")
	w.SetGateways(&Gateway{Host: "gateway.test", Port: 587, RequireSTARTTLS: true}, nil)

	if _, err := w.ProcessDue(context.Background(), t0); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(be.recorded()) != 0 {
		t.Error("the message was delivered in the clear to a gateway configured for STARTTLS")
	}
}

// TestGatewayKeepsTheMessageQueuedOnAFailedUpgrade proves the refused delivery stays in the
// spool for a retry rather than being dropped.
func TestGatewayKeepsTheMessageQueuedOnAFailedUpgrade(t *testing.T) {
	_, addr := startSink(t)
	w, t0 := gatewayWorker(t, addr, "alice@hermex.test")
	w.SetGateways(&Gateway{Host: "gateway.test", Port: 587, RequireSTARTTLS: true}, nil)

	if _, err := w.ProcessDue(context.Background(), t0); err != nil {
		t.Fatalf("process: %v", err)
	}

	if due, _ := w.Spool.Claim(t0.Add(24*time.Hour), 10); len(due) != 1 {
		t.Errorf("spool holds %d recipients, want the undelivered one still queued", len(due))
	}
}

// TestGatewayAbsentKeepsTheDirectPath proves a worker with no gateway installed still
// resolves mail exchangers, so the setting is off by default.
func TestGatewayAbsentKeepsTheDirectPath(t *testing.T) {
	be, addr := startSink(t)
	w, t0 := gatewayWorker(t, addr, "alice@hermex.test")
	var routed string
	w.Router = func(domain string) ([]string, error) { routed = domain; return []string{"sink"}, nil }
	w.Dialer = func(string) (net.Conn, error) { return net.Dial("tcp", addr) }

	if _, err := w.ProcessDue(context.Background(), t0); err != nil {
		t.Fatalf("process: %v", err)
	}

	if routed != "remote.test" || len(be.recorded()) != 1 {
		t.Errorf("routed %q and received %d messages, want the recipient domain resolved and delivered",
			routed, len(be.recorded()))
	}
}

// startAuthSink runs a bare SMTP responder that advertises AUTH PLAIN and records whether
// the relay authenticated. hermEX's own SMTP server refuses AUTH on a plaintext link, so it
// cannot stand in here; this responder only has to speak enough to observe the command.
func startAuthSink(t *testing.T) (*int32, string) {
	t.Helper()
	authed := new(int32)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveAuthSession(conn, authed)
		}
	}()
	return authed, ln.Addr().String()
}

// serveAuthSession answers one session, counting an AUTH command and accepting the message.
func serveAuthSession(conn net.Conn, authed *int32) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	write := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }

	write("220 gateway.test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimRight(line, "\r\n"))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			write("250-gateway.test")
			write("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH"):
			atomic.AddInt32(authed, 1)
			write("235 2.7.0 authenticated")
		case strings.HasPrefix(cmd, "DATA"):
			write("354 go ahead")
		case strings.TrimRight(line, "\r\n") == ".":
			write("250 2.0.0 accepted")
			return
		default:
			write("250 2.0.0 ok")
		}
	}
}

// TestGatewayAuthenticatesWhenAUsernameIsSet proves the stored credentials reach the
// gateway. The host is 127.0.0.1 because net/smtp refuses PLAIN off an encrypted link
// except to localhost, which stands in for the TLS a real gateway would carry.
func TestGatewayAuthenticatesWhenAUsernameIsSet(t *testing.T) {
	authed, addr := startAuthSink(t)
	w, t0 := gatewayWorker(t, addr, "alice@hermex.test")
	w.SetGateways(&Gateway{Host: "127.0.0.1", Port: 587, Username: "relay", Password: "pw"}, nil)
	w.GatewayDialer = func(string) (net.Conn, error) { return net.Dial("tcp", addr) }

	if _, err := w.ProcessDue(context.Background(), t0); err != nil {
		t.Fatalf("process: %v", err)
	}

	if atomic.LoadInt32(authed) != 1 {
		t.Error("the gateway received no AUTH command, so the credentials never left")
	}
}
