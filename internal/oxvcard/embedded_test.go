package oxvcard

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestEmbeddedContactRoundTrip proves an attached contact's properties survive the
// encoding by name: a named property comes back under the id the reading store
// gives that name, whatever id the writing store used.
func TestEmbeddedContactRoundTrip(t *testing.T) {
	writer := mapi.MakeTag(0x8123, mapi.PtUnicode)
	props := mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrDisplayName, Value: "Çağla Öztürk"},
		{Tag: writer, Value: "cagla@example.org"},
	}
	nameOf := func(id uint16) (mapi.PropertyName, bool, error) {
		return mapi.NameEmail1Address, id == writer.ID(), nil
	}
	blob, err := EncodeEmbedded(props, nameOf)
	if err != nil {
		t.Fatal(err)
	}
	reader := func(_ bool, names []mapi.PropertyName) ([]uint16, error) {
		out := make([]uint16, len(names))
		for i, n := range names {
			if n == mapi.NameEmail1Address {
				out[i] = 0x8456
			}
		}
		return out, nil
	}
	got, err := RestoreEmbedded(blob, reader)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get(mapi.MakeTag(0x8456, mapi.PtUnicode)); v != "cagla@example.org" {
		t.Errorf("email under the reader's id = %v", v)
	}
	if v, _ := got.Get(mapi.PrDisplayName); v != "Çağla Öztürk" {
		t.Errorf("display name = %v", v)
	}
}

// TestEmbeddedCardIsVCard3 proves the card an attached contact is sent as is the
// vCard 3.0 [MS-OXCMAIL] 2.1.3.4.6 names: VERSION 3.0, the N property 3.0
// requires, and the IM address as X-MS-IMADDRESS ([MS-OXVCARD] 2.1.3.9.4).
func TestEmbeddedCardIsVCard3(t *testing.T) {
	email := mapi.MakeTag(0x8001, mapi.PtUnicode)
	im := mapi.MakeTag(0x8002, mapi.PtUnicode)
	props := mapi.PropertyValues{
		{Tag: mapi.PrDisplayName, Value: "Çağla Öztürk"},
		{Tag: email, Value: "cagla@example.org"},
		{Tag: im, Value: "cagla@im.example.org"},
	}
	nameOf := func(id uint16) (mapi.PropertyName, bool, error) {
		if id == email.ID() {
			return mapi.NameEmail1Address, true, nil
		}
		return mapi.NameInstantMessagingAddress, true, nil
	}
	blob, err := EncodeEmbedded(props, nameOf)
	if err != nil {
		t.Fatal(err)
	}
	card, name, err := EmbeddedCard(blob)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Çağla Öztürk" {
		t.Errorf("name = %q", name)
	}
	for _, want := range []string{"VERSION:3.0\r\n", "FN:Çağla Öztürk\r\n", "N:;;;;\r\n",
		"EMAIL:cagla@example.org\r\n", "X-MS-IMADDRESS:cagla@im.example.org\r\n"} {
		if !strings.Contains(string(card), want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
}
