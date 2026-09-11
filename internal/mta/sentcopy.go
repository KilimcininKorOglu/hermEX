package mta

import (
	"bytes"
	"net/mail"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// fileRepresentedCopy files a copy of a just-sent message in the Sent Items of the
// mailbox it was sent in the name of, when that mailbox asked for one.
//
// A person sending as, or on behalf of, a shared mailbox keeps the copy in their own
// Sent Items, which is what every protocol's own sent-copy path already files.
// Without this, nobody else with access to the shared mailbox can see what went out
// in its name, which is usually the whole point of sharing it.
//
// It runs at the one point every protocol's outgoing mail converges on, after the
// message has gone out. Nothing here can turn a completed send into a failure: the
// mail has already left, so every failure is recorded and dropped.
//
// Which grant was used is read from the message rather than passed down, because the
// message is what carries it: a represented send puts the mailbox in From and leaves
// the caller as the envelope sender, and an on-behalf send additionally names the
// caller in Sender. That is the same distinction the two settings are written in
// terms of.
func fileRepresentedCopy(accounts directory.Accounts, envelopeFrom string, raw []byte, sent time.Time) {
	path, onBehalf, ok := representedMailbox(accounts, envelopeFrom, raw)
	if !ok {
		return
	}
	st, err := objectstore.OpenExisting(path)
	if err != nil {
		logSentCopy(envelopeFrom, "open", err)
		return
	}
	defer st.Close()

	cfg, err := st.GetSentCopyConfig()
	if err != nil {
		st.LogSwallowedError("mta.sent-copy-config", err)
		return
	}
	if (onBehalf && !cfg.ForSendOnBehalf) || (!onBehalf && !cfg.ForSendAs) {
		return
	}
	// The mailbox filed it itself, so it is already read: nobody there composed it and
	// an unread count that grows with every delegate send would be noise.
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, sent, objectstore.FlagSeen); err != nil {
		st.LogSwallowedError("mta.sent-copy-file", err)
	}
}

// representedMailbox reports the maildir of the mailbox a message was sent in the
// name of, and whether the send named the caller in Sender (on behalf of) or not
// (send as). ok is false when the message names no other mailbox.
//
// The comparison is between MAILDIRS, not addresses. An alias send puts the alias in
// From and the login in the envelope, two different strings for one account, and
// comparing addresses would file a second copy of every ordinary aliased send.
func representedMailbox(accounts directory.Accounts, envelopeFrom string, raw []byte) (path string, onBehalf, ok bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", false, false
	}
	from, ok := singleAddress(msg.Header.Get("From"))
	if !ok {
		return "", false, false
	}
	reprPath, ok := accounts.Resolve(from)
	if !ok {
		return "", false, false
	}
	senderPath, ok := accounts.Resolve(envelopeFrom)
	if ok && senderPath == reprPath {
		return "", false, false // the sender's own mailbox, alias or not
	}
	_, onBehalf = singleAddress(msg.Header.Get("Sender"))
	return reprPath, onBehalf, true
}

// singleAddress returns the bare address of a header that names exactly one, and
// reports ok=false for an absent, unparseable, or multi-address header.
func singleAddress(field string) (string, bool) {
	if strings.TrimSpace(field) == "" {
		return "", false
	}
	list, err := mail.ParseAddressList(field)
	if err != nil || len(list) != 1 {
		return "", false
	}
	return list[0].Address, true
}

// logSentCopy records a sent-copy failure that happened before a store was open, so
// it has no store to record itself through. The send already succeeded, so this is
// the only trace it leaves.
func logSentCopy(envelopeFrom, stage string, err error) {
	defaultLogger.Load().Emit(logging.Event{
		Level:     logging.LevelWarn,
		Subsystem: logging.MTA,
		Name:      "sent-copy.fail",
		User:      envelopeFrom,
		Err:       err.Error(),
		Fields:    logging.Fields{"stage": stage},
	})
}
