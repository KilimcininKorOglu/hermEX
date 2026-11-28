package antivirus

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// fakeClamd is a minimal clamd that speaks just enough INSTREAM to drive the
// client: it reads the zINSTREAM command, reassembles the chunked payload, then
// writes the configured reply. It records the reassembled payload so a test can
// assert the client framed the stream correctly.
type fakeClamd struct {
	ln      net.Listener
	reply   string
	gotBody chan []byte
	done    chan error // the serving goroutine's outcome, read at cleanup
}

func newFakeClamd(t *testing.T, reply string) *fakeClamd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeClamd{ln: ln, reply: reply, gotBody: make(chan []byte, 1), done: make(chan error, 1)}
	go func() { f.done <- f.serve() }()
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close fake clamd listener: %v", err)
		}
		// A listener closed before any client dialed ends Accept with ErrClosed,
		// which is the expected end of a test that never scanned.
		if err := <-f.done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("fake clamd: %v", err)
		}
	})
	return f
}

func (f *fakeClamd) addr() string { return "tcp://" + f.ln.Addr().String() }

func (f *fakeClamd) serve() (err error) {
	conn, err := f.ln.Accept()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	body, err := readInstream(conn)
	if err != nil {
		return err
	}
	f.gotBody <- body
	n, err := io.WriteString(conn, f.reply)
	if err == nil && n != len(f.reply) {
		err = fmt.Errorf("wrote %d of %d reply bytes", n, len(f.reply))
	}
	return err
}

// mustNew builds a Scanner and fails the test on an address New refuses.
func mustNew(t *testing.T, addr string) *Scanner {
	t.Helper()
	s, err := New(addr)
	if err != nil {
		t.Fatalf("New(%q): %v", addr, err)
	}
	return s
}

// faultyConn is a connection whose SetDeadline or Close fails on demand, the
// failures a real socket reports only under conditions a test cannot arrange.
type faultyConn struct {
	net.Conn
	deadlineErr, closeErr error
}

func (c faultyConn) SetDeadline(t time.Time) error {
	if c.deadlineErr != nil {
		return c.deadlineErr
	}
	return c.Conn.SetDeadline(t)
}

func (c faultyConn) Close() error {
	return errors.Join(c.Conn.Close(), c.closeErr)
}

// TestScanRefusesAConnectionWithoutADeadline: a deadline that cannot be set leaves
// the scan unbounded inside the SMTP DATA phase, so it must fail before any byte
// reaches clamd, never stream without a bound.
func TestScanRefusesAConnectionWithoutADeadline(t *testing.T) {
	s := mustNew(t, "tcp://127.0.0.1:3310")
	deadlineErr := errors.New("set deadline refused")
	client, server := net.Pipe()
	// The clamd side drains whatever stream arrives and then hangs up, so a scan
	// that wrongly streams ends with an error instead of waiting on the pipe.
	type drained struct {
		body []byte
		err  error
	}
	sent := make(chan drained, 1)
	go func() {
		body, err := readInstream(server)
		sent <- drained{body, errors.Join(err, server.Close())}
	}()
	s.dial = func(string, string, time.Duration) (net.Conn, error) {
		return faultyConn{Conn: client, deadlineErr: deadlineErr}, nil
	}
	res, err := s.Scan([]byte("payload"))
	if !errors.Is(err, deadlineErr) || res != (Result{}) {
		t.Errorf("Scan = %+v, %v; want the deadline error and no verdict", res, err)
	}
	// The client hung up before its first byte, so the clamd side reads end of
	// stream at the command record and holds no payload.
	if d := <-sent; !errors.Is(d.err, io.EOF) || len(d.body) != 0 {
		t.Errorf("clamd side read %q, err %v; want no stream and io.EOF", d.body, d.err)
	}
}

// TestScanKeepsTheVerdictWhenCloseFails: a connection that fails to close after
// clamd answered is reported, and the verdict survives it, because a caller that
// fails open on the error alone would deliver mail clamd found infected.
func TestScanKeepsTheVerdictWhenCloseFails(t *testing.T) {
	closeErr := errors.New("close refused")
	for _, c := range []struct {
		reply string
		want  Result
	}{
		{"stream: Eicar-Test-Signature FOUND\x00", Result{VirusName: "Eicar-Test-Signature"}},
		{"stream: OK\x00", Result{Clean: true}},
	} {
		f := newFakeClamd(t, c.reply)
		s := mustNew(t, f.addr())
		s.dial = func(network, address string, timeout time.Duration) (net.Conn, error) {
			conn, err := net.DialTimeout(network, address, timeout)
			if err != nil {
				return nil, err
			}
			return faultyConn{Conn: conn, closeErr: closeErr}, nil
		}
		res, err := s.Scan([]byte("payload"))
		if !errors.Is(err, closeErr) {
			t.Errorf("reply %q: err %v, want the close error reported", c.reply, err)
		}
		if res != c.want {
			t.Errorf("reply %q: result %+v, want %+v kept beside the close error", c.reply, res, c.want)
		}
	}
}

