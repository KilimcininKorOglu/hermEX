package smtp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// buildReceived formats the RFC 5321 §4.4 trace header stamped at DATA time,
// matching the reference MDA form: "from <helo> (<rdns> [<ip>]) by <host> with
// <SMTP|SMTPS>; <date>". The protocol keyword is SMTP, with an S suffix when the
// link is TLS (RFC 3848); the reference does not distinguish EHLO or record AUTH in
// this token. rdns is the reverse-resolved name of the client address (reported as
// "unknown" when it did not resolve); an IPv6 client address carries the "IPv6:"
// address-literal tag. The header is CRLF-terminated and folds onto continuation
// lines, matching the dot-decoded body it is prepended to. helo is recorded
// verbatim (it cannot carry a CRLF, arriving on a single command line); an empty
// value is reported as "unknown".
func buildReceived(helo, remoteAddr, rdns, hostname string, tls bool, now time.Time) string {
	with := "SMTP"
	if tls {
		with = "SMTPS"
	}
	ip := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = host
	}
	ipTag := ip
	if strings.Contains(ip, ":") {
		ipTag = "IPv6:" + ip
	}
	from := helo
	if from == "" {
		from = "unknown"
	}
	rev := rdns
	if rev == "" {
		rev = "unknown"
	}
	date := now.Format("Mon, 02 Jan 2006 15:04:05 -0700")
	return fmt.Sprintf("Received: from %s (%s [%s])\r\n\tby %s with %s;\r\n\t%s\r\n",
		from, rev, ipTag, hostname, with, date)
}

// lookupRDNS reverse-resolves the client address to a hostname for the Received
// header's from-clause, the way the reference records the connection's resolved
// domain. The lookup is bounded by a short timeout and degrades to "" (reported as
// "unknown") on failure, so a slow or missing PTR record cannot stall delivery.
func lookupRDNS(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, host)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}
