package relay

import (
	"context"
	"net"
	"testing"
)

// TestSplitRouteDeliversToTheSplitHost proves a split domain's recipient is
// delivered to the configured host, bypassing both the recipient domain's mail
// exchangers and an installed outbound gateway.
func TestSplitRouteDeliversToTheSplitHost(t *testing.T) {
	be, addr := startSink(t)
	w, t0 := gatewayWorker(t, addr, "alice@hermex.test")
	w.GatewayDialer = func(string) (net.Conn, error) {
		t.Error("a split-domain recipient went through the gateway")
		return nil, errNoMailExchanger
	}
	var dialed string
	w.Dialer = func(host string) (net.Conn, error) { dialed = host; return net.Dial("tcp", addr) }
	w.SetGateways(&Gateway{Host: "gateway.test", Port: 587}, nil)
	w.SplitHost = func(domain string) (string, error) {
		if domain == "remote.test" {
			return "legacy.remote.test", nil
		}
		return "", nil
	}

	sent, err := w.ProcessDue(context.Background(), t0)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if sent != 1 || len(be.recorded()) != 1 {
		t.Errorf("settled %d and the split host received %d messages, want 1 and 1", sent, len(be.recorded()))
	}
	if dialed != "legacy.remote.test" {
		t.Errorf("dialed %q, want the split host", dialed)
	}
}
