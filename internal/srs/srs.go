// Package srs rewrites the envelope sender of mail this server forwards with the
// Sender Rewriting Scheme (SRS0), so the next hop's SPF check sees a domain this
// server is allowed to send for, and turns the rewritten address back into the
// original when a bounce comes back to it.
//
// The address is SRS0=HHHHHHHH=TT=domain=local@forwarder: HHHHHHHH is a keyed
// hash that stops anyone else from minting addresses that relay through this
// server, and TT is the day of the rewrite, so an address stops working after
// MaxAge.
package srs

import (
	"crypto/hmac"
	"crypto/sha1" // SRS0 names HMAC-SHA1; HMAC does not rely on SHA-1 collision resistance
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	prefix     = "SRS0="
	hashLen    = 8
	maxLocal   = 64 // RFC 5321 section 4.5.3.1.1
	dayPeriod  = 1024
	tsAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
)

// MaxAge is how long a rewritten address keeps working for a bounce.
const MaxAge = 21 * 24 * time.Hour

// Errors Reverse returns.
var (
	ErrNotSRS  = errors.New("srs: not a rewritten address")
	ErrBadHash = errors.New("srs: the hash does not match")
	ErrExpired = errors.New("srs: the address has expired")
)

// Forward rewrites addr as an SRS0 address at forwarder. It returns addr
// unchanged when there is nothing to rewrite (the null sender), when addr has no
// domain, or when the result would exceed the local-part limit.
func Forward(addr, forwarder string, secret []byte, now time.Time) (string, error) {
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return addr, nil
	}
	local, domain := addr[:at], addr[at+1:]
	ts := timestamp(now)
	h, err := hash(secret, ts, domain, local)
	if err != nil {
		return "", err
	}
	out := prefix + h + "=" + ts + "=" + domain + "=" + local
	if len(out) > maxLocal {
		return addr, nil
	}
	return out + "@" + forwarder, nil
}

// IsSRS reports whether addr has the form of an SRS0 address, in any case.
func IsSRS(addr string) bool {
	return len(addr) > len(prefix) && strings.EqualFold(addr[:len(prefix)], prefix) && strings.Contains(addr, "@")
}

// Reverse returns the original address an SRS0 address stands for. The hash
// and the day are checked; the address is matched without regard to case,
// since a bounce may come back in another case.
func Reverse(addr string, secret []byte, now time.Time) (string, error) {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 || len(addr[:at]) < len(prefix) || !strings.EqualFold(addr[:len(prefix)], prefix) {
		return "", ErrNotSRS
	}
	parts := strings.SplitN(addr[len(prefix):at], "=", 4)
	if len(parts) != 4 || parts[2] == "" || parts[3] == "" {
		return "", ErrNotSRS
	}
	h, ts, domain, local := parts[0], parts[1], parts[2], parts[3]
	want, err := hash(secret, ts, domain, local)
	if err != nil {
		return "", err
	}
	if !hmac.Equal([]byte(strings.ToLower(h)), []byte(strings.ToLower(want))) {
		return "", ErrBadHash
	}
	if !fresh(ts, now) {
		return "", ErrExpired
	}
	return local + "@" + domain, nil
}

// hash is the truncated HMAC-SHA1 of the timestamp and the original address,
// all in lower case so a case change on the way back still verifies.
func hash(secret []byte, ts, domain, local string) (string, error) {
	m := hmac.New(sha1.New, secret)
	if _, err := m.Write([]byte(strings.ToLower(ts + domain + local))); err != nil {
		return "", fmt.Errorf("srs: hash the address: %w", err)
	}
	return base64.StdEncoding.EncodeToString(m.Sum(nil))[:hashLen], nil
}

// timestamp is the day number modulo 1024 in two base32 characters.
func timestamp(now time.Time) string {
	d := day(now)
	return string([]byte{tsAlphabet[d>>5], tsAlphabet[d&31]})
}

// fresh reports whether a timestamp lies within MaxAge before now. A day in the
// future reads as a large age modulo the period and is refused.
func fresh(ts string, now time.Time) bool {
	if len(ts) != 2 {
		return false
	}
	hi := strings.IndexByte(tsAlphabet, upper(ts[0]))
	lo := strings.IndexByte(tsAlphabet, upper(ts[1]))
	if hi < 0 || lo < 0 {
		return false
	}
	age := (day(now) - (hi<<5 | lo) + dayPeriod) % dayPeriod
	return age <= int(MaxAge/(24*time.Hour))
}

func day(now time.Time) int {
	return int(now.Unix()/86400) % dayPeriod
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}
