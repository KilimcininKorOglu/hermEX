package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hermex/internal/config"
	"hermex/internal/logging"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	})
}

// TestServePlaintext proves that with no certificate configured New falls back
// to plaintext HTTP (backward compatible with the pre-TLS daemons).
func TestServePlaintext(t *testing.T) {
	hs, err := New("127.0.0.1:0", okHandler(), &config.Config{}, nil, logging.System) // TLS disabled
	if err != nil {
		t.Fatal(err)
	}
	go hs.Start()
	defer hs.Shutdown(context.Background())

	resp, err := http.Get("http://" + hs.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if body, _ := io.ReadAll(resp.Body); string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

// TestServeTLS proves that with a certificate configured New terminates TLS,
// that a client trusting the cert completes the request, and that the TLS 1.2
// floor rejects an older client.
func TestServeTLS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir)
	cfg := &config.Config{TLSCert: certPath, TLSKey: keyPath}

	hs, err := New("127.0.0.1:0", okHandler(), cfg, nil, logging.System)
	if err != nil {
		t.Fatal(err)
	}
	go hs.Start()
	defer hs.Shutdown(context.Background())

	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("AppendCertsFromPEM: no cert added")
	}
	url := "https://" + hs.Addr().String() + "/"

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("HTTPS GET (trusting cert): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Errorf("connection not TLS >= 1.2: %+v", resp.TLS)
	}

	// The TLS 1.2 floor must reject a client that caps at TLS 1.1.
	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    pool,
		MaxVersion: tls.VersionTLS11,
	}}}
	if _, err := old.Get(url); err == nil {
		t.Error("TLS 1.1 client should be rejected by the 1.2 floor")
	}
}

// TestServerGracefulDrain proves Shutdown drains an in-flight request instead of
// cutting it off: a request enters a blocked handler, Shutdown is started while
// it is still running, and only then is the handler released — the response must
// still complete with its body, and Shutdown must report success. A hard close
// would fail the in-flight request instead.
func TestServerGracefulDrain(t *testing.T) {
	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(handlerEntered)
		<-releaseHandler
		io.WriteString(w, "drained")
	})
	hs, err := New("127.0.0.1:0", h, &config.Config{}, nil, logging.System)
	if err != nil {
		t.Fatal(err)
	}
	go hs.Start()

	type result struct {
		body string
		err  error
	}
	resc := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + hs.Addr().String() + "/")
		if err != nil {
			resc <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		resc <- result{body: string(b)}
	}()

	<-handlerEntered // the request is now in-flight inside the handler

	shutDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutDone <- hs.Shutdown(ctx)
	}()
	time.Sleep(50 * time.Millisecond) // let Shutdown begin while the handler is still blocked
	close(releaseHandler)

	got := <-resc
	if got.err != nil {
		t.Fatalf("in-flight request failed during shutdown: %v", got.err)
	}
	if got.body != "drained" {
		t.Errorf("in-flight body = %q, want \"drained\"", got.body)
	}
	if err := <-shutDone; err != nil {
		t.Errorf("Shutdown = %v, want nil after draining", err)
	}
}

// TestTLSListener proves TLSListener yields a listener that terminates TLS for
// the mail daemons, and errors when no certificate is configured.
func TestTLSListener(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir)
	cfg := &config.Config{TLSCert: certPath, TLSKey: keyPath}

	ln, err := TLSListener("127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("TLSListener: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.(*tls.Conn).Handshake()
	}()

	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("AppendCertsFromPEM: no cert added")
	}
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: pool})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if v := conn.ConnectionState().Version; v < tls.VersionTLS12 {
		t.Errorf("negotiated version = %#x, want >= TLS 1.2", v)
	}

	if _, err := TLSListener("127.0.0.1:0", &config.Config{}); err == nil {
		t.Error("TLSListener without a certificate should error")
	}
}

// captureSink records every event the logger emits, for asserting the request log.
type captureSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *captureSink) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *captureSink) last() (logging.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return logging.Event{}, false
	}
	return c.events[len(c.events)-1], true
}

// TestRequestLoggingEmitsEvent proves the serve middleware records one structured
// event per request — method, path, status (and a 4xx level), the presented
// Basic-auth user, the real client from X-Forwarded-For, and the inbound request
// id — and that the password never reaches the event. This is the seam the central
// log uses to reconstruct who did what over HTTP.
func TestRequestLoggingEmitsEvent(t *testing.T) {
	sink := &captureSink{}
	logger := logging.New(sink)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // a 4xx, to assert the warn level
	})
	hs, err := New("127.0.0.1:0", h, &config.Config{}, logger, logging.Webmail)
	if err != nil {
		t.Fatal(err)
	}
	go hs.Start()
	defer hs.Shutdown(context.Background())

	req, _ := http.NewRequest(http.MethodGet, "http://"+hs.Addr().String()+"/mail/inbox", nil)
	req.SetBasicAuth("alice@hermex.test", "hunter2")
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	req.Header.Set("X-Request-Id", "req-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	// Emit runs after ServeHTTP returns, which can lag the client's response; poll
	// briefly for the (synchronous) sink to receive it.
	var e logging.Event
	for range 50 {
		if got, ok := sink.last(); ok {
			e = got
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e.Name != "http.request" {
		t.Fatalf("event name = %q, want http.request (middleware did not log)", e.Name)
	}
	if e.Subsystem != logging.Webmail {
		t.Errorf("subsystem = %q, want webmail", e.Subsystem)
	}
	if e.User != "alice@hermex.test" {
		t.Errorf("user = %q, want alice@hermex.test", e.User)
	}
	if e.RemoteAddr != "203.0.113.7" {
		t.Errorf("remote = %q, want the first X-Forwarded-For hop 203.0.113.7", e.RemoteAddr)
	}
	if e.RequestID != "req-123" {
		t.Errorf("request id = %q, want the inbound req-123", e.RequestID)
	}
	if e.Fields["method"] != http.MethodGet || e.Fields["path"] != "/mail/inbox" {
		t.Errorf("method/path = %v/%v, want GET /mail/inbox", e.Fields["method"], e.Fields["path"])
	}
	if e.Fields["status"] != http.StatusTeapot {
		t.Errorf("status = %v, want %d", e.Fields["status"], http.StatusTeapot)
	}
	if e.Level != logging.LevelWarn {
		t.Errorf("level = %v, want warn for a 4xx response", e.Level)
	}
	// The password must never appear anywhere in the rendered event.
	var rendered strings.Builder
	logging.NewStderrSink(&rendered).Write(e)
	if strings.Contains(rendered.String(), "hunter2") {
		t.Error("the request password leaked into the logged event")
	}
}

// writeSelfSignedCert generates an ECDSA P-256 self-signed certificate valid for
// localhost/127.0.0.1 and writes the PEM cert/key to dir, returning their paths.
func writeSelfSignedCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hermex.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}
