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
	"testing"
	"time"

	"hermex/internal/config"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	})
}

// TestServePlaintext proves that with no certificate configured New falls back
// to plaintext HTTP (backward compatible with the pre-TLS daemons).
func TestServePlaintext(t *testing.T) {
	hs, err := New("127.0.0.1:0", okHandler(), &config.Config{}) // TLS disabled
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

	hs, err := New("127.0.0.1:0", okHandler(), cfg)
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
	hs, err := New("127.0.0.1:0", h, &config.Config{})
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
