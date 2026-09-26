package objectstore

import "hermex/internal/mapi"

// The flags that decide whether a message sent in this mailbox's name is also filed
// in its own Sent Items, and whether that copy is then the only one. They have no
// MS-OXPROPS counterpart (Exchange keeps MessageCopyForSentAsEnabled and
// MessageCopyForSendOnBehalfEnabled on the mailbox object, not in the store), so they
// are named properties under hermEX's own namespace, like the meeting-handling flag.
var (
	nameSentCopyForSendAs = mapi.PropertyName{
		Kind: mapi.MnidString, GUID: hermexStoreNamespace, Name: "SentCopyForSendAs",
	}
	nameSentCopyForSendOnBehalf = mapi.PropertyName{
		Kind: mapi.MnidString, GUID: hermexStoreNamespace, Name: "SentCopyForSendOnBehalf",
	}
	nameSentCopyExclusive = mapi.PropertyName{
		Kind: mapi.MnidString, GUID: hermexStoreNamespace, Name: "SentCopyExclusive",
	}
)

// SentCopyConfig is a mailbox's answer to "keep a copy of what is sent in my name".
// A person sending as, or on behalf of, a shared mailbox keeps the copy in their own
// Sent Items unless Exclusive says otherwise; ForSendAs and ForSendOnBehalf decide
// whether the represented mailbox keeps one as well, so everyone with access can see
// what went out in its name.
//
// All default to false. A mailbox that has never been configured therefore behaves
// as it did before the setting existed, and turning one on is the operator's or the
// owner's decision rather than something an upgrade does to them.
type SentCopyConfig struct {
	// ForSendAs applies when the message named only this mailbox (a send-as grant).
	ForSendAs bool
	// ForSendOnBehalf applies when the message named this mailbox in From and the
	// real sender in Sender (a send-on-behalf-of grant).
	ForSendOnBehalf bool
	// Exclusive makes this mailbox's copy the only one: once one of the two flags
	// above has filed it, the server files no copy in the sender's own mailbox. It
	// has no effect on its own, and none when this mailbox's copy was not written.
	Exclusive bool
}

// sentCopyNames lists the flags in the order of SentCopyConfig's fields.
var sentCopyNames = []mapi.PropertyName{nameSentCopyForSendAs, nameSentCopyForSendOnBehalf, nameSentCopyExclusive}

// sentCopyTags resolves the flags' tags for this store, in the order of
// sentCopyNames; an unallocated one is 0. create allocates the named-property ids,
// which a write needs and a read must not do.
func (s *Store) sentCopyTags(create bool) ([]mapi.PropTag, error) {
	ids, err := s.GetNamedPropIDs(create, sentCopyNames)
	if err != nil {
		return nil, err
	}
	tags := make([]mapi.PropTag, len(sentCopyNames))
	for i := range tags {
		if i < len(ids) && ids[i] != 0 {
			tags[i] = mapi.MakeTag(ids[i], mapi.PtBoolean)
		}
	}
	return tags, nil
}

// fields returns pointers to the config's flags, in the order of sentCopyNames.
func (c *SentCopyConfig) fields() []*bool {
	return []*bool{&c.ForSendAs, &c.ForSendOnBehalf, &c.Exclusive}
}

// GetSentCopyConfig reads the mailbox's sent-copy settings. An unset property reads
// as false, which is the default: no copy is kept until someone asks for one.
func (s *Store) GetSentCopyConfig() (SentCopyConfig, error) {
	tags, err := s.sentCopyTags(false)
	if err != nil {
		return SentCopyConfig{}, err
	}
	var want []mapi.PropTag
	for _, t := range tags {
		if t != 0 {
			want = append(want, t)
		}
	}
	var cfg SentCopyConfig
	if len(want) == 0 {
		return cfg, nil
	}
	props, err := s.GetStoreProperties(want...)
	if err != nil {
		return SentCopyConfig{}, err
	}
	for i, f := range cfg.fields() {
		if tags[i] != 0 {
			*f = boolProp(props, tags[i])
		}
	}
	return cfg, nil
}

// SetSentCopyConfig replaces the mailbox's sent-copy settings.
func (s *Store) SetSentCopyConfig(cfg SentCopyConfig) error {
	tags, err := s.sentCopyTags(true)
	if err != nil {
		return err
	}
	var props mapi.PropertyValues
	for i, f := range cfg.fields() {
		if tags[i] != 0 {
			props = append(props, mapi.TaggedPropVal{Tag: tags[i], Value: *f})
		}
	}
	if len(props) == 0 {
		return nil
	}
	return s.SetStoreProperties(props)
}
