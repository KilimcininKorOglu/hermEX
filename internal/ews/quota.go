package ews

import "hermex/internal/objectstore"

// quotaCode is the response code refusing a write that would add to st: the
// [MS-OXWSCDATA] ErrorQuotaExceeded once the mailbox is past its receive quota,
// the limit past which nothing new may enter it, or an internal error when the
// store cannot report its usage. An empty code means the write may proceed.
func quotaCode(st *objectstore.Store) string {
	over, err := st.OverReceiveQuota()
	switch {
	case err != nil:
		return "ErrorInternalServerError"
	case over:
		return "ErrorQuotaExceeded"
	}
	return ""
}

// itemCount is the number of response messages a CreateItem answers with: one
// per item of every kind the request carries.
func (req createItemRequest) itemCount() int {
	it := req.Items
	return len(it.Messages) + len(it.CalendarItems) + len(it.Tasks) + len(it.Items) +
		len(it.Accept) + len(it.TentativelyAccept) + len(it.Decline) +
		len(it.ReplyToItem) + len(it.ReplyAllToItem) + len(it.ForwardItem) +
		len(it.SuppressReadReceipt)
}

// storesItems reports whether a CreateItem adds anything to the caller's
// mailbox. Only a SendOnly request carrying no calendar item, task or note
// stores nothing: its messages leave without a copy, under the send quota.
func (req createItemRequest) storesItems(disp string) bool {
	it := req.Items
	return disp != "SendOnly" || len(it.CalendarItems)+len(it.Tasks)+len(it.Items) > 0
}

// quotaRefusal answers every item of a CreateItem with the quota code.
func (req createItemRequest) quotaRefusal(code string) createItemResponse {
	msgs := make([]itemResponseMessage, req.itemCount())
	for i := range msgs {
		msgs[i] = itemError(code)
	}
	return createItemResponse{Messages: msgs}
}