// readInstream consumes "zINSTREAM\0" then the length-prefixed chunks up to the
// zero-length terminator, returning the reassembled payload.
func readInstream(conn net.Conn) ([]byte, error) {
	cmd := make([]byte, len("zINSTREAM\x00"))
	if n, err := io.ReadFull(conn, cmd); err != nil {
		return nil, fmt.Errorf("read %d of %d command bytes: %w", n, len(cmd), err)
	}
	if string(cmd) != "zINSTREAM\x00" {
		return nil, errors.New("bad command record: " + string(cmd))
	}
	var body []byte
	var hdr [4]byte
	for {
		if n, err := io.ReadFull(conn, hdr[:]); err != nil {
			return nil, fmt.Errorf("read %d of 4 chunk-length bytes: %w", n, err)
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 {
			return body, nil
		}
		chunk := make([]byte, n)
		if got, err := io.ReadFull(conn, chunk); err != nil {
			return nil, fmt.Errorf("read %d of %d chunk bytes: %w", got, n, err)
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
	s := mustNew(t, f.addr())
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
	s := mustNew(t, f.addr())
	payload := make([]byte, chunkSize*2+123)
	for i := range payload {
		payload[i] = byte(i)
	}
	res, err := s.Scan(payload)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res != (Result{Clean: true}) {
		t.Errorf("Scan of a chunked payload clamd answered OK = %+v, want clean", res)
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
	s := mustNew(t, f.addr())
	res, err := s.Scan([]byte("x"))
	if err == nil {
		t.Fatal("want error on clamd ERROR reply")
	}
	if res != (Result{}) {
		t.Errorf("an ERROR reply produced a verdict %+v, want none", res)
	}
}

// TestScanNeverReadsAnUnclearReplyAsClean sends the replies a crashing, foreign or
// misconfigured daemon gives: none at all, an unrelated answer, and a size-limit
// refusal. None of them is a verdict, so each must be an error the caller's fail
// policy handles, never a clean result that delivers the message unscanned.
func TestScanNeverReadsAnUnclearReplyAsClean(t *testing.T) {
	for _, reply := range []string{"", "PONG\x00", "INSTREAM size limit exceeded. ERROR\x00", "stream: \x00"} {
		f := newFakeClamd(t, reply)
		s := mustNew(t, f.addr())
		res, err := s.Scan([]byte("payload"))
		if err == nil || res.Clean {
			t.Errorf("reply %q = %+v, err %v; want an error and no clean verdict", reply, res, err)
		}
	}
}

// startSilentClamd serves one clamd connection that reads the whole stream,
// checks it is want, and never answers until cleanup. It returns the address.
func startSilentClamd(t *testing.T, want string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	served := make(chan error, 1)
	t.Cleanup(func() {
		close(release)
		if err := <-served; err != nil {
			t.Errorf("silent clamd: %v", err)
		}
		if err := ln.Close(); err != nil {
			t.Errorf("close silent clamd listener: %v", err)
		}
	})
	go func() { served <- serveSilently(ln, want, release) }()
	return "tcp://" + ln.Addr().String()
}

// serveSilently accepts one connection, reads its INSTREAM, and holds it open
// without a reply until release is closed.
func serveSilently(ln net.Listener, want string, release <-chan struct{}) (err error) {
	conn, err := ln.Accept()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	body, err := readInstream(conn)
	if err != nil {
		return err
	}
	if string(body) != want {
		return fmt.Errorf("silent clamd received %q, want %q", body, want)
	}
	<-release // read everything, answer nothing
	return nil
}

// TestScanGivesUpOnASilentClamd connects to a clamd that reads the whole stream
// and never answers, the state of a wedged or overloaded daemon. The scan runs
// inside the SMTP DATA phase, so it must fail within its deadline and let the
// fail-open path deliver, rather than hold the session open indefinitely.
func TestScanGivesUpOnASilentClamd(t *testing.T) {
	s := mustNew(t, startSilentClamd(t, "payload"))
	s.timeout = 200 * time.Millisecond
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := s.Scan([]byte("payload"))
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		if o.err == nil || o.res != (Result{}) {
			t.Errorf("a silent clamd produced %+v, err %v; want an error and no verdict", o.res, o.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Scan still waiting on a silent clamd after 5s, want it bounded by its deadline")
	}
}

func TestScanDialError(t *testing.T) {
	// 127.0.0.1:1 is a reserved/unused port; the dial fails fast.
	s := mustNew(t, "tcp://127.0.0.1:1")
	res, err := s.Scan([]byte("x"))
	if err == nil {
		t.Fatal("want dial error when clamd is unreachable")
	}
	if res != (Result{}) {
		t.Errorf("an unreachable clamd produced a verdict %+v, want none", res)
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
	for _, bad := range []string{"  ", "tcp://", "unix://"} {
		s, err := New(bad)
		if err == nil {
			t.Errorf("New(%q) accepted an address with no target", bad)
		}
		if s != nil {
			t.Errorf("New(%q) refused but returned a scanner", bad)
		}
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
