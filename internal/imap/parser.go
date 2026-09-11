package imap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"

	"hermex/internal/netline"
)

// defaultMaxLiteralSize caps a single IMAP literal so a hostile client cannot force an
// unbounded allocation. It is generous enough for ordinary mail APPENDs, and is the
// fallback when no operator limit has been set.
const defaultMaxLiteralSize = 50 << 20 // 50 MiB

// defaultMaxCommandLine caps ONE command line so a client that never sends a line
// terminator cannot grow the server's memory without limit. It is the fallback when
// no operator limit has been set, and is generous: a long UID set or SEARCH key is
// legitimate. A literal's payload is bounded separately by the literal cap.
const defaultMaxCommandLine = 64 << 10 // 64 KiB

// errProtocol marks a malformed command line (a client/syntax error), as
// distinct from an I/O error on the connection.
var errProtocol = errors.New("imap: protocol error")

// errLineTooLong marks a command line past the cap. The rest of the line has
// already been discarded, so the caller answers BAD and keeps serving.
var errLineTooLong = fmt.Errorf("%w: command line too long", errProtocol)

// tokenKind classifies a lexed command token.
type tokenKind uint8

const (
	tAtom     tokenKind = iota // unquoted word: keyword, number, flag, sequence set
	tString                    // a string value (quoted string or literal)
	tLParen                    // (
	tRParen                    // )
	tLBracket                  // [
	tRBracket                  // ]
)

// token is one lexed element of a command line. literal records whether a
// tString arrived as a literal (and so may carry 8-bit/binary octets).
type token struct {
	kind    tokenKind
	val     string
	literal bool
}

// isAtom reports whether t is an atom equal to want, case-insensitively (IMAP
// keywords are case-insensitive).
func (t token) isAtom(want string) bool {
	return t.kind == tAtom && strings.EqualFold(t.val, want)
}

// str returns the token's textual value for an atom or string token, and
// reports ok=false for the structural delimiters.
func (t token) str() (string, bool) {
	if t.kind == tAtom || t.kind == tString {
		return t.val, true
	}
	return "", false
}

// tokenCursor walks a lexed token slice during per-command argument parsing.
type tokenCursor struct {
	toks []token
	i    int
}

func (c *tokenCursor) empty() bool { return c.i >= len(c.toks) }

// peek returns the current token without advancing.
func (c *tokenCursor) peek() (token, bool) {
	if c.empty() {
		return token{}, false
	}
	return c.toks[c.i], true
}

// next returns the current token and advances past it.
func (c *tokenCursor) next() (token, bool) {
	t, ok := c.peek()
	if ok {
		c.i++
	}
	return t, ok
}

// commandReader lexes whole IMAP command lines off a connection, resolving
// literals inline (issuing a continuation request for synchronizing literals).
type commandReader struct {
	br *bufio.Reader
	bw *bufio.Writer
	// maxLiteral points at the server's live literal-size cap (bytes); nil or a
	// non-positive value means use defaultMaxLiteralSize. Read live in readLiteral so
	// an operator's edit applies to an existing connection on its next literal.
	maxLiteral *atomic.Int64
	// maxLine points at the server's live command-line cap (bytes); nil or a
	// non-positive value means use defaultMaxCommandLine. Read live at the start of
	// each command, so an operator's edit applies to an existing connection.
	maxLine *atomic.Int64
	// lineBytes counts the bytes of the command line read so far, reset per command.
	// A literal's payload is not counted: it carries its own cap.
	lineBytes int
}

// readByte reads one byte of the command line and counts it against the line cap.
// It returns errLineTooLong once the line passes the cap, after discarding the rest
// of the line, so the connection stays framed for the next command.
func (r *commandReader) readByte() (byte, error) {
	b, err := r.br.ReadByte()
	if err != nil {
		return 0, err
	}
	r.lineBytes++
	if limit := r.lineLimit(); r.lineBytes > limit {
		if derr := discardLine(r.br); derr != nil {
			return 0, derr
		}
		return 0, errLineTooLong
	}
	return b, nil
}

// lineLimit is the command-line cap in force right now.
func (r *commandReader) lineLimit() int {
	if r.maxLine != nil {
		if n := r.maxLine.Load(); n > 0 {
			return int(n)
		}
	}
	return defaultMaxCommandLine
}

// discardLine reads and drops bytes up to and including the next LF.
func discardLine(br *bufio.Reader) error {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		if b == '\n' {
			return nil
		}
	}
}

// readCommand reads and lexes one command line into a flat token slice. The
// terminating CRLF is consumed and not emitted. An empty line yields no tokens.
func (r *commandReader) readCommand() ([]token, error) {
	var toks []token
	r.lineBytes = 0
	for {
		b, err := r.readByte()
		if err != nil {
			return nil, err
		}
		switch b {
		case '\r':
			if err := r.expectLF(); err != nil {
				return nil, err
			}
			return toks, nil
		case '\n':
			// A bare LF terminates the line too (lenient toward clients).
			return toks, nil
		case ' ':
			// Field separator; collapse runs of spaces.
		default:
			t, err := r.readToken(b)
			if err != nil {
				return nil, err
			}
			toks = append(toks, t)
		}
	}
}

// punctuationTokens are the single-byte structural tokens of the IMAP grammar.
var punctuationTokens = map[byte]tokenKind{
	'(': tLParen,
	')': tRParen,
	'[': tLBracket,
	']': tRBracket,
}

