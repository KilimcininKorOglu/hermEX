package imap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxLiteralSize caps a single IMAP literal so a hostile client cannot force an
// unbounded allocation. It is generous enough for ordinary mail APPENDs.
const maxLiteralSize = 50 << 20 // 50 MiB

// errProtocol marks a malformed command line (a client/syntax error), as
// distinct from an I/O error on the connection.
var errProtocol = errors.New("imap: protocol error")

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

// commandReader lexes whole IMAP command lines off a connection, resolving
// literals inline (issuing a continuation request for synchronizing literals).
type commandReader struct {
	br *bufio.Reader
	bw *bufio.Writer
}

// readCommand reads and lexes one command line into a flat token slice. The
// terminating CRLF is consumed and not emitted. An empty line yields no tokens.
func (r *commandReader) readCommand() ([]token, error) {
	var toks []token
	for {
		b, err := r.br.ReadByte()
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
		case '(':
			toks = append(toks, token{kind: tLParen})
		case ')':
			toks = append(toks, token{kind: tRParen})
		case '[':
			toks = append(toks, token{kind: tLBracket})
		case ']':
			toks = append(toks, token{kind: tRBracket})
		case '"':
			s, err := r.readQuoted()
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tString, val: s})
		case '{':
			s, err := r.readLiteral()
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tString, val: s, literal: true})
		default:
			if err := r.br.UnreadByte(); err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tAtom, val: r.readAtom()})
		}
	}
}

// readLine reads one raw CRLF-terminated line and returns it without the
// terminator. It is used for SASL continuation data (a bare base64 line), which
// is not tokenized as a command.
func (r *commandReader) readLine() (string, error) {
	line, err := r.br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
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
func (r *commandReader) readAtom() string {
	var sb strings.Builder
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			return sb.String()
		}
		if isAtomDelimiter(b) {
			r.br.UnreadByte()
			return sb.String()
		}
		sb.WriteByte(b)
	}
}

// readQuoted reads a quoted string body after the opening quote, honoring the
// \\ and \" escapes. A quoted string may not span lines.
func (r *commandReader) readQuoted() (string, error) {
	var sb strings.Builder
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			return "", err
		}
		switch b {
		case '"':
			return sb.String(), nil
		case '\\':
			esc, err := r.br.ReadByte()
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
	var numSb strings.Builder
	nonSync := false
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '}' {
			break
		}
		if b == '+' {
			nonSync = true
			continue
		}
		if b < '0' || b > '9' {
			return "", fmt.Errorf("%w: bad literal length", errProtocol)
		}
		if nonSync {
			return "", fmt.Errorf("%w: digit after '+' in literal", errProtocol)
		}
		numSb.WriteByte(b)
	}
	n, err := strconv.Atoi(numSb.String())
	if err != nil || n < 0 {
		return "", fmt.Errorf("%w: bad literal length", errProtocol)
	}
	if n > maxLiteralSize {
		return "", fmt.Errorf("%w: literal of %d bytes exceeds limit", errProtocol, n)
	}
	if b, err := r.br.ReadByte(); err != nil {
		return "", err
	} else if b == '\r' {
		if err := r.expectLF(); err != nil {
			return "", err
		}
	} else if b != '\n' {
		return "", fmt.Errorf("%w: literal length not followed by CRLF", errProtocol)
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
