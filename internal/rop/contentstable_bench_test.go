package rop

import (
	"fmt"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// Opening a folder in a MAPI client runs Logon -> OpenFolder -> GetContentsTable
// -> SetColumns -> QueryRows. GetContentsTable snapshots the whole folder, so the
// cost of that sequence grows with the folder rather than with the page the client
// reads. These benchmarks measure the sequence at several folder sizes, so a change
// to the snapshot has a before-and-after number rather than an argument.
//
// The sizes stay modest on purpose: seedInboxBulk delivers real messages through
// AppendMessage, which writes one eml cache file each. A folder of several hundred
// thousand messages is not seeded here; read the numbers as a curve across the
// sizes below, and never quote a larger figure as measured.
var benchFolderSizes = []int{1000, 5000, 20000}

// benchColumns is the column set a mail list view asks for.
var benchColumns = []mapi.PropTag{
	mapi.PrSubject, mapi.PrSenderName, mapi.PrMessageDeliveryTime, mapi.PrMessageFlags,
}

// seedInboxBulk delivers n messages into the mailbox's Inbox, opening the store
// once for the whole run. seedInboxMessage opens and closes it per message, which
// dominates the time at these sizes.
//
// Each subject and sender is distinct, so a sort has to order every row and a
// restriction on one subject matches exactly one row, which is the case that walks
// the whole folder.
func seedInboxBulk(tb testing.TB, dir string, n int) {
	tb.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		tb.Fatal(err)
	}
	defer mustClose(tb, st)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		raw := fmt.Sprintf("From: sender%06d@hermex.test\r\nTo: alice@hermex.test\r\n"+
			"Subject: Subject %06d\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nhello body\r\n", i, i)
		info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), base.Add(time.Duration(i)*time.Second), 0)
		if err != nil {
			tb.Fatalf("seed %d: %v", i, err)
		}
		if info.UID == 0 {
			tb.Fatalf("seed %d: the store assigned no IMAP UID", i)
		}
	}
}

// benchSubject is the subject seedInboxBulk gives row i.
func benchSubject(i int) string { return fmt.Sprintf("Subject %06d", i) }

// openAndPage runs one client folder-open: a fresh session, the table, the column
// set, and the first page. prepare runs between SetColumns and QueryRows, which is
// where a sort or a restriction goes. It returns how many rows the page carried.
func openAndPage(tb testing.TB, dir string, prepare func(tb testing.TB, sess *Session, tableH uint32)) int {
	tb.Helper()
	sess := NewSession(dir, nil, "")
	defer sess.Close()

	tableH := openInboxContentsTable(tb, sess)
	benchROPOK(tb, dispatchOn(sess, buildSetColumns(0, benchColumns), tableH), ropSetColumns, "SetColumns")
	if prepare != nil {
		prepare(tb, sess, tableH)
	}
	return benchRowCount(tb, dispatchOn(sess, buildQueryRows(0, 0, 1, benchPageRows), tableH))
}

// benchROPOK fails the benchmark when a setup ROP did not succeed, so a sort or a
// restriction the server refused is not timed as if it had run.
func benchROPOK(tb testing.TB, resp []byte, ropID uint8, what string) {
	tb.Helper()
	if len(resp) < 6 || resp[0] != ropID {
		tb.Fatalf("%s: response %x is not the expected ROP %#x", what, resp, ropID)
	}
	if ec := uint32(resp[2]) | uint32(resp[3])<<8 | uint32(resp[4])<<16 | uint32(resp[5])<<24; ec != ecSuccess {
		tb.Fatalf("%s ec = %#x", what, ec)
	}
}

// benchRowCount reads the RowCount field out of a QueryRows response, so a
// benchmark iteration that silently returned nothing fails instead of scoring well.
func benchRowCount(tb testing.TB, resp []byte) int {
	tb.Helper()
	// Header: RopId, hindex, ec, SeekPosition, then RowCount.
	if len(resp) < 9 {
		tb.Fatalf("QueryRows response too short: %d bytes", len(resp))
	}
	if resp[0] != ropQueryRows {
		tb.Fatalf("RopId = %#x, want QueryRows", resp[0])
	}
	ec := uint32(resp[2]) | uint32(resp[3])<<8 | uint32(resp[4])<<16 | uint32(resp[5])<<24
	if ec != ecSuccess {
		tb.Fatalf("QueryRows ec = %#x", ec)
	}
	return int(uint16(resp[7]) | uint16(resp[8])<<8)
}

// benchPageRows is the page size openAndPage asks QueryRows for.
const benchPageRows = 50

