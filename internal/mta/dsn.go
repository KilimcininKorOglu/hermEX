package mta

import "strings"

// NotifyFailureWanted reports whether a failure delivery-status notification
// should be generated for a recipient, given its RFC 3461 NOTIFY value as
// received on RCPT TO (e.g. "NEVER" or "SUCCESS,DELAY"). A failure DSN is wanted
// when NOTIFY was absent (empty), in which case the pre-DSN default is to bounce
// on failure, or when the value explicitly lists FAILURE. It is suppressed for
// NEVER and for any value that omits FAILURE (e.g. SUCCESS,DELAY): the sender
// asked for no failure notice, and emitting one anyway is backscatter. Matching
// is case-insensitive.
func NotifyFailureWanted(notify string) bool {
	if strings.TrimSpace(notify) == "" {
		return true
	}
	for elem := range strings.SplitSeq(notify, ",") {
		if strings.EqualFold(strings.TrimSpace(elem), "FAILURE") {
			return true
		}
	}
	return false
}
