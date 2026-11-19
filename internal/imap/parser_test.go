package imap

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// TestReadLiteralRespectsServerLimit proves the literal cap is read live from the
// server's atomic, so an operator's edit (applied by the poll) decides what a literal
// may be, a literal over the cap is rejected, and raising the cap admits it, with no
// reconstruction.
func TestReadLiteralRespectsServerLimit(t *testing.T) {
	var limit atomic.Int64
	limit.Store(8) // an 8-byte cap

	var out bytes.Buffer
	over := &commandReader{
		br:         bufio.NewReader(strings.NewReader("a LOGIN {20}\r\n")),
		bw:         bufio.NewWriter(&out),
		maxLiteral: &limit,
	}
	if _, err := over.readCommand(); !errors.Is(err, errProtocol) {
		t.Fatalf("20-byte literal under an 8-byte cap = %v, want a protocol error", err)
	}

	// Raising the cap (what the poll does) admits a literal the old cap would reject.
	limit.Store(50 << 20)
	var out2 bytes.Buffer
	ok := &commandReader{
		br:         bufio.NewReader(strings.NewReader("a LOGIN {2}\r\nhi\r\n")),
		bw:         bufio.NewWriter(&out2),
		maxLiteral: &limit,
	}
	if _, err := ok.readCommand(); err != nil {
		t.Fatalf("2-byte literal under a raised cap = %v, want success", err)
	}
}

// TestLiteralBeforeLoginIsBoundByTheLineCap reads a 200-byte literal from a
// client that has not authenticated. Before login the only literals a client
// needs are a user name and password, so the cap there is the command-line cap
// (here 128), not the message-sized literal cap: an anonymous client must not be
// able to make the server allocate a mail-sized buffer. The same literal after
// login is accepted.
func TestLiteralBeforeLoginIsBoundByTheLineCap(t *testing.T) {
	var line, literal atomic.Int64
	line.Store(128)
	literal.Store(50 << 20)
	input := "a LOGIN {200+}\r\n" + strings.Repeat("x", 200) + " pw\r\n"
	for _, authed := range []bool{false, true} {
		r := &commandReader{
			br: bufio.NewReader(strings.NewReader(input)), bw: bufio.NewWriter(&bytes.Buffer{}),
			maxLiteral: &literal, maxLine: &line, authed: func() bool { return authed },
		}
		_, err := r.readCommand()
		if !authed && !errors.Is(err, errProtocol) {
			t.Errorf("before login: a 200-byte literal under a 128-byte line cap = %v, want a protocol error", err)
		}
		if authed && err != nil {
			t.Errorf("after login: a 200-byte literal = %v, want success", err)
		}
	}
}

// TestLiteralsOfOneCommandShareTheLiteralCap sends one command whose two
// literals are each under the 8-byte cap but 12 bytes together. The reader keeps
// a whole command in memory before it runs, so the cap bounds their sum: without
// that, a command of many literals costs the cap times their number.
func TestLiteralsOfOneCommandShareTheLiteralCap(t *testing.T) {
	var limit atomic.Int64
	limit.Store(8)
	r := &commandReader{
		br:         bufio.NewReader(strings.NewReader("a LOGIN {6+}\r\nabcdef {6+}\r\nghijkl\r\n")),
		bw:         bufio.NewWriter(&bytes.Buffer{}),
		maxLiteral: &limit,
	}
	if _, err := r.readCommand(); !errors.Is(err, errProtocol) {
		t.Fatalf("two 6-byte literals under an 8-byte cap = %v, want a protocol error", err)
	}
	r = &commandReader{
		br:         bufio.NewReader(strings.NewReader("a LOGIN {4+}\r\nabcd {4+}\r\nefgh\r\n")),
		bw:         bufio.NewWriter(&bytes.Buffer{}),
		maxLiteral: &limit,
	}
	if _, err := r.readCommand(); err != nil {
		t.Fatalf("two 4-byte literals filling an 8-byte cap = %v, want success", err)
	}
}

