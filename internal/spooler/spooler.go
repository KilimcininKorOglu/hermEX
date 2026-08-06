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
	"fmt"
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

// GiveUpFunc reports a scheduled send the spooler has abandoned after
// maxReleaseAttempts consecutive failures: the wire copy, the recipients it never
// reached, and the last failure. The caller returns a non-delivery report to the
// sender, the same way relay.Worker's OnGiveUp does for an abandoned external
// recipient. It may be nil, which leaves the abandonment silent beyond the
// returned error.
type GiveUpFunc func(raw []byte, recipients []string, cause error)

// maxReleaseAttempts bounds how many consecutive failures one scheduled message
// may cause before the spooler stops trying to release it. It matches the relay
// worker's default attempt budget; at the MTA's 30-second sweep it is about five
// minutes of unbroken failure, long enough to ride out a blip and short enough
// that a message that can never be released does not retry forever.
//
// The budget covers the local steps only (reading the message, handing it to
// delivery, clearing the Outbox). Once delivery accepts the message the relay
// spool owns the retries, with its own operator-tunable budget.
const maxReleaseAttempts = 10

// nameReleaseAttempts is the private named property counting one message's
// consecutive release failures. It is hermEX bookkeeping, not part of any wire
// format, so it lives here rather than in the MS property model in internal/mapi.
// It never reaches a client: the delivered bytes come from the message's stored
// wire copy, and the property rides only the Outbox object, which is deleted or
// rewritten the moment the message leaves.
var nameReleaseAttempts = mapi.PropertyName{
	Kind: mapi.MnidString,
	GUID: mapi.GUID{Data1: 0x6d8c1f2a, Data2: 0x4b73, Data3: 0x4d31,
		Data4: [8]byte{0x9e, 0x55, 0x0a, 0x1c, 0x77, 0xb4, 0xe2, 0x30}},
	Name: "SendLaterReleaseAttempts",
}

// ProcessDueOutbox releases every Outbox message whose deferred-send time has
// arrived: it recovers the message's recipients (To, Cc, and the blind Bcc) from
// the stored object, delivers the wire copy with the Bcc header stripped (the
// blind list must never reach the wire), files the with-Bcc copy to Sent, and
// then removes it from the Outbox. It returns the number released.
//
// Messages without a deferred-send time, or whose time has not yet come, are left
// untouched. Ordering is deliver -> file -> remove, so a crash between delivery
// and removal re-delivers on the next scan (at-least-once; local-only and
// bounded). A failure before delivery leaves the message in the Outbox to retry
// and is reported (joined) so the caller can log it, without stopping the batch.
//
// Retries are bounded: after maxReleaseAttempts consecutive failures the message
// is moved back to Drafts (what a user-initiated cancel does) and onGiveUp is
// called, so a message that can never be released stops burning a delivery
// attempt every sweep and its sender is told instead of left guessing.
func ProcessDueOutbox(st *objectstore.Store, deliver DeliverFunc, onGiveUp GiveUpFunc, now time.Time) (released int, err error) {
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
		done, e := releaseMessage(st, deliver, outbox, m, now)
		if e != nil {
			errs = append(errs, e)
		}
		if done {
			released++
			continue
		}
		if e := recordFailure(st, onGiveUp, outbox, m, e); e != nil {
			errs = append(errs, e)
		}
	}
	return released, errors.Join(errs...)
}

// releaseMessage delivers one due message, files it to Sent, and removes it from
// the Outbox. done reports whether the message has left the Outbox, so the caller
// knows whether to count the release or to charge the failure against the
// message's attempt budget. A failure before delivery leaves the message in place
// to retry.
func releaseMessage(st *objectstore.Store, deliver DeliverFunc, outbox int64, m objectstore.MessageInfo, now time.Time) (done bool, err error) {
	full, err := st.OpenMessage(m.ID)
	if err != nil {
		return false, err
	}
	recipients := recipientAddrs(full)
	raw, err := st.GetMessageRaw(outbox, m.UID)
	if err != nil {
		return false, err
	}
	// Deliver with the Bcc header removed; the unresolved list is ignored (no
	// external relay yet, the same as the interactive compose path).
	if _, err := deliver(recipients, stripBcc(raw), now); err != nil {
		// A terminal delivery error (the message was quarantined for a virus) drops
		// the scheduled copy without filing Sent, rather than retrying it forever.
		var term interface{ TerminalDelivery() bool }
		if errors.As(err, &term) && term.TerminalDelivery() {
			if e := st.DeleteMessage(outbox, m.UID); e != nil {
				return false, e
			}
			return true, nil
		}
		return false, err
	}
	// Delivered. Keep the with-Bcc copy in Sent for the record, then clear the
	// Outbox. A filing failure is reported but does NOT hold the message in the
	// Outbox: the mail is already out, and leaving it scheduled would re-deliver
	// it to every recipient on the next sweep, once every sweep, forever. Losing
	// the Sent copy is the lesser harm, and it is logged.
	_, fileErr := st.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, now, objectstore.FlagSeen)
	if err := st.DeleteMessage(outbox, m.UID); err != nil {
		return false, errors.Join(fileErr, err)
	}
	if fileErr != nil {
		return true, fmt.Errorf("delivered but not filed to Sent: %w", fileErr)
	}
	return true, nil
}

// recordFailure charges one failed release against the message's attempt budget
// and, once the budget is spent, abandons the message: it moves back to Drafts
// (the same landing place as a user-initiated cancel, and the move rewrites the
// object so the deferred-send time and the attempt counter go with it) and
// onGiveUp reports it to the sender.
func recordFailure(st *objectstore.Store, onGiveUp GiveUpFunc, outbox int64, m objectstore.MessageInfo, cause error) error {
	attempts, err := bumpReleaseAttempts(st, m.ID)
	if err != nil {
		return err
	}
	if attempts < maxReleaseAttempts {
		return nil
	}
	// Collect what the report needs before the move invalidates the Outbox uid.
	var raw []byte
	var recipients []string
	if full, e := st.OpenMessage(m.ID); e == nil {
		recipients = recipientAddrs(full)
	}
	if b, e := st.GetMessageRaw(outbox, m.UID); e == nil {
		raw = b
	}
	if _, err := st.MoveMessage(outbox, m.UID, int64(mapi.PrivateFIDDraft)); err != nil {
		return err
	}
	if onGiveUp != nil && raw != nil {
		onGiveUp(raw, recipients, cause)
	}
	return fmt.Errorf("gave up releasing the scheduled message after %d attempts, moved to Drafts: %w", attempts, cause)
}

// bumpReleaseAttempts increments the message's consecutive-failure counter and
// returns the new value. The counter lives only on the Outbox object, so it is
// discarded automatically when the message is released, moved, or deleted.
func bumpReleaseAttempts(st *objectstore.Store, messageID int64) (int32, error) {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{nameReleaseAttempts})
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 || ids[0] == 0 {
		return 0, fmt.Errorf("spooler: could not allocate the release-attempt property")
	}
	tag := mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtLong))
	var attempts int32
	props, err := st.GetMessageProperties(messageID, tag)
	if err != nil {
		return 0, err
	}
	if v, ok := props.Get(tag); ok {
		if n, ok := v.(int32); ok {
			attempts = n
		}
	}
	attempts++
	if err := st.SetMessageProperties(messageID, mapi.PropertyValues{{Tag: tag, Value: attempts}}); err != nil {
		return 0, err
	}
	return attempts, nil
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
