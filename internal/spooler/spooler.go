// Package spooler releases scheduled (send-later) messages when their time
// arrives. A scheduled send is a message in a mailbox's Outbox carrying
// PrDeferredSendTime; ProcessDueOutbox scans the Outbox, delivers each message
// whose time has come, files a copy to Sent, and removes it from the Outbox. The
// worker logic is pure and delivery is injected, so it is host-agnostic and
// testable without a transport.
package spooler

import (
	"bytes"
	"errors"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// DeliverFunc delivers a message to its recipients, returning any addresses that
// could not be delivered locally (there is no external relay yet) and a transport
// error. It mirrors mta.Deliver with the account directory already bound, so the
// spooler need not depend on the transport package.
type DeliverFunc func(recipients []string, raw []byte, when time.Time) (unresolved []string, err error)

// ProcessDueOutbox releases every Outbox message whose deferred-send time has
// arrived: it recovers the message's recipients (To, Cc, and the blind Bcc) from
// the stored object, delivers the wire copy with the Bcc header stripped (the
// blind list must never reach the wire), files the with-Bcc copy to Sent, and
// then removes it from the Outbox. It returns the number released.
//
// Messages without a deferred-send time, or whose time has not yet come, are left
// untouched. Ordering is deliver -> file -> remove, so a crash between delivery
// and removal re-delivers on the next scan (at-least-once; local-only and
// bounded). A delivery or store failure leaves the message in the Outbox to retry
// and is reported (joined) so the caller can log it, without stopping the batch.
func ProcessDueOutbox(st *objectstore.Store, deliver DeliverFunc, now time.Time) (released int, err error) {
	outbox := int64(mapi.PrivateFIDOutbox)
	msgs, err := st.ListMessages(outbox)
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, m := range msgs {
		due, scheduled, e := deferredSendDue(st, m.ID, now)
		if e != nil {
			errs = append(errs, e)
			continue
		}
		if !scheduled || !due {
			continue
		}
		if e := releaseMessage(st, deliver, outbox, m, now); e != nil {
			errs = append(errs, e)
			continue
		}
		released++
	}
	return released, errors.Join(errs...)
}

// releaseMessage delivers one due message and, only on success, files it to Sent
// and removes it from the Outbox. Any error leaves the message in place to retry.
func releaseMessage(st *objectstore.Store, deliver DeliverFunc, outbox int64, m objectstore.MessageInfo, now time.Time) error {
	full, err := st.OpenMessage(m.ID)
	if err != nil {
		return err
	}
	recipients := recipientAddrs(full)
	raw, err := st.GetMessageRaw(outbox, m.UID)
	if err != nil {
		return err
	}
	// Deliver with the Bcc header removed; the unresolved list is ignored (no
	// external relay yet, the same as the interactive compose path).
	if _, err := deliver(recipients, stripBcc(raw), now); err != nil {
		return err
	}
	// Delivered: keep the with-Bcc copy in Sent for the record, then clear the
	// Outbox. If filing fails the Outbox copy stays and re-delivers next scan.
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, now, objectstore.FlagSeen); err != nil {
		return err
	}
	return st.DeleteMessage(outbox, m.UID)
}

// deferredSendDue reports whether a message carries a deferred-send time
// (scheduled) and, if so, whether that time has arrived (due) as of now.
func deferredSendDue(st *objectstore.Store, messageID int64, now time.Time) (due, scheduled bool, err error) {
	props, err := st.GetMessageProperties(messageID, mapi.PrDeferredSendTime)
	if err != nil {
		return false, false, err
	}
	v, ok := props.Get(mapi.PrDeferredSendTime)
	if !ok {
		return false, false, nil
	}
	nt, ok := v.(uint64)
	if !ok {
		return false, false, nil
	}
	when := mapi.NTTimeToUnix(nt)
	return !when.After(now), true, nil
}

// recipientAddrs collects the SMTP address of every recipient — To, Cc, and Bcc
// alike — since all must receive the message. The Bcc recipients survive here
// because the stored object keeps every recipient bag; only the delivered wire
// copy has the Bcc header stripped.
func recipientAddrs(msg *oxcmail.Message) []string {
	var out []string
	for _, r := range msg.Recipients {
		if v, ok := r.Get(mapi.PrSmtpAddress); ok {
			if addr, ok := v.(string); ok && addr != "" {
				out = append(out, addr)
			}
		}
	}
	return out
}

// stripBcc removes the Bcc header field (and any folded continuation lines) from
// a message's top-level header block, so the blind-copy list never reaches the
// wire. It is the inverse of the webmail compose's Bcc splice; the stored
// Outbox/Sent copy keeps Bcc, only the delivered bytes have it removed. The input
// is a well-formed CRLF message (oxcmail's re-synthesized wire form); a message
// with no header/body separator is returned unchanged.
func stripBcc(raw []byte) []byte {
	sep := []byte("\r\n\r\n")
	i := bytes.Index(raw, sep)
	if i < 0 {
		return raw
	}
	header, body := raw[:i], raw[i:]
	var kept [][]byte
	dropping := false
	for line := range bytes.SplitSeq(header, []byte("\r\n")) {
		// A folded continuation (leading SP/HTAB) belongs to the previous field;
		// drop it too while dropping a Bcc field.
		if dropping && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			continue
		}
		dropping = false
		if isBccField(line) {
			dropping = true
			continue
		}
		kept = append(kept, line)
	}
	return append(bytes.Join(kept, []byte("\r\n")), body...)
}

// isBccField reports whether a header line begins the Bcc field (the field name
// up to the colon is "Bcc", case-insensitively).
func isBccField(line []byte) bool {
	name, _, found := bytes.Cut(line, []byte(":"))
	if !found {
		return false
	}
	return strings.EqualFold(string(bytes.TrimSpace(name)), "Bcc")
}
