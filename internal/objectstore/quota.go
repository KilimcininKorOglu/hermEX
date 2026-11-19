package objectstore

import "hermex/internal/mapi"

// QuotaLimits holds a mailbox's store quotas in kibibytes; zero means unlimited,
// matching the Exchange store-quota convention. They are store-root MAPI
// properties (PtLong), the same "everything is a property" shape as the rest of
// the store. The store only holds the limits; the delivery and submission paths
// decide what to enforce against the live MailboxSize.
type QuotaLimits struct {
	SendKB    uint32 `json:"sendKB"`    // PR_PROHIBIT_SEND_QUOTA, block submission above this
	ReceiveKB uint32 `json:"receiveKB"` // PR_PROHIBIT_RECEIVE_QUOTA, block delivery above this
	StorageKB uint32 `json:"storageKB"` // PR_STORAGE_QUOTA_LIMIT, warning threshold
}

// GetQuota returns the mailbox's store quota limits; an unset limit reads as 0
// (unlimited).
func (s *Store) GetQuota() (QuotaLimits, error) {
	props, err := s.GetStoreProperties(mapi.PrProhibitSendQuota, mapi.PrProhibitReceiveQuota, mapi.PrStorageQuotaLimit)
	if err != nil {
		return QuotaLimits{}, err
	}
	return QuotaLimits{
		SendKB:    quotaProp(props, mapi.PrProhibitSendQuota),
		ReceiveKB: quotaProp(props, mapi.PrProhibitReceiveQuota),
		StorageKB: quotaProp(props, mapi.PrStorageQuotaLimit),
	}, nil
}

// quotaProp reads a PtLong quota property as a uint32, defaulting to 0 (unlimited)
// when absent.
func quotaProp(props mapi.PropertyValues, tag mapi.PropTag) uint32 {
	if v, ok := props.Get(tag); ok {
		if n, ok := v.(int32); ok {
			// #nosec G115 -- the signed and unsigned views of the same 32 bits
			return uint32(n)
		}
	}
	return 0
}

// SetQuota replaces the mailbox's store quota limits.
func (s *Store) SetQuota(q QuotaLimits) error {
	return s.SetStoreProperties(mapi.PropertyValues{
		// #nosec G115 -- the signed and unsigned views of the same 32 bits
		{Tag: mapi.PrProhibitSendQuota, Value: int32(q.SendKB)},
		// #nosec G115 -- the signed and unsigned views of the same 32 bits
		{Tag: mapi.PrProhibitReceiveQuota, Value: int32(q.ReceiveKB)},
		// #nosec G115 -- the signed and unsigned views of the same 32 bits
		{Tag: mapi.PrStorageQuotaLimit, Value: int32(q.StorageKB)},
	})
}

// OverReceiveQuota reports whether the mailbox already holds more than its receive
// quota, the Exchange "prohibit send and receive" limit: past it, nothing new may be
// added to the mailbox, whether by delivery or by a client saving an item. A zero
// limit is unlimited. The comparison is 64-bit, since the limit in bytes overflows
// 32 bits.
func (s *Store) OverReceiveQuota() (bool, error) {
	q, err := s.GetQuota()
	if err != nil || q.ReceiveKB == 0 {
		return false, err
	}
	size, err := s.MailboxSize()
	if err != nil {
		return false, err
	}
	return size > int64(q.ReceiveKB)*1024, nil
}

// MailboxSize returns the mailbox's current used space in bytes, the sum of the
// stored message sizes, computed on demand. It is the value the Exchange store
// reports as PR_MESSAGE_SIZE_EXTENDED and the basis for quota enforcement.
func (s *Store) MailboxSize() (int64, error) {
	var size int64
	if err := s.objdb.QueryRow(`SELECT COALESCE(SUM(message_size), 0) FROM messages WHERE is_deleted=0`).Scan(&size); err != nil {
		return 0, err
	}
	return size, nil
}
