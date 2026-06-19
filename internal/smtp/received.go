package smtp

import (
	"fmt"
	"net"
	"time"
)

// buildReceived formats the RFC 5321 §4.4 trace header that is stamped at the top
// of every message at DATA time. The "with" protocol keyword follows RFC 3848:
// SMTP for a HELO greeting, ESMTP for EHLO, an "S" suffix when the link is TLS and
// an "A" suffix when the session authenticated. hermEX requires TLS before AUTH,
// so an authenticated session is always ESMTPSA (never bare ESMTPA). The header is
// CRLF-terminated and folds onto continuation lines, matching the dot-decoded body
// it is prepended to.
//
// helo is the client-supplied HELO/EHLO argument (recorded verbatim, as every MTA
// does — it cannot carry a CRLF, since it arrived on a single command line); an
// empty value is reported as "unknown". remoteAddr is the connection's host:port.
func buildReceived(helo, remoteAddr, hostname string, esmtp, tls, authed bool, now time.Time) string {
	with := "SMTP"
	if esmtp {
		with = "ESMTP"
		if tls {
			with += "S"
		}
		if authed {
			with += "A"
		}
	}
	ip := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = host
	}
	from := helo
	if from == "" {
		from = "unknown"
	}
	// RFC 5322 date-time with a numeric zone (unambiguous across readers).
	date := now.Format("Mon, 02 Jan 2006 15:04:05 -0700")
	return fmt.Sprintf("Received: from %s ([%s])\r\n\tby %s with %s;\r\n\t%s\r\n",
		from, ip, hostname, with, date)
}
