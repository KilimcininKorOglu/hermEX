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
	defer st.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		raw := fmt.Sprintf("From: sender%06d@hermex.test\r\nTo: alice@hermex.test\r\n"+
			"Subject: Subject %06d\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nhello body\r\n", i, i)
		if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), base.Add(time.Duration(i)*time.Second), 0); err != nil {
			tb.Fatalf("seed %d: %v", i, err)
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
	if _, _ = sess.Dispatch(buildSetColumns(0, benchColumns), []uint32{tableH}); tableH == 0 {
		tb.Fatal("no table handle")
	}
	if prepare != nil {
		prepare(tb, sess, tableH)
	}
	resp, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 50), []uint32{tableH})
	return benchRowCount(tb, resp)
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

// runFolderOpenBenchmark seeds each folder size once and times the open sequence.
func runFolderOpenBenchmark(b *testing.B, prepare func(tb testing.TB, sess *Session, tableH uint32)) {
	for _, n := range benchFolderSizes {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			seedInboxBulk(b, dir, n)
			rows := openAndPage(b, dir, prepare)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				openAndPage(b, dir, prepare)
			}
			b.StopTimer()
			b.ReportMetric(float64(rows), "rows/page")
		})
	}
}

// BenchmarkContentsTableOpen measures the plain date-ordered open: no sort, no
// restriction. The client reads one page of 50 rows whatever the folder holds, so
// every millisecond that grows with the folder is snapshot cost.
func BenchmarkContentsTableOpen(b *testing.B) {
	runFolderOpenBenchmark(b, nil)
}

// BenchmarkContentsTableSorted measures an open that sorts on PR_SUBJECT, which
// makes rebuildView read the sort key for every row in the folder.
func BenchmarkContentsTableSorted(b *testing.B) {
	runFolderOpenBenchmark(b, func(tb testing.TB, sess *Session, tableH uint32) {
		tb.Helper()
		sess.Dispatch(buildSortTable(0, 0, 0, []sortOrderEntry{{mapi.PrSubject, sortAscend}}), []uint32{tableH})
	})
}

// BenchmarkContentsTableRestricted measures an open filtered to one matching row,
// which makes rebuildView read the restriction's property for every row in the
// folder. This is the shape of a client opening a view filtered on unread mail.
func BenchmarkContentsTableRestricted(b *testing.B) {
	runFolderOpenBenchmark(b, func(tb testing.TB, sess *Session, tableH uint32) {
		tb.Helper()
		sess.Dispatch(buildRestrict(0, propEq(mapi.PrSubject, benchSubject(0))), []uint32{tableH})
	})
}
