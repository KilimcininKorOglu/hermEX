package objectstore

import "hermex/internal/mapi"

// The two flags that decide whether a message sent in this mailbox's name is also
// filed in its own Sent Items. They have no MS-OXPROPS counterpart (Exchange keeps
// MessageCopyForSentAsEnabled and MessageCopyForSendOnBehalfEnabled on the mailbox
// object, not in the store), so they are named properties under hermEX's own
// namespace, like the meeting-handling flag.
var (
	nameSentCopyForSendAs = mapi.PropertyName{
		Kind: mapi.MnidString, GUID: hermexStoreNamespace, Name: "SentCopyForSendAs",
	}
	nameSentCopyForSendOnBehalf = mapi.PropertyName{
		Kind: mapi.MnidString, GUID: hermexStoreNamespace, Name: "SentCopyForSendOnBehalf",
	}
)

// SentCopyConfig is a mailbox's answer to "keep a copy of what is sent in my name".
// A person sending as, or on behalf of, a shared mailbox keeps the copy in their own
// Sent Items whatever these say; these decide whether the represented mailbox keeps
// one as well, so everyone with access can see what went out in its name.
//
// Both default to false. A mailbox that has never been configured therefore behaves
// as it did before the setting existed, and turning it on is the operator's or the
// owner's decision rather than something an upgrade does to them.
type SentCopyConfig struct {
	// ForSendAs applies when the message named only this mailbox (a send-as grant).
	ForSendAs bool
	// ForSendOnBehalf applies when the message named this mailbox in From and the
	// real sender in Sender (a send-on-behalf-of grant).
	ForSendOnBehalf bool
}

// sentCopyTags resolves the two flags' tags for this store. create allocates the
// named-property ids, which a write needs and a read must not do.
func (s *Store) sentCopyTags(create bool) (sendAs, onBehalf mapi.PropTag, err error) {
	ids, err := s.GetNamedPropIDs(create, []mapi.PropertyName{
		nameSentCopyForSendAs, nameSentCopyForSendOnBehalf,
	})
	if err != nil || len(ids) < 2 {
		return 0, 0, err
	}
	if ids[0] != 0 {
		sendAs = mapi.MakeTag(ids[0], mapi.PtBoolean)
	}
	if ids[1] != 0 {
		onBehalf = mapi.MakeTag(ids[1], mapi.PtBoolean)
	}
	return sendAs, onBehalf, nil
}

// GetSentCopyConfig reads the mailbox's sent-copy settings. An unset property reads
// as false, which is the default: no copy is kept until someone asks for one.
func (s *Store) GetSentCopyConfig() (SentCopyConfig, error) {
	sendAs, onBehalf, err := s.sentCopyTags(false)
	if err != nil {
		return SentCopyConfig{}, err
	}
	var cfg SentCopyConfig
	tags := make([]mapi.PropTag, 0, 2)
	for _, t := range []mapi.PropTag{sendAs, onBehalf} {
		if t != 0 {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		return cfg, nil
	}
	props, err := s.GetStoreProperties(tags...)
	if err != nil {
		return SentCopyConfig{}, err
	}
	if sendAs != 0 {
		cfg.ForSendAs = boolProp(props, sendAs)
	}
	if onBehalf != 0 {
		cfg.ForSendOnBehalf = boolProp(props, onBehalf)
	}
	return cfg, nil
}

// SetSentCopyConfig replaces the mailbox's sent-copy settings.
func (s *Store) SetSentCopyConfig(cfg SentCopyConfig) error {
	sendAs, onBehalf, err := s.sentCopyTags(true)
	if err != nil {
		return err
	}
	var props mapi.PropertyValues
	if sendAs != 0 {
		props = append(props, mapi.TaggedPropVal{Tag: sendAs, Value: cfg.ForSendAs})
	}
	if onBehalf != 0 {
		props = append(props, mapi.TaggedPropVal{Tag: onBehalf, Value: cfg.ForSendOnBehalf})
	}
	if len(props) == 0 {
		return nil
	}
	return s.SetStoreProperties(props)
}
