package objectstore

import (
	"encoding/binary"
	"testing"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// TestContentDownloadProgressMode proves a download that asks for progress
// reports it ([MS-OXCFXICS] 2.2.4.3.19, 2.2.4.3.20): the stream opens with the
// totals of each class and every message change is preceded by its own size and
// class, so a client can show how far the synchronization got.
func TestContentDownloadProgressMode(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	normal := mustCreateMessage(t, s, fld, contactMsg("Ada Lovelace"))
	fai := mustCreateMessage(t, s, fld, richMsg("fai"))
	_, err := s.objdb.Exec(`UPDATE messages SET is_associated=1 WHERE message_id=?`, fai)
	mustNoErr(t, "mark the message associated", err)
	sizes := map[bool]int64{false: storedSize(t, s, normal), true: storedSize(t, s, fai)}
	if sizes[false] == 0 || sizes[true] == 0 {
		t.Fatalf("a message has no stored size: %v", sizes)
	}

	dc, err := s.NewContentDownload(fld, downloadState(t, s), SyncNormal|SyncAssociated|SyncProgressMode, 0, nil)
	mustNoErr(t, "new content download", err)
	items := drainDownload(t, dc, 48)

	if len(items) < 2 || !items[0].IsMarker || items[0].Marker != ics.MarkerIncrSyncProgressMode {
		t.Fatalf("stream does not open with INCRSYNCPROGRESSMODE: %+v", items[:min(len(items), 2)])
	}
	info, _ := items[1].Prop.Value.([]byte)
	if len(info) != 32 {
		t.Fatalf("ProgressInformation is %d bytes, want 32", len(info))
	}
	le := binary.LittleEndian
	wantEq(t, "FAI message count", le.Uint32(info[4:]), uint32(1))
	wantEq(t, "FAI message size", le.Uint64(info[8:]), uint64(sizes[true]))
	wantEq(t, "normal message count", le.Uint32(info[16:]), uint32(1))
	wantEq(t, "normal message size", le.Uint64(info[24:]), uint64(sizes[false]))

	wantEq(t, "INCRSYNCCHG count", countMarkers(items, ics.MarkerIncrSyncChg), 2)
	for i, it := range items {
		if it.IsMarker && it.Marker == ics.MarkerIncrSyncChg {
			wantProgressBefore(t, items, i, sizes)
		}
	}
	wantStreamEnd(t, items)
}

// wantProgressBefore checks the INCRSYNCCHG at index i is preceded by
// INCRSYNCPROGRESSPERMSG, the size of its class's message and the class itself.
func wantProgressBefore(t *testing.T, items []ics.Item, i int, sizes map[bool]int64) {
	t.Helper()
	if i < 3 || !items[i-3].IsMarker || items[i-3].Marker != ics.MarkerIncrSyncProgressPerMsg {
		t.Fatalf("INCRSYNCCHG at %d is not preceded by INCRSYNCPROGRESSPERMSG", i)
	}
	size, _ := items[i-2].Prop.Value.(int32)
	isFAI, _ := items[i-1].Prop.Value.(bool)
	wantEq(t, "per-message size", int64(size), sizes[isFAI])
}

// TestContentDownloadProgressAdvances proves a download's progress starts at
// zero, totals the size of every change, and reaches the total once every change
// was written.
func TestContentDownloadProgressAdvances(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	a := mustCreateMessage(t, s, fld, contactMsg("Ada Lovelace"))
	b := mustCreateMessage(t, s, fld, contactMsg("Grace Hopper"))
	want := uint64(storedSize(t, s, a) + storedSize(t, s, b))
	dc, err := s.NewContentDownload(fld, downloadState(t, s), SyncNormal, 0, nil)
	mustNoErr(t, "new content download", err)
	if done, total := dc.Progress(); done != 0 || total != want {
		t.Fatalf("progress before the first chunk = %d/%d, want 0/%d", done, total, want)
	}
	drainDownload(t, dc, 48)
	if done, total := dc.Progress(); done != want || total != want {
		t.Errorf("progress after the download = %d/%d, want %d/%d", done, total, want, want)
	}
}

// TestContentDownloadWithoutProgressMode proves the progress elements stay out of
// a download that did not ask for them.
func TestContentDownloadWithoutProgressMode(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mustCreateMessage(t, s, fld, contactMsg("Ada Lovelace"))
	dc, err := s.NewContentDownload(fld, downloadState(t, s), SyncNormal, 0, nil)
	mustNoErr(t, "new content download", err)
	items := drainDownload(t, dc, 48)
	wantEq(t, "INCRSYNCPROGRESSMODE count", countMarkers(items, ics.MarkerIncrSyncProgressMode), 0)
	wantEq(t, "INCRSYNCPROGRESSPERMSG count", countMarkers(items, ics.MarkerIncrSyncProgressPerMsg), 0)
}
