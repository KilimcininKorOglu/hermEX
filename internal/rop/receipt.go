package rop

import (
	"log"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// maybeReadReceipt generates and sends a read-receipt MDN for a message the
// client just marked read, when that message carried a read-receipt request
// ([MS-OXOMSG] 3.3.4.3). It is best-effort: any storage or send failure is
// logged and swallowed so it can never fail the SetMessageReadFlag that
// triggered it. After a successful send it clears both request flags so the
// receipt fires exactly once — a later read finds no pending request. A
// read-only session (no MTA bridge) sends nothing.
//
// The destination is the original message's PR_SENT_REPRESENTING_SMTP_ADDRESS,
// matching the reference; an absent value means there is no represented sender to
// notify, and the receipt is skipped.
func (s *Session) maybeReadReceipt(store *objectstore.Store, messageID int64) {
	if s.accounts == nil {
		return
	}
	props, err := store.GetMessageProperties(messageID,
		mapi.PrReadReceiptRequested,
		mapi.PrSentRepresentingSmtpAddress,
		mapi.PrSubject,
		mapi.PrInternetMessageID,
		mapi.PrClientSubmitTime,
	)
	if err != nil {
		log.Printf("rop: read-receipt property read failed for message %d, skipped: %v", messageID, err)
		return
	}
	if req, _ := props.Get(mapi.PrReadReceiptRequested); req != true {
		return // no receipt requested, or a prior read already consumed it
	}
	dest := stringProp(props, mapi.PrSentRepresentingSmtpAddress)
	if dest == "" {
		return // no represented sender to notify (matches the reference's early return)
	}

	info := mta.ReadReceiptInfo{
		Reader:      s.owner,
		To:          dest,
		OrigFrom:    dest,
		OrigSubject: stringProp(props, mapi.PrSubject),
		OrigMsgID:   stringProp(props, mapi.PrInternetMessageID),
	}
	if v, ok := props.Get(mapi.PrClientSubmitTime); ok {
		if nt, ok := v.(uint64); ok {
			info.SubmitTime = mapi.NTTimeToUnix(nt)
		}
	}

	if err := mta.SendReadReceipt(s.accounts, info, time.Now()); err != nil {
		log.Printf("rop: read-receipt send failed for message %d, skipped: %v", messageID, err)
		return
	}
	// Fire-once: clear both request flags after sending, exactly as the reference
	// does, so a subsequent read of the same message cannot re-send the receipt.
	if err := store.SetMessageProperties(messageID, mapi.PropertyValues{
		{Tag: mapi.PrReadReceiptRequested, Value: false},
		{Tag: mapi.PrNonReceiptNotificationRequested, Value: false},
	}); err != nil {
		log.Printf("rop: read-receipt flag clear failed for message %d: %v", messageID, err)
	}
}
