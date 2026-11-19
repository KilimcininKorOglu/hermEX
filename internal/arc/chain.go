package arc

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"slices"
	"strconv"
	"strings"
)

// maxSets is the most ARC sets a chain may hold (RFC 8617 section 4.2.1).
const maxSets = 50

// Chain validation status values (RFC 8617 section 4.4).
const (
	cvNone = "none"
	cvPass = "pass"
	cvFail = "fail"
)

// Header field names of an ARC set.
const (
	hdrAAR = "ARC-Authentication-Results"
	hdrAMS = "ARC-Message-Signature"
	hdrAS  = "ARC-Seal"
)

// arcSet is one instance's three fields, as raw header text.
type arcSet struct{ aar, ams, as string }

// collectSets gathers the ARC fields by their instance number. It reports false
// when a field has no valid instance, an instance repeats a field, or the
// instances are not exactly 1..n with all three fields each.
func collectSets(fields []field) ([]arcSet, bool) {
	byInstance := map[int]*arcSet{}
	for _, f := range fields {
		slot := setSlot(f.name)
		if slot == "" {
			continue
		}
		i, err := strconv.Atoi(tagList(fieldValue(f.raw))["i"])
		if err != nil || i < 1 || i > maxSets {
			return nil, false
		}
		if byInstance[i] == nil {
			byInstance[i] = &arcSet{}
		}
		if !byInstance[i].put(slot, f.raw) {
			return nil, false
		}
	}
	return orderedSets(byInstance)
}

// setSlot names which field of a set a header is, or "" for any other header.
func setSlot(name string) string {
	for _, h := range []string{hdrAAR, hdrAMS, hdrAS} {
		if strings.EqualFold(name, h) {
			return h
		}
	}
	return ""
}

// put stores raw in the slot, reporting false when the slot is already taken.
func (s *arcSet) put(slot, raw string) bool {
	target := map[string]*string{hdrAAR: &s.aar, hdrAMS: &s.ams, hdrAS: &s.as}[slot]
	if *target != "" {
		return false
	}
	*target = raw
	return true
}

// orderedSets returns the sets in instance order, reporting false when one is
// missing or incomplete.
func orderedSets(byInstance map[int]*arcSet) ([]arcSet, bool) {
	out := make([]arcSet, 0, len(byInstance))
	for i := 1; i <= len(byInstance); i++ {
		s := byInstance[i]
		if s == nil || s.aar == "" || s.ams == "" || s.as == "" {
			return nil, false
		}
		out = append(out, *s)
	}
	return out, true
}

// chainStatus validates an existing chain and returns its cv (RFC 8617 section
// 5.2): none with no sets, pass when every seal and the newest message
// signature verify and each seal carries the cv its position requires, and fail
// otherwise.
func chainStatus(fields []field, body []byte, sets []arcSet, ok bool, lookup LookupTXT) string {
	if !ok {
		return cvFail
	}
	if len(sets) == 0 {
		return cvNone
	}
	if !sealCVsValid(sets) {
		return cvFail
	}
	if verifyAMS(fields, body, sets[len(sets)-1].ams, lookup) != nil {
		return cvFail
	}
	for i := range sets {
		if verifySeal(sets, i, lookup) != nil {
			return cvFail
		}
	}
	return cvPass
}

// sealCVsValid checks each seal's cv: none on the first set, pass on every later
// one (RFC 8617 section 5.2, step 3).
func sealCVsValid(sets []arcSet) bool {
	for i, s := range sets {
		want := cvPass
		if i == 0 {
			want = cvNone
		}
		if tagList(fieldValue(s.as))["cv"] != want {
			return false
		}
	}
	return true
}

// verifyAMS checks a message signature against the message: the body hash, then
// the signature over the selected headers and the signature field itself.
func verifyAMS(fields []field, body []byte, ams string, lookup LookupTXT) error {
	tags := tagList(fieldValue(ams))
	headerCanon, bodyCanon, err := canonicalizations(tags["c"])
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bodyCanon(body))
	if base64.StdEncoding.EncodeToString(sum[:]) != tags["bh"] {
		return errors.New("arc: the body hash does not match")
	}
	data := signedHeaders(fields, strings.Split(tags["h"], ":"), headerCanon)
	data += strings.TrimSuffix(headerCanon(withoutSignature(ams)), "\r\n")
	pub, err := publicKey(lookup, tags["s"], tags["d"])
	if err != nil {
		return err
	}
	return verify(pub, tags["a"], tags["b"], []byte(data))
}

// verifySeal checks the seal of set i over the sets up to and including it.
func verifySeal(sets []arcSet, i int, lookup LookupTXT) error {
	tags := tagList(fieldValue(sets[i].as))
	pub, err := publicKey(lookup, tags["s"], tags["d"])
	if err != nil {
		return err
	}
	return verify(pub, tags["a"], tags["b"], []byte(sealInput(sets[:i], sets[i])))
}

// sealInput is what a seal signs (RFC 8617 section 5.1.1): every earlier set's
// three fields in instance order, then the new set's results, message signature
// and seal, the seal without its b= value and its final CRLF, all relaxed.
func sealInput(earlier []arcSet, last arcSet) string {
	var b strings.Builder
	for _, s := range earlier {
		b.WriteString(relaxedHeader(s.aar))
		b.WriteString(relaxedHeader(s.ams))
		b.WriteString(relaxedHeader(s.as))
	}
	b.WriteString(relaxedHeader(last.aar))
	b.WriteString(relaxedHeader(last.ams))
	b.WriteString(strings.TrimSuffix(relaxedHeader(withoutSignature(last.as)), "\r\n"))
	return b.String()
}

// signedHeaders returns the canonical form of the fields a signature's h= list
// names. Each name takes the bottom-most instance not yet used, and a name with
// no instance left contributes nothing (RFC 6376 section 5.4.2).
func signedHeaders(fields []field, names []string, canon func(string) string) string {
	used := map[string]int{}
	var b strings.Builder
	for _, n := range names {
		key := strings.ToLower(strings.TrimSpace(n))
		seen := 0
		for _, f := range slices.Backward(fields) {
			if strings.ToLower(f.name) != key {
				continue
			}
			if seen == used[key] {
				b.WriteString(canon(f.raw))
				used[key]++
				break
			}
			seen++
		}
	}
	return b.String()
}

// canonicalizations returns the header and body algorithms a c= tag names. A
// missing body half is simple, and a missing tag is simple/simple (RFC 6376
// section 3.5).
func canonicalizations(c string) (func(string) string, func([]byte) []byte, error) {
	h, bdy, _ := strings.Cut(c, "/")
	header := map[string]func(string) string{"": simpleHeader, "simple": simpleHeader, "relaxed": relaxedHeader}[h]
	body := map[string]func([]byte) []byte{"": simpleBody, "simple": simpleBody, "relaxed": relaxedBody}[bdy]
	if header == nil || body == nil {
		return nil, nil, errors.New("arc: unknown canonicalization " + c)
	}
	return header, body, nil
}
