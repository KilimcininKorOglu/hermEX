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
		{"clamped", progressSource{2000, 1000}, false, 1000, 1000},
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
	dir := provisionedDir(t)
	mid := seedInboxMessage(t, dir, "COPYME")
	st, err := objectstore.Open(dir)
	mustNil(t, err)
	defer mustClose(t, st)
	src, err := st.NewCopyToMessageSource(mid, []mapi.PropTag{})
	mustNil(t, err)
	done0, total := src.Progress()
	if done0 != 0 || total == 0 {
		t.Fatalf("initial progress = %d/%d, want 0/>0", done0, total)
	}
	chunk, last, err := src.GetBuffer(16)
	mustNil(t, err)
	if last {
		t.Fatal("first 16-byte chunk unexpectedly finished the stream")
	}
	readBytes := uint64(len(chunk))
	if done, tot := src.Progress(); done != readBytes || tot != total {
		t.Errorf("progress after one chunk = %d/%d, want %d/%d", done, tot, readBytes, total)
	}
	readBytes += drainProgressSource(t, src)
	if readBytes != total {
		t.Errorf("total bytes read = %d, want %d", readBytes, total)
	}
	if done, tot := src.Progress(); done != total || tot != total {
		t.Errorf("progress after the stream = %d/%d, want %d/%d", done, tot, total, total)
	}
}

func drainProgressSource(t *testing.T, src fastTransferSource) uint64 {
	t.Helper()
	var n uint64
	for {
		next, last, err := src.GetBuffer(0xFFFF)
		mustNil(t, err)
		n += uint64(len(next))
		if last {
			return n
		}
	}
}