// sortBySubject orders the table on PR_SUBJECT ([MS-OXCTABL] 2.2.2.3).
func sortBySubject(tb testing.TB, sess *Session, tableH uint32) {
	tb.Helper()
	benchROPOK(tb, dispatchOn(sess, buildSortTable(0, 0, 0, []sortOrderEntry{{mapi.PrSubject, sortAscend}}), tableH), ropSortTable, "SortTable")
}

// restrictToFirstSubject filters the table to the one row seedInboxBulk gave
// benchSubject(0) ([MS-OXCTABL] 2.2.2.4).
func restrictToFirstSubject(tb testing.TB, sess *Session, tableH uint32) {
	tb.Helper()
	benchROPOK(tb, dispatchOn(sess, buildRestrict(0, propEq(mapi.PrSubject, benchSubject(0))), tableH), ropRestrict, "Restrict")
}

// runFolderOpenBenchmark seeds each folder size once and times the open sequence.
// Every iteration must page wantRows rows, so a run whose table came back empty or
// unfiltered fails instead of scoring well.
func runFolderOpenBenchmark(b *testing.B, prepare func(tb testing.TB, sess *Session, tableH uint32), wantRows int) {
	for _, n := range benchFolderSizes {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			seedInboxBulk(b, dir, n)
			if rows := openAndPage(b, dir, prepare); rows != wantRows {
				b.Fatalf("first page = %d rows, want %d", rows, wantRows)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if rows := openAndPage(b, dir, prepare); rows != wantRows {
					b.Fatalf("page = %d rows, want %d", rows, wantRows)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(wantRows), "rows/page")
		})
	}
}

// BenchmarkContentsTableOpen measures the plain date-ordered open: no sort, no
// restriction. The client reads one page of 50 rows whatever the folder holds, so
// every millisecond that grows with the folder is snapshot cost.
func BenchmarkContentsTableOpen(b *testing.B) {
	runFolderOpenBenchmark(b, nil, benchPageRows)
}

// BenchmarkContentsTableSorted measures an open that sorts on PR_SUBJECT, which
// makes rebuildView read the sort key for every row in the folder.
func BenchmarkContentsTableSorted(b *testing.B) {
	runFolderOpenBenchmark(b, sortBySubject, benchPageRows)
}

// BenchmarkContentsTableRestricted measures an open filtered to one matching row,
// which makes rebuildView read the restriction's property for every row in the
// folder. This is the shape of a client opening a view filtered on unread mail.
func BenchmarkContentsTableRestricted(b *testing.B) {
	runFolderOpenBenchmark(b, restrictToFirstSubject, 1)
}

// TestFolderOpenHarnessPagesWhatTheSpecSays runs the benchmark's open sequence on
// a folder larger than one page. QueryRows returns at most the rows requested
// ([MS-OXCTABL] 2.2.2.5), so the plain and sorted opens page 50 rows, and the
// restriction matches the one row seeded with that subject.
func TestFolderOpenHarnessPagesWhatTheSpecSays(t *testing.T) {
	dir := t.TempDir()
	seedInboxBulk(t, dir, benchPageRows+10)
	for _, c := range []struct {
		name    string
		prepare func(tb testing.TB, sess *Session, tableH uint32)
		want    int
	}{
		{"plain", nil, benchPageRows},
		{"sorted", sortBySubject, benchPageRows},
		{"restricted", restrictToFirstSubject, 1},
	} {
		if got := openAndPage(t, dir, c.prepare); got != c.want {
			t.Errorf("%s open paged %d rows, want %d", c.name, got, c.want)
		}
	}
}

// TestFolderOpenRefusesARestrictOnATableNeverIssued sends RopRestrict on a table
// handle the session never issued. It must answer ecError, and the real table must
// still page unfiltered rows, so the refused filter landed on nothing.
func TestFolderOpenRefusesARestrictOnATableNeverIssued(t *testing.T) {
	dir := t.TempDir()
	seedInboxBulk(t, dir, benchPageRows+10)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	benchROPOK(t, dispatchOn(sess, buildSetColumns(0, benchColumns), tableH), ropSetColumns, "SetColumns")

	bogus := tableH + 1000
	wantEC(t, dispatchOn(sess, buildRestrict(0, propEq(mapi.PrSubject, benchSubject(0))), bogus), ropRestrict, ecError, "Restrict on a table never issued")
	if got := benchRowCount(t, dispatchOn(sess, buildQueryRows(0, 0, 1, benchPageRows), tableH)); got != benchPageRows {
		t.Errorf("real table after the refusal paged %d rows, want %d unfiltered", got, benchPageRows)
	}
}
