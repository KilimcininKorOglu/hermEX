package ext

import (
	"bytes"
	"errors"
	"testing"

	"hermex/internal/mapi"
)

func TestBinShortVector(t *testing.T) {
	p := NewPush(0)
	if err := p.BinShort([]byte{0xAA, 0xBB}); err != nil {
		t.Fatalf("BinShort: %v", err)
	}
	want := []byte{0x02, 0x00, 0xAA, 0xBB}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("BinShort bytes = % X, want % X", p.Bytes(), want)
	}
}

func TestBinShortRoundTrip(t *testing.T) {
	in := []byte{1, 2, 3, 4, 5}
	p := NewPush(FlagWCount) // width must stay 16-bit regardless of FlagWCount
	if err := p.BinShort(in); err != nil {
		t.Fatalf("BinShort: %v", err)
	}
	got, err := NewPull(p.Bytes(), FlagWCount).BinShort()
	if err != nil {
		t.Fatalf("pull BinShort: %v", err)
	}
	if !bytes.Equal(got, in) {
		t.Fatalf("round-trip = % X, want % X", got, in)
	}
}

func TestBinShortOversize(t *testing.T) {
	if err := NewPush(0).BinShort(make([]byte, 0x10000)); !errors.Is(err, ErrFormat) {
		t.Fatalf("oversize err = %v, want ErrFormat", err)
	}
}

func TestBinExVector(t *testing.T) {
	p := NewPush(0)
	p.BinEx([]byte{0xAA, 0xBB})
	want := []byte{0x02, 0x00, 0x00, 0x00, 0xAA, 0xBB}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("BinEx bytes = % X, want % X", p.Bytes(), want)
	}
}

func TestBinExRoundTrip(t *testing.T) {
	in := []byte{9, 8, 7}
	p := NewPush(0) // width must stay 32-bit even without FlagWCount
	p.BinEx(in)
	got, err := NewPull(p.Bytes(), 0).BinEx()
	if err != nil {
		t.Fatalf("pull BinEx: %v", err)
	}
	if !bytes.Equal(got, in) {
		t.Fatalf("round-trip = % X, want % X", got, in)
	}
}

func TestBlobNoPrefix(t *testing.T) {
	p := NewPush(0)
	p.Blob([]byte{1, 2, 3})
	want := []byte{1, 2, 3}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("Blob bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).Blob()
	if err != nil {
		t.Fatalf("pull Blob: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip = % X, want % X", got, want)
	}
}

func TestBlobConsumesRest(t *testing.T) {
	// A blob has no self-describing length: it must run to the buffer end even
	// when other fields precede it.
	p := NewPush(0)
	p.Uint16(0xBEEF)
	p.Blob([]byte{4, 5, 6, 7})

	pull := NewPull(p.Bytes(), 0)
	if v, _ := pull.Uint16(); v != 0xBEEF {
		t.Fatalf("prefix = %#x, want 0xBEEF", v)
	}
	got, err := pull.Blob()
	if err != nil {
		t.Fatalf("pull Blob: %v", err)
	}
	if !bytes.Equal(got, []byte{4, 5, 6, 7}) {
		t.Fatalf("blob = % X, want 04 05 06 07", got)
	}
	if pull.Remaining() != 0 {
		t.Fatalf("Remaining after Blob = %d, want 0", pull.Remaining())
	}
}

func TestSystemTimeVector(t *testing.T) {
	st := mapi.SystemTime{Year: 2023, Month: 11, Day: 5, Hour: 2}
	p := NewPush(0)
	p.SystemTime(st)
	want := []byte{
		0xE7, 0x07, // year 2023
		0x0B, 0x00, // month 11
		0x00, 0x00, // dayofweek 0
		0x05, 0x00, // day 5
		0x02, 0x00, // hour 2
		0x00, 0x00, // minute 0
		0x00, 0x00, // second 0
		0x00, 0x00, // milliseconds 0
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("SystemTime bytes = % X, want % X", p.Bytes(), want)
	}
}

func TestSystemTimeRoundTrip(t *testing.T) {
	st := mapi.SystemTime{
		Year: 2026, Month: 6, DayOfWeek: 5, Day: 12,
		Hour: 13, Minute: 37, Second: 42, Milliseconds: 250,
	}
	p := NewPush(0)
	p.SystemTime(st)
	if p.Len() != 16 {
		t.Fatalf("SystemTime length = %d, want 16", p.Len())
	}
	got, err := NewPull(p.Bytes(), 0).SystemTime()
	if err != nil {
		t.Fatalf("pull SystemTime: %v", err)
	}
	if got != st {
		t.Fatalf("round-trip = %+v, want %+v", got, st)
	}
}
