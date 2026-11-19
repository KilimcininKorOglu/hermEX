package arc

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrNotSealed reports that a message was left without a new set: its chain
// already failed at an earlier hop, or it already holds the most sets allowed.
var ErrNotSealed = errors.New("arc: the chain cannot take another set")

// ErrUnsupportedKey reports a sealing key other than RSA. RFC 8617 leaves more
// than one algorithm to a later document, and common verifiers accept only
// rsa-sha256 in an ARC set, so an Ed25519 seal would read as a broken chain.
var ErrUnsupportedKey = errors.New("arc: sealing needs an RSA key")

// Sealer adds this server's ARC set to a message it forwards.
type Sealer struct {
	Domain     string        // d= of the message signature and the seal
	Selector   string        // s=, the key record under <selector>._domainkey.<domain>
	Key        crypto.Signer // RSA or Ed25519 private key
	AuthServID string        // authserv-id of the Authentication-Results this server wrote
	Lookup     LookupTXT     // resolves the keys of earlier sealers
}

// signedFields are the header fields the message signature covers when present
// (RFC 6376 section 5.4.1). ARC and Authentication-Results fields are never
// signed, since later hops add and remove them.
var signedFields = []string{"from", "to", "cc", "subject", "date", "message-id", "reply-to",
	"in-reply-to", "references", "mime-version", "content-type", "content-transfer-encoding", "dkim-signature"}

// Seal returns raw with a new ARC set on top (RFC 8617 section 5.1). The chain
// already present is validated to set the seal's cv; a chain whose newest seal
// says fail is not extended and ErrNotSealed is returned with raw unchanged.
func (s Sealer) Seal(raw []byte, now time.Time) ([]byte, error) {
	if _, ok := s.Key.Public().(*rsa.PublicKey); !ok {
		return nil, ErrUnsupportedKey
	}
	return s.seal(raw, now)
}

// seal adds the set with the algorithm of the key. Seal limits it to RSA; the
// package tests also call it with an Ed25519 key, to build the kind of chain
// another sealer may write and this verifier must accept.
func (s Sealer) seal(raw []byte, now time.Time) ([]byte, error) {
	algo, err := algorithm(s.Key)
	if err != nil {
		return nil, err
	}
	fields, body := splitMessage(raw)
	sets, ok := collectSets(fields)
	if !extendable(sets) {
		return raw, ErrNotSealed
	}
	cv := chainStatus(fields, body, sets, ok, s.Lookup)
	next := arcSet{aar: s.resultsField(fields, len(sets)+1)}
	common := "i=" + strconv.Itoa(len(sets)+1) + "; a=" + algo + "; d=" + s.Domain + "; s=" + s.Selector +
		"; t=" + strconv.FormatInt(now.Unix(), 10)
	if next.ams, err = s.messageSignature(fields, body, common); err != nil {
		return nil, err
	}
	next.as = hdrAS + ": " + common + "; cv=" + cv + "; b=\r\n"
	sig, err := sign(s.Key, []byte(sealInput(sets, next)))
	if err != nil {
		return nil, err
	}
	next.as = strings.TrimSuffix(next.as, "\r\n") + sig + "\r\n"
	out := make([]byte, 0, len(next.as)+len(next.ams)+len(next.aar)+len(raw))
	out = append(out, next.as...)
	out = append(out, next.ams...)
	out = append(out, next.aar...)
	return append(out, raw...), nil
}

// extendable reports whether a chain may take another set: not when its newest
// seal already says fail (RFC 8617 section 5.1.2), and not when it holds the
// most sets allowed (section 4.2.1).
func extendable(sets []arcSet) bool {
	if len(sets) >= maxSets {
		return false
	}
	return len(sets) == 0 || tagList(fieldValue(sets[len(sets)-1].as))["cv"] != cvFail
}

// resultsField builds the ARC-Authentication-Results of instance i from the
// Authentication-Results this server wrote (RFC 8617 section 4.1.1). A message
// with none records that no results were available.
func (s Sealer) resultsField(fields []field, i int) string {
	payload := s.AuthServID + "; none"
	for _, f := range fields {
		if !strings.EqualFold(f.name, "Authentication-Results") {
			continue
		}
		v := strings.Join(strings.Fields(fieldValue(f.raw)), " ")
		id, _, _ := strings.Cut(v, ";")
		if first := strings.Fields(id); len(first) > 0 && strings.EqualFold(first[0], s.AuthServID) {
			payload = v
			break
		}
	}
	return hdrAAR + ": i=" + strconv.Itoa(i) + "; " + payload + "\r\n"
}

// messageSignature builds the ARC-Message-Signature: a DKIM signature without
// v=, over the present signedFields and the body, relaxed/relaxed (RFC 8617
// section 4.1.2).
func (s Sealer) messageSignature(fields []field, body []byte, common string) (string, error) {
	var names []string
	for _, n := range signedFields {
		for _, f := range fields {
			if strings.EqualFold(f.name, n) {
				names = append(names, n)
			}
		}
	}
	sum := sha256.Sum256(relaxedBody(body))
	field := hdrAMS + ": " + common + "; c=relaxed/relaxed; h=" + strings.Join(names, ":") +
		"; bh=" + base64.StdEncoding.EncodeToString(sum[:]) + "; b=\r\n"
	data := signedHeaders(fields, names, relaxedHeader) + strings.TrimSuffix(relaxedHeader(field), "\r\n")
	sig, err := sign(s.Key, []byte(data))
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(field, "\r\n") + sig + "\r\n", nil
}

// simpleHeader is the simple header canonicalization: the field unchanged.
func simpleHeader(raw string) string { return raw }

// simpleBody is the simple body canonicalization (RFC 6376 section 3.4.3):
// empty lines at the end removed and the body ended with one CRLF; an empty
// body becomes a single CRLF.
func simpleBody(body []byte) []byte {
	b := strings.ReplaceAll(string(body), "\r\n", "\n")
	b = strings.TrimRight(b, "\n")
	return []byte(strings.ReplaceAll(b, "\n", "\r\n") + "\r\n")
}