// readToken reads one token whose first byte the caller already consumed.
func (r *commandReader) readToken(b byte) (token, error) {
	if kind, ok := punctuationTokens[b]; ok {
		return token{kind: kind}, nil
	}
	switch b {
	case '"':
		s, err := r.readQuoted()
		if err != nil {
			return token{}, err
		}
		return token{kind: tString, val: s}, nil
	case '{':
		s, err := r.readLiteral()
		if err != nil {
			return token{}, err
		}
		return token{kind: tString, val: s, literal: true}, nil
	}
	if err := r.br.UnreadByte(); err != nil {
		return token{}, err
	}
	r.lineBytes-- // the byte is counted again when readAtom re-reads it
	atom, err := r.readAtom()
	if err != nil {
		return token{}, err
	}
	return token{kind: tAtom, val: atom}, nil
}

// readLine reads one raw CRLF-terminated line and returns it without the
// terminator. It is used for SASL continuation data (a bare base64 line), which
// is not tokenized as a command. It carries the same cap as a command line: the
// client reaches this reader before it authenticates.
func (r *commandReader) readLine() (string, error) {
	line, err := netline.ReadLine(r.br, r.lineLimit())
	if errors.Is(err, netline.ErrTooLong) {
		return "", errLineTooLong
	}
	return line, err
}

// expectLF consumes the LF following a CR.
func (r *commandReader) expectLF() error {
	b, err := r.br.ReadByte()
	if err != nil {
		return err
	}
	if b != '\n' {
		return fmt.Errorf("%w: CR not followed by LF", errProtocol)
	}
	return nil
}

// isAtomDelimiter reports whether b ends an atom. Atoms run up to a space, a
// structural delimiter, a quote/literal start, or the line terminator.
func isAtomDelimiter(b byte) bool {
	switch b {
	case ' ', '\r', '\n', '(', ')', '[', ']', '"', '{':
		return true
	}
	return false
}

// readAtom reads a maximal run of non-delimiter bytes. It is only called once a
// non-delimiter byte has been unread, so it always returns a non-empty atom.
func (r *commandReader) readAtom() (string, error) {
	var sb strings.Builder
	for {
		b, err := r.readByte()
		if err != nil {
			if errors.Is(err, errLineTooLong) {
				return "", err
			}
			return sb.String(), nil // end of stream: the atom is what was read
		}
		if isAtomDelimiter(b) {
			_ = r.br.UnreadByte() // the ReadByte above succeeded, so this cannot fail
			r.lineBytes--         // the delimiter is read again by the caller
			return sb.String(), nil
		}
		sb.WriteByte(b)
	}
}

// readQuoted reads a quoted string body after the opening quote, honoring the
// \\ and \" escapes. A quoted string may not span lines.
func (r *commandReader) readQuoted() (string, error) {
	var sb strings.Builder
	for {
		b, err := r.readByte()
		if err != nil {
			return "", err
		}
		switch b {
		case '"':
			return sb.String(), nil
		case '\\':
			esc, err := r.readByte()
			if err != nil {
				return "", err
			}
			sb.WriteByte(esc)
		case '\r', '\n':
			return "", fmt.Errorf("%w: CR/LF in quoted string", errProtocol)
		default:
			sb.WriteByte(b)
		}
	}
}

// readLiteral reads a literal after the opening brace: the byte count, an
// optional '+' (non-synchronizing, RFC 7888/2088), the closing brace and CRLF,
// then exactly count octets. For a synchronizing literal it first writes a
// command-continuation request so the client knows to send the data.
func (r *commandReader) readLiteral() (string, error) {
	n, nonSync, err := r.readLiteralHeader()
	if err != nil {
		return "", err
	}
	if int64(n) > r.literalLimit() {
		return "", fmt.Errorf("%w: literal of %d bytes exceeds limit", errProtocol, n)
	}
	if err := r.expectCRLF(); err != nil {
		return "", err
	}
	if !nonSync && r.bw != nil {
		if _, err := r.bw.WriteString("+ Ready for literal data\r\n"); err != nil {
			return "", err
		}
		if err := r.bw.Flush(); err != nil {
			return "", err
		}
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.br, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// readLiteralHeader reads the byte count up to the closing brace and reports
// whether the client marked the literal non-synchronizing (a trailing '+', which
// means it will not wait for the continuation response).
func (r *commandReader) readLiteralHeader() (n int, nonSync bool, err error) {
	var digits strings.Builder
	for {
		b, err := r.readByte()
		if err != nil {
			return 0, false, err
		}
		if b == '}' {
			break
		}
		if b == '+' {
			nonSync = true
			continue
		}
		if b < '0' || b > '9' {
			return 0, false, fmt.Errorf("%w: bad literal length", errProtocol)
		}
		if nonSync {
			return 0, false, fmt.Errorf("%w: digit after '+' in literal", errProtocol)
		}
		digits.WriteByte(b)
	}
	n, err = strconv.Atoi(digits.String())
	if err != nil || n < 0 {
		return 0, false, fmt.Errorf("%w: bad literal length", errProtocol)
	}
	return n, nonSync, nil
}

// literalLimit is the largest literal this connection accepts, the operator's
// setting when one is configured and the built-in default otherwise.
func (r *commandReader) literalLimit() int64 {
	if r.maxLiteral != nil {
		if v := r.maxLiteral.Load(); v > 0 {
			return v
		}
	}
	return int64(defaultMaxLiteralSize)
}

// expectCRLF consumes the line ending after a literal's length, tolerating a bare
// LF as the readers around it do.
func (r *commandReader) expectCRLF() error {
	b, err := r.br.ReadByte()
	if err != nil {
		return err
	}
	if b == '\r' {
		return r.expectLF()
	}
	if b != '\n' {
		return fmt.Errorf("%w: literal length not followed by CRLF", errProtocol)
	}
	return nil
}
