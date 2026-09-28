package oxvcard

import (
	"errors"
	"strings"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// An attached contact (an embedded message of class IPM.Contact) is stored as RFC
// 5322 bytes like every embedded message, which lose its class and its contact
// fields. Its properties are therefore also kept in the attachment's
// PrEmbeddedContact, encoded here. Named properties are recorded by name, not by
// the store's id, because the message can be copied into another store whose ids
// differ: in the encoding a named property's id is 0x8000 plus its index in the
// name list.

// blobFlags selects the encoding: UTF-8 strings and 32-bit binary lengths.
const blobFlags = ext.FlagWCount

// errBlob reports an encoded contact that does not decode.
var errBlob = errors.New("oxvcard: malformed embedded contact")

// NameResolver maps a store property id back to its named property.
type NameResolver func(propid uint16) (mapi.PropertyName, bool, error)

// IsContactClass reports whether a message class is a contact's.
func IsContactClass(class string) bool {
	return class == "IPM.Contact" || strings.HasPrefix(class, "IPM.Contact.")
}

// EncodeEmbedded encodes an attached contact's properties. A named property the
// store cannot name is left out.
func EncodeEmbedded(props mapi.PropertyValues, nameOf NameResolver) ([]byte, error) {
	var names []mapi.PropertyName
	index := map[mapi.PropertyName]uint16{}
	out := make(mapi.PropertyValues, 0, len(props))
	for _, pv := range props {
		if pv.Tag.ID() < 0x8000 {
			out = append(out, pv)
			continue
		}
		name, ok, err := nameOf(pv.Tag.ID())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		i, seen := index[name]
		if !seen {
			i = uint16(len(names)) // #nosec G115 -- a message holds far fewer than 32768 named properties
			index[name] = i
			names = append(names, name)
		}
		out = append(out, mapi.TaggedPropVal{Tag: mapi.MakeTag(0x8000+i, pv.Tag.Type()), Value: pv.Value})
	}
	p := ext.NewPush(blobFlags)
	if err := p.PropertyNames(names); err != nil {
		return nil, err
	}
	if err := p.PropertyValuesLong(out); err != nil {
		return nil, err
	}
	return p.Bytes(), nil
}

// decodeEmbedded decodes an encoded contact into its properties, whose named
// properties carry placeholder ids, and the names those ids stand for.
func decodeEmbedded(blob []byte) (mapi.PropertyValues, []mapi.PropertyName, error) {
	p := ext.NewPull(blob, blobFlags)
	names, err := p.PropertyNames()
	if err != nil {
		return nil, nil, errors.Join(errBlob, err)
	}
	props, err := p.PropertyValuesLong()
	if err != nil {
		return nil, nil, errors.Join(errBlob, err)
	}
	return props, names, nil
}

// RestoreEmbedded decodes an encoded contact into properties under the store's
// ids. resolve maps the recorded names; a name it leaves at 0 drops its property.
func RestoreEmbedded(blob []byte, resolve PropIDResolver) (mapi.PropertyValues, error) {
	props, names, err := decodeEmbedded(blob)
	if err != nil {
		return nil, err
	}
	ids := make([]uint16, len(names))
	if len(names) > 0 {
		if ids, err = resolve(false, names); err != nil {
			return nil, err
		}
	}
	out := make(mapi.PropertyValues, 0, len(props))
	for _, pv := range props {
		id := pv.Tag.ID()
		if id >= 0x8000 {
			i := int(id - 0x8000)
			if i >= len(ids) || ids[i] == 0 {
				continue
			}
			id = ids[i]
		}
		out = append(out, mapi.TaggedPropVal{Tag: mapi.MakeTag(id, pv.Tag.Type()), Value: pv.Value})
	}
	return out, nil
}

// EmbeddedCard renders an encoded contact as the vCard 3.0 an internet message
// carries ([MS-OXCMAIL] 2.1.3.4.6), with the contact's display name for the file
// name.
func EmbeddedCard(blob []byte) (card []byte, name string, err error) {
	props, names, err := decodeEmbedded(blob)
	if err != nil {
		return nil, "", err
	}
	// The placeholder ids answer the renderer's name lookups directly.
	resolve := func(_ bool, want []mapi.PropertyName) ([]uint16, error) {
		out := make([]uint16, len(want))
		for i, w := range want {
			for j, n := range names {
				if n == w {
					out[i] = 0x8000 + uint16(j) // #nosec G115 -- the index came from a uint16 id
				}
			}
		}
		return out, nil
	}
	msg := &oxcmail.Message{Props: props}
	card, err = Export(msg, Options{Resolver: resolve, Version3: true})
	if err != nil {
		return nil, "", err
	}
	return card, displayName(&msg.Props), nil
}
