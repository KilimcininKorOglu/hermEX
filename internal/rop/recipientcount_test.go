package rop

import (
	"encoding/binary"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestRecipientCountSaturates pins the 16-bit RecipientCount field. A stored
// message can carry more recipients than the field can name, and the count used to
// wrap: 65536 recipients reported as 0, which tells the client the message has
// none at all.
func TestRecipientCountSaturates(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want uint16
	}{
		{"under the field width", 3, 3},
		{"one past the field width", 0x10000, 0xFFFF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recipients := make([]mapi.PropertyValues, tc.n)
			out := ext.NewPush(0)
			writeRecipientTable(out, recipients)
			b := out.Bytes()
			if len(b) < 2 {
				t.Fatalf("response is %d bytes", len(b))
			}
			if got := binary.LittleEndian.Uint16(b[:2]); got != tc.want {
				t.Errorf("RecipientCount = %d, want %d", got, tc.want)
			}
		})
	}
}
