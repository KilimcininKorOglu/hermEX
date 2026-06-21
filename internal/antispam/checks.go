package antispam

import (
	"bytes"
	"net"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
)

// realSPF evaluates SPF for the connecting client and maps the RFC 7208 result to
// an AuthResult. The library's error is advisory (it can be non-nil even on a
// successful check), so only the Result drives the verdict.
func realSPF(ip net.IP, helo, mailFrom string) AuthResult {
	res, _ := spf.CheckHostWithSender(ip, helo, mailFrom)
	switch res {
	case spf.Pass:
		return AuthPass
	case spf.Fail:
		return AuthFail
	case spf.SoftFail:
		return AuthSoftFail
	case spf.Neutral:
		return AuthNeutral
	case spf.None:
		return AuthNone
	default: // TempError, PermError
		return AuthError
	}
}

// realDKIM verifies the message's DKIM signatures and returns each signature's
// claiming domain with whether it validated. A parse error yields no results, so
// the scorer treats the message as unsigned.
func realDKIM(raw []byte) []DKIMResult {
	vs, err := dkim.Verify(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	out := make([]DKIMResult, 0, len(vs))
	for _, v := range vs {
		out = append(out, DKIMResult{Domain: v.Domain, Valid: v.Err == nil})
	}
	return out
}