// lex runs the command reader over input and returns the tokens plus whatever
// the server wrote back (continuation requests).
func lex(t *testing.T, input string) ([]token, string) {
	t.Helper()
	var out bytes.Buffer
	r := &commandReader{
		br: bufio.NewReader(strings.NewReader(input)),
		bw: bufio.NewWriter(&out),
	}
	toks, err := r.readCommand()
	if err != nil {
		t.Fatalf("readCommand(%q): %v", input, err)
	}
	return toks, out.String()
}

func TestLexSimpleCommand(t *testing.T) {
	toks, _ := lex(t, "a1 LOGIN admin secret\r\n")
	want := []string{"a1", "LOGIN", "admin", "secret"}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %+v", len(toks), len(want), toks)
	}
	for i, w := range want {
		if toks[i].kind != tAtom || toks[i].val != w {
			t.Errorf("token %d = %+v, want atom %q", i, toks[i], w)
		}
	}
}

func TestLexParensBracketsQuoted(t *testing.T) {
	// FETCH body section: brackets are structural and survive spaces inside.
	toks, _ := lex(t, `a UID FETCH 1:* (FLAGS BODY.PEEK[HEADER.FIELDS (DATE FROM)]<0.512>)`+"\r\n")
	var kinds []tokenKind
	for _, tk := range toks {
		kinds = append(kinds, tk.kind)
	}
	want := []tokenKind{
		tAtom, tAtom, tAtom, tAtom, // a UID FETCH 1:*
		tLParen, tAtom, tAtom, // ( FLAGS BODY.PEEK
		tLBracket, tAtom, tLParen, tAtom, tAtom, tRParen, tRBracket, // [HEADER.FIELDS (DATE FROM)]
		tAtom, tRParen, // <0.512> )
	}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v (len %d), want len %d", kinds, len(kinds), len(want))
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("token %d kind = %d, want %d (val %q)", i, kinds[i], want[i], toks[i].val)
		}
	}
	// The sequence set and partial stay intact as single atoms.
	if toks[3].val != "1:*" {
		t.Errorf("seqset atom = %q, want 1:*", toks[3].val)
	}
	if toks[14].val != "<0.512>" {
		t.Errorf("partial atom = %q, want <0.512>", toks[14].val)
	}
}

func TestLexSynchronizingLiteral(t *testing.T) {
	// "admin" arrives as a synchronizing literal; the server must request
	// continuation before the bytes, and the bytes are taken verbatim.
	toks, cont := lex(t, "a LOGIN {5}\r\nadmin secret\r\n")
	if cont != "+ Ready for literal data\r\n" {
		t.Errorf("continuation = %q, want the literal-ready line", cont)
	}
	if len(toks) != 4 || toks[2].val != "admin" || !toks[2].literal {
		t.Fatalf("tokens = %+v, want literal admin at index 2", toks)
	}
	if toks[3].val != "secret" {
		t.Errorf("trailing token = %q, want secret", toks[3].val)
	}
}

func TestLexNonSynchronizingLiteral(t *testing.T) {
	// {n+} must NOT trigger a continuation request, and may carry CRLF bytes.
	toks, cont := lex(t, "a APPEND INBOX {6+}\r\nab\r\ncd\r\n")
	if cont != "" {
		t.Errorf("non-sync literal wrote a continuation %q, want none", cont)
	}
	if len(toks) != 4 {
		t.Fatalf("tokens = %+v, want 4", toks)
	}
	if toks[3].val != "ab\r\ncd" || !toks[3].literal {
		t.Errorf("literal = %q (literal=%v), want \"ab\\r\\ncd\"", toks[3].val, toks[3].literal)
	}
}

func TestLexQuotedEscapes(t *testing.T) {
	toks, _ := lex(t, `a LOGIN "she said \"hi\"" "back\\slash"`+"\r\n")
	if toks[2].kind != tString || toks[2].val != `she said "hi"` {
		t.Errorf("quoted = %+v, want she said \"hi\"", toks[2])
	}
	if toks[3].val != `back\slash` {
		t.Errorf("quoted backslash = %q, want back\\slash", toks[3].val)
	}
}
