// Package mime parses RFC 5322 / MIME messages into the structures the mail
// protocols need: the message envelope (the addressing and identification
// headers IMAP's ENVELOPE response is built from, see ParseEnvelope) and the
// full MIME body tree with byte-exact section extraction (ParseStructure,
// Part, and Section, which back IMAP's BODYSTRUCTURE and BODY[...] fetches).
// Parsing preserves the source bytes verbatim — no transfer decoding, no
// line-ending normalization — so fetched sections match what was stored.
package mime

import (
	"bytes"
	stdmime "mime"
	"net/mail"
	"strings"
	"time"
)

// Address is one parsed mailbox: an optional display name plus the local part
// and domain of the address, matching the shape of an IMAP ENVELOPE address.
type Address struct {
	Name    string // display name, RFC 2047 decoded
	Mailbox string // local part, before '@'
	Host    string // domain, after '@'
}

// Envelope holds the RFC 5322 envelope headers (the IMAP ENVELOPE fields).
type Envelope struct {
	Date      time.Time
	Subject   string
	From      []Address
	Sender    []Address
	ReplyTo   []Address
	To        []Address
	Cc        []Address
	Bcc       []Address
	InReplyTo string
	MessageID string
}

var wordDecoder = new(stdmime.WordDecoder)

// ParseEnvelope reads the headers of a raw RFC 5322 message and returns its
// envelope. Per RFC 3501, Sender and Reply-To default to From when absent.
func ParseEnvelope(raw []byte) (*Envelope, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	h := msg.Header
	env := &Envelope{
		Subject:   decodeWord(h.Get("Subject")),
		InReplyTo: h.Get("In-Reply-To"),
		MessageID: h.Get("Message-ID"),
		From:      parseAddrList(h.Get("From")),
		Sender:    parseAddrList(h.Get("Sender")),
		ReplyTo:   parseAddrList(h.Get("Reply-To")),
		To:        parseAddrList(h.Get("To")),
		Cc:        parseAddrList(h.Get("Cc")),
		Bcc:       parseAddrList(h.Get("Bcc")),
	}
	if d, err := h.Date(); err == nil {
		env.Date = d
	}
	if len(env.Sender) == 0 {
		env.Sender = env.From
	}
	if len(env.ReplyTo) == 0 {
		env.ReplyTo = env.From
	}
	return env, nil
}

// parseAddrList parses an address-list header on a best-effort basis: a
// malformed list yields no addresses rather than failing the whole parse.
func parseAddrList(s string) []Address {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parsed, err := mail.ParseAddressList(s)
	if err != nil {
		return nil
	}
	out := make([]Address, 0, len(parsed))
	for _, a := range parsed {
		mbox, host, _ := strings.Cut(a.Address, "@")
		out = append(out, Address{Name: a.Name, Mailbox: mbox, Host: host})
	}
	return out
}

// decodeWord decodes RFC 2047 encoded-words, returning the input unchanged if
// it is not encoded or cannot be decoded.
func decodeWord(s string) string {
	if d, err := wordDecoder.DecodeHeader(s); err == nil {
		return d
	}
	return s
}
