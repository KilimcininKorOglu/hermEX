package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadAndDerivations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	doc := `{"database_dsn":"root:pw@tcp(db:3306)/email","data_dir":"/data/mb",
	         "hostname":"mail.test","smtp_addr":":25","pop3_addr":":110"}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DatabaseDSN == "" || c.Hostname != "mail.test" {
		t.Fatalf("loaded config = %+v", c)
	}
	// Maildir/homedir follow the {prefix}/{domain}/{localpart} rule.
	if got := c.MaildirFor("Alice@Example.com"); got != "/data/mb/user/example.com/alice" {
		t.Errorf("MaildirFor = %q", got)
	}
	if got := c.HomedirFor("Example.com"); got != "/data/mb/domain/example.com" {
		t.Errorf("HomedirFor = %q", got)
	}
}

// TestMaildirForPartitions proves data_partitions spreads new mailboxes across
// roots by a stable hash: an empty pool leaves every mailbox under DataDir, while
// a configured pool places each user deterministically (same address — even
// across casing — always the same root, so a re-derivation never relocates a live
// mailbox), always inside the pool, keeping the {root}/user/{domain}/{local}
// shape, and actually using more than one partition.
func TestMaildirForPartitions(t *testing.T) {
	// Empty pool: byte-identical to the single-root rule.
	single := &Config{DataDir: "/data/mb"}
	if got := single.MaildirFor("alice@example.com"); got != "/data/mb/user/example.com/alice" {
		t.Errorf("empty-pool MaildirFor = %q, want the DataDir path", got)
	}

	pool := []string{"/p0", "/p1", "/p2"}
	c := &Config{DataDir: "/data/mb", DataPartitions: pool}

	// Stable across casing: the same user always lands on the same partition.
	if a, b := c.MaildirFor("alice@example.com"), c.MaildirFor("Alice@Example.com"); a != b {
		t.Errorf("MaildirFor not stable across casing: %q vs %q", a, b)
	}

	roots := map[string]bool{}
	for i := range 20 {
		addr := fmt.Sprintf("user%d@example.com", i)
		path := c.MaildirFor(addr)
		tail := fmt.Sprintf("/user/example.com/user%d", i)
		root, ok := strings.CutSuffix(path, tail)
		if !ok {
			t.Fatalf("MaildirFor(%q) = %q, want a {root}%s path", addr, path, tail)
		}
		if !slices.Contains(pool, root) {
			t.Errorf("MaildirFor(%q) root = %q, not in the pool %v", addr, root, pool)
		}
		roots[root] = true
	}
	// The whole point of a pool is to spread: a hash that parked everyone on one
	// partition would defeat it.
	if len(roots) < 2 {
		t.Errorf("partition hash used only %d root(s): %v — it does not spread", len(roots), roots)
	}
}

// TestLoadParsesDataPartitions proves the placement pool survives JSON
// unmarshalling so the daemons agree on where new mailboxes land.
func TestLoadParsesDataPartitions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	doc := `{"database_dsn":"d","data_dir":"/data","data_partitions":["/disk0","/disk1"]}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.DataPartitions) != 2 || c.DataPartitions[0] != "/disk0" || c.DataPartitions[1] != "/disk1" {
		t.Errorf("DataPartitions = %v, want the two configured roots", c.DataPartitions)
	}
}

func TestLoadRequiresDSNandDataDir(t *testing.T) {
	cases := []string{`{"data_dir":"/x"}`, `{"database_dsn":"d"}`}
	for _, doc := range cases {
		p := filepath.Join(t.TempDir(), "c.json")
		if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("Load(%s) should fail", doc)
		}
	}
}

// TestLoadParsesTLSFields proves the cert/key paths survive JSON unmarshalling so
// the daemons can find them.
func TestLoadParsesTLSFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	doc := `{"database_dsn":"d","data_dir":"/x","tls_cert":"/etc/c.pem","tls_key":"/etc/k.pem"}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.TLSCert != "/etc/c.pem" || c.TLSKey != "/etc/k.pem" {
		t.Errorf("TLS fields = %q / %q", c.TLSCert, c.TLSKey)
	}
}

// TestTLSConfigRequiresCert proves an unconfigured pair is an error, not a nil
// config the caller might mistake for plaintext.
func TestTLSConfigRequiresCert(t *testing.T) {
	if _, err := (&Config{}).TLSConfig(); err == nil {
		t.Error("TLSConfig with no cert/key should error")
	}
	if _, err := (&Config{TLSCert: "/c.pem"}).TLSConfig(); err == nil {
		t.Error("TLSConfig with only cert should error")
	}
}

// TestTLSConfigHandshake proves the builder yields a config a real client can
// complete a >= TLS 1.2 handshake against. It drives a live tls.NewListener fed
// by TLSConfig() and dials it trusting only the configured cert — this fails iff
// the builder is wrong (httptest.NewTLSServer would not, as it injects its own
// certificate and never touches TLSConfig).
func TestTLSConfigHandshake(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir)
	c := &Config{TLSCert: certPath, TLSKey: keyPath}
	cfg, err := c.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want TLS 1.2 (%#x)", cfg.MinVersion, tls.VersionTLS12)
	}

	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := tls.NewListener(rawLn, cfg)
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.(*tls.Conn).Handshake() // drive the server side of the handshake
	}()

	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("AppendCertsFromPEM: no cert added")
	}
	client, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("dial (trusting configured cert): %v", err)
	}
	defer client.Close()
	if v := client.ConnectionState().Version; v < tls.VersionTLS12 {
		t.Errorf("negotiated version = %#x, want >= TLS 1.2", v)
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
