package rop

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// progressSource is a FastTransfer source with fixed progress.
type progressSource struct{ done, total uint64 }

func (progressSource) GetBuffer(int) ([]byte, bool, error) { return nil, true, nil }
func (p progressSource) Progress() (uint64, uint64)        { return p.done, p.total }

// TestStepCounts proves the GetBuffer step counts follow the source's progress:
// a small transfer is counted as is, a transfer too large for 16 bits is scaled
// so its total fits, an empty one still has one step, and a finished one has
// every step done.
func TestStepCounts(t *testing.T) {
	cases := []struct {
		name        string
		src         fastTransferSource
		last        bool
		done, total uint16
	}{
		{"bytes", progressSource{300, 1000}, false, 300, 1000},
		{"scaled", progressSource{0x40000, 0x100000}, false, 0x40000 / 17, 0x100000 / 17},
		{"empty", progressSource{0, 0}, false, 0, 1},
		{"finished", progressSource{10, 1000}, true, 1000, 1000},
	}
	for _, c := range cases {
		done, total := stepCounts(c.src, c.last)
		if done != c.done || total != c.total {
			t.Errorf("%s: steps = %d/%d, want %d/%d", c.name, done, total, c.done, c.total)
		}
	}
}

// TestCopySourceStepCounts proves a generic-copy source counts its steps too:
// a CopyTo of a stored message reports the bytes served against the size of
// the rendered stream, and every step done once drained.
func TestCopySourceStepCounts(t *testing.T) {
	dir := t.TempDir()
	mid := seedInboxMessage(t, dir, "COPYME")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	src, err := st.NewCopyToMessageSource(mid, []mapi.PropTag{})
	if err != nil {
		t.Fatal(err)
	}
	_, total := src.Progress()
	if total == 0 {
		t.Fatal("a rendered message stream reports a total of zero")
	}
	chunk, last, err := src.GetBuffer(16)
	if err != nil || last {
		t.Fatalf("first chunk: last=%v err=%v", last, err)
	}
	if done, _ := src.Progress(); done != uint64(len(chunk)) {
		t.Errorf("progress after one chunk = %d, want %d", done, len(chunk))
	}
	for !last {
		if _, last, err = src.GetBuffer(0xFFFF); err != nil {
			t.Fatal(err)
		}
	}
	if done, _ := src.Progress(); done != total {
		t.Errorf("progress after the stream = %d, want %d", done, total)
	}
}
