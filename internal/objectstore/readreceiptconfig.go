package objectstore

import (
	"fmt"

	"hermex/internal/mapi"
)

// ReadReceiptResponse is how webmail answers a message that asks for a read
// receipt, the three choices Outlook on the web offers.
type ReadReceiptResponse int32

const (
	// ReadReceiptAsk shows the reader a prompt and sends only when they agree. It
	// is the zero value, so a mailbox that never chose asks.
	ReadReceiptAsk ReadReceiptResponse = 0
	// ReadReceiptAlways sends the receipt as soon as the message is opened.
	ReadReceiptAlways ReadReceiptResponse = 1
	// ReadReceiptNever sends nothing and shows no prompt.
	ReadReceiptNever ReadReceiptResponse = 2
)

// Valid reports whether r is one of the three responses.
func (r ReadReceiptResponse) Valid() bool {
	return r >= ReadReceiptAsk && r <= ReadReceiptNever
}

// The read-receipt settings have no MS-OXPROPS counterpart (Exchange keeps them in
// the user's web options and on the CAS mailbox), so they are named properties
// under hermEX's own namespace, like the sent-copy flags.
var (
	nameReadReceiptResponse = mapi.PropertyName{
		Kind: mapi.MnidString, GUID: hermexStoreNamespace, Name: "ReadReceiptResponse",
	}
	nameReadReceiptSuppressActiveSync = mapi.PropertyName{
		Kind: mapi.MnidString, GUID: hermexStoreNamespace, Name: "ReadReceiptSuppressActiveSync",
	}
)

// ReadReceiptConfig is a mailbox's read-receipt behaviour. Response applies to
// webmail; SuppressActiveSync stops the receipt a message read on an ActiveSync
// device would otherwise send. EWS follows the request's own SuppressReadReceipts
// and Outlook its own ReadFlags, so neither has a setting here.
type ReadReceiptConfig struct {
	Response           ReadReceiptResponse
	SuppressActiveSync bool
}

// readReceiptTags resolves the settings' tags for this store; an unallocated one is
// 0. create allocates the named-property ids, which a write needs and a read must
// not do.
func (s *Store) readReceiptTags(create bool) (response, suppress mapi.PropTag, err error) {
	ids, err := s.GetNamedPropIDs(create, []mapi.PropertyName{nameReadReceiptResponse, nameReadReceiptSuppressActiveSync})
	if err != nil {
		return 0, 0, err
	}
	if len(ids) > 0 && ids[0] != 0 {
		response = mapi.MakeTag(ids[0], mapi.PtLong)
	}
	if len(ids) > 1 && ids[1] != 0 {
		suppress = mapi.MakeTag(ids[1], mapi.PtBoolean)
	}
	return response, suppress, nil
}

// GetReadReceiptConfig reads the mailbox's read-receipt settings. An unset setting
// reads as its zero value: webmail asks, and ActiveSync sends.
func (s *Store) GetReadReceiptConfig() (ReadReceiptConfig, error) {
	response, suppress, err := s.readReceiptTags(false)
	if err != nil {
		return ReadReceiptConfig{}, err
	}
	var cfg ReadReceiptConfig
	var want []mapi.PropTag
	for _, t := range []mapi.PropTag{response, suppress} {
		if t != 0 {
			want = append(want, t)
		}
	}
	if len(want) == 0 {
		return cfg, nil
	}
	props, err := s.GetStoreProperties(want...)
	if err != nil {
		return ReadReceiptConfig{}, err
	}
	if v, ok := props.Get(response); ok && response != 0 {
		n, _ := v.(int32)
		cfg.Response = ReadReceiptResponse(n)
	}
	if suppress != 0 {
		cfg.SuppressActiveSync = boolProp(props, suppress)
	}
	return cfg, nil
}

// SetReadReceiptConfig replaces the mailbox's read-receipt settings. A response
// outside the three choices is refused.
func (s *Store) SetReadReceiptConfig(cfg ReadReceiptConfig) error {
	if !cfg.Response.Valid() {
		return fmt.Errorf("objectstore: read receipt response %d is not one of the three choices", cfg.Response)
	}
	response, suppress, err := s.readReceiptTags(true)
	if err != nil {
		return err
	}
	var props mapi.PropertyValues
	if response != 0 {
		props = append(props, mapi.TaggedPropVal{Tag: response, Value: int32(cfg.Response)})
	}
	if suppress != 0 {
		props = append(props, mapi.TaggedPropVal{Tag: suppress, Value: cfg.SuppressActiveSync})
	}
	if len(props) == 0 {
		return nil
	}
	return s.SetStoreProperties(props)
}
