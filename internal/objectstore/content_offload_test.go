package objectstore

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestContentPropertyOffload proves the property layer offloads content
// properties (bodies, HTML, attachment payloads, captured headers) to content
// files instead of storing them inline, and reverses the offload transparently
// on read so callers see the original value. This is the symmetry CreateMessage
// and GetMessageProperties depend on.
func TestContentPropertyOffload(t *testing.T) {
	s := openTestStore(t)

	body := strings.Repeat("gövde satırı ünïçödé\n", 400)        // PtUnicode
	html := bytes.Repeat([]byte("<p>içerik</p>"), 1000)          // PtBinary
	attach := bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 2000) // PtBinary
	headers := strings.Repeat("X-Header: değer\r\n", 300)        // PtUnicode

	want := mapi.PropertyValues{
		{Tag: mapi.PrBody, Value: body},
		{Tag: mapi.PrHTML, Value: html},
		{Tag: mapi.PrAttachDataBin, Value: attach},
		{Tag: mapi.PrTransportMessageHeaders, Value: headers},
		{Tag: mapi.PrImportance, Value: int32(mapi.ImportanceHigh)}, // inline, alongside
	}
	mustNoErr(t, "set store properties", s.SetStoreProperties(want))

	// Every value round-trips identically through the offload.
	got, err := s.GetStoreProperties()
	mustNoErr(t, "get store properties", err)
	gm := asMap(got)
	for _, w := range want {
		g, ok := gm[w.Tag]
		if !ok {
			t.Errorf("%s missing after round-trip", w.Tag)
			continue
		}
		if !reflect.DeepEqual(g, w.Value) {
			t.Errorf("%s did not round-trip: got %T, want %T", w.Tag, g, w.Value)
		}
	}

	for _, tg := range []mapi.PropTag{mapi.PrBody, mapi.PrHTML, mapi.PrAttachDataBin, mapi.PrTransportMessageHeaders} {
		wantOffloaded(t, s, tg)
	}

	// An inline (non-content) property is stored as its native value, not a
	// content id.
	var col any
	mustScan(t, s.objdb.QueryRow(
		`SELECT propval FROM store_properties WHERE proptag=?`, int64(uint32(mapi.PrImportance))), &col)
	iv, _ := col.(int64)
	wantEq(t, "inline PrImportance column", iv, int64(mapi.ImportanceHigh))
}

// wantOffloaded proves a content property's column holds a content id, not the
// payload, and that the content file exists on disk.
func wantOffloaded(t *testing.T, s *Store, tg mapi.PropTag) {
	t.Helper()
	var col any
	mustScan(t, s.objdb.QueryRow(`SELECT propval FROM store_properties WHERE proptag=?`, int64(uint32(tg))), &col)
	cid, err := asString(col)
	mustNoErr(t, tg.String()+" column", err)
	if !strings.HasPrefix(cid, "S-") {
		t.Errorf("%s column = %q, want a content id", tg, cid)
	}
	if len(cid) >= 200 {
		t.Errorf("%s column holds %d bytes; payload was stored inline, not offloaded", tg, len(cid))
	}
	if _, err := os.Stat(s.cidPath(cid)); err != nil {
		t.Errorf("%s content file missing: %v", tg, err)
	}
}
