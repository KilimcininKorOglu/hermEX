package rop

import "testing"

// progressSource is a FastTransfer source with fixed progress.
type progressSource struct{ done, total uint64 }

func (progressSource) GetBuffer(int) ([]byte, bool, error) { return nil, true, nil }
func (p progressSource) Progress() (uint64, uint64)        { return p.done, p.total }

// plainSource is a FastTransfer source that cannot report progress.
type plainSource struct{}

func (plainSource) GetBuffer(int) ([]byte, bool, error) { return nil, true, nil }

// TestStepCounts proves the GetBuffer step counts follow the source's progress:
// a small transfer is counted in bytes, a transfer too large for 16 bits is
// scaled so its total fits, an empty one still has one step, a finished one has
// every step done, and a source without progress reports none.
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
		{"no progress", plainSource{}, false, 0, 0},
	}
	for _, c := range cases {
		done, total := stepCounts(c.src, c.last)
		if done != c.done || total != c.total {
			t.Errorf("%s: steps = %d/%d, want %d/%d", c.name, done, total, c.done, c.total)
		}
	}
}
