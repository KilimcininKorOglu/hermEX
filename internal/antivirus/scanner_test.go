package antivirus

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"testing"
)

// fakeClamd is a minimal clamd that speaks just enough INSTREAM to drive the
// client: it reads the zINSTREAM command, reassembles the chunked payload, then
// writes the configured reply. It records the reassembled payload so a test can
// assert the client framed the stream correctly.
type fakeClamd struct {
	ln      net.Listener
	reply   string
	gotBody chan []byte
}

func newFakeClamd(t *testing.T, reply string) *fakeClamd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeClamd{ln: ln, reply: reply, gotBody: make(chan []byte, 1)}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeClamd) addr() string { return "tcp://" + f.ln.Addr().String() }

func (f *fakeClamd) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	body, err := readInstream(conn)
	if err != nil {
		return
	}
	f.gotBody <- body
	_, _ = io.WriteString(conn, f.reply)
}

// readInstream consumes "zINSTREAM\0" then the length-prefixed chunks up to the
// zero-length terminator, returning the reassembled payload.
func readInstream(conn net.Conn) ([]byte, error) {
	cmd := make([]byte, len("zINSTREAM\x00"))
	if _, err := io.ReadFull(conn, cmd); err != nil {
		return nil, err
	}
	if string(cmd) != "zINSTREAM\x00" {
		return nil, errors.New("bad command record: " + string(cmd))
	}
	var body []byte
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return nil, err
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 {
			return body, nil
		}
		chunk := make([]byte, n)
		if _, err := io.ReadFull(conn, chunk); err != nil {
			return nil, err
		}
		body = append(body, chunk...)
	}
}

func TestScanClean(t *testing.T) {
	f := newFakeClamd(t, "stream: OK\x00")
	s, err := New(f.addr())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("a clean message body")
	res, err := s.Scan(payload)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !res.Clean || res.VirusName != "" {
		t.Fatalf("want clean, got %+v", res)
	}
	if got := <-f.gotBody; string(got) != string(payload) {
		t.Fatalf("clamd received %q, want %q", got, payload)
	}
}

func TestScanInfected(t *testing.T) {
	f := newFakeClamd(t, "stream: Eicar-Test-Signature FOUND\x00")
	s, _ := New(f.addr())
	res, err := s.Scan([]byte("anything"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Clean {
		t.Fatal("want infected, got clean")
	}
	if res.VirusName != "Eicar-Test-Signature" {
		t.Fatalf("virus name = %q, want Eicar-Test-Signature", res.VirusName)
	}
}

// TestScanChunking pushes a payload larger than one chunk and asserts clamd
// received the exact bytes, proving the client's length framing is correct.
func TestScanChunking(t *testing.T) {
	f := newFakeClamd(t, "stream: OK\x00")
	s, _ := New(f.addr())
	payload := make([]byte, chunkSize*2+123)
	for i := range payload {
		payload[i] = byte(i)
	}
	if _, err := s.Scan(payload); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := <-f.gotBody
	if len(got) != len(payload) {
		t.Fatalf("clamd got %d bytes, want %d", len(got), len(payload))
	}
	for i := range got {
		if got[i] != payload[i] {
			t.Fatalf("byte %d differs: got %d want %d", i, got[i], payload[i])
		}
	}
}

func TestScanClamdError(t *testing.T) {
	f := newFakeClamd(t, "stream: database error ERROR\x00")
	s, _ := New(f.addr())
	if _, err := s.Scan([]byte("x")); err == nil {
		t.Fatal("want error on clamd ERROR reply")
	}
}

func TestScanDialError(t *testing.T) {
	// 127.0.0.1:1 is a reserved/unused port; the dial fails fast.
	s, _ := New("tcp://127.0.0.1:1")
	if _, err := s.Scan([]byte("x")); err == nil {
		t.Fatal("want dial error when clamd is unreachable")
	}
}

func TestNew(t *testing.T) {
	cases := map[string]struct{ net, addr string }{
		"tcp://clamav:3310":      {"tcp", "clamav:3310"},
		"clamav:3310":            {"tcp", "clamav:3310"},
		"unix:///run/clamd.sock": {"unix", "/run/clamd.sock"},
	}
	for in, want := range cases {
		s, err := New(in)
		if err != nil {
			t.Fatalf("New(%q): %v", in, err)
		}
		if s.network != want.net || s.address != want.addr {
			t.Errorf("New(%q) = %s/%s, want %s/%s", in, s.network, s.address, want.net, want.addr)
		}
	}
	if _, err := New("  "); err == nil {
		t.Error("want error for empty address")
	}
}

// eicar returns the standard EICAR anti-virus test signature, decoded at runtime
// so the literal never sits in the source tree (host AV would otherwise
// quarantine this file).
func eicar(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(
		"WDVPIVAlQEFQWzRcUFpYNTQoUF4pN0NDKTd9JEVJQ0FSLVNU" +
			"QU5EQVJELUFOVElWSVJVUy1URVNULUZJTEUhJEgrSCo=")
	if err != nil {
		t.Fatalf("decode EICAR: %v", err)
	}
	return b
}

// TestScanEICAR is the integration proof: it streams the real EICAR signature to
// a live clamd and expects a FOUND verdict. It skips unless HERMEX_TEST_CLAMD_ADDR
// points at a running daemon (set in the dev container once the clamav service
// exists), so it never fails on a host without clamd.
func TestScanEICAR(t *testing.T) {
	addr := os.Getenv("HERMEX_TEST_CLAMD_ADDR")
	if addr == "" {
		t.Skip("HERMEX_TEST_CLAMD_ADDR unset; skipping live clamd scan")
	}
	s, err := New(addr)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Scan(eicar(t))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Clean {
		t.Fatal("clamd reported EICAR clean; are signatures loaded?")
	}
	if res.VirusName == "" {
		t.Fatal("infected but no virus name")
	}
	t.Logf("clamd detected %q", res.VirusName)
}
