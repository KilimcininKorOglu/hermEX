package ews

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"hermex/internal/oxews"
)

// findItemPageReq is FindItem on the Inbox with an IndexedPageItemView
// ([MS-OXWSSRCH] 2.2.4.6): view is the element's attribute list.
func findItemPageReq(view string) string {
	return wrapRequest(`<FindItem Traversal="Shallow" xmlns="` + nsMessages + `">` +
		`<ItemShape><BaseShape>Default</BaseShape></ItemShape>` +
		`<IndexedPageItemView ` + view + `/>` +
		`<ParentFolderIds><t:DistinguishedFolderId Id="inbox" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindItem>`)
}

var (
	rootAttrsRE = regexp.MustCompile(`RootFolder ([^>]*)>`)
	subjectRE   = regexp.MustCompile(`<(?:\w+:)?Subject>([^<]*)</`)
)

// pageOf posts one paged FindItem and returns the RootFolder attributes and the
// subjects listed, in order.
func pageOf(t *testing.T, post func(string) string, view string) (string, []string) {
	t.Helper()
	out := post(findItemPageReq(view))
	root := rootAttrsRE.FindStringSubmatch(out)
	if len(root) != 2 {
		t.Fatalf("no RootFolder in the FindItem answer: %s", out)
	}
	var subjects []string
	for _, m := range subjectRE.FindAllStringSubmatch(out, -1) {
		subjects = append(subjects, m[1])
	}
	return root[1], subjects
}

// TestFindItemPagesTheView lists a five-message Inbox through IndexedPageItemView.
// [MS-OXWSSRCH] has the server return at most MaxEntriesReturned items starting
// Offset items from the BasePoint, report the total in TotalItemsInView, the
// offset of the next page in IndexedPagingOffset, and whether the page reached
// the end of the view in IncludesLastItemInRange. Hostile views (a negative or
// past-the-end Offset, a zero or negative size) must neither fail nor list more
// than the view holds.
func TestFindItemPagesTheView(t *testing.T) {
	var raws []string
	for i := range 5 {
		raws = append(raws, strings.Replace(plainMessage, "Subject: Hello EWS", fmt.Sprintf("Subject: Msg %d", i), 1))
	}
	ts, _ := seededWithMessage(t, raws...)
	unauthResp, unauthOut := soapPost(t, ts, findItemPageReq(`MaxEntriesReturned="10" Offset="0" BasePoint="Beginning"`), false)
	wantEq(t, "unauth FindItem status", unauthResp.StatusCode, http.StatusUnauthorized)
	wantNotContains(t, "unauth FindItem body", unauthOut, "Msg ")

	post := func(body string) string {
		resp, out := soapPost(t, ts, body, true)
		if resp.StatusCode != 200 {
			t.Fatalf("FindItem status = %d: %s", resp.StatusCode, out)
		}
		return out
	}
	_, all := pageOf(t, post, `MaxEntriesReturned="100" Offset="0" BasePoint="Beginning"`)
	if len(all) != 5 {
		t.Fatalf("the whole view lists %d items, want 5", len(all))
	}

	attrs, got := pageOf(t, post, `MaxEntriesReturned="2" Offset="1" BasePoint="Beginning"`)
	wantEq(t, "the page's items", strings.Join(got, ","), strings.Join(all[1:3], ","))
	for _, want := range []string{`IndexedPagingOffset="3"`, `TotalItemsInView="5"`, `IncludesLastItemInRange="false"`} {
		if !strings.Contains(attrs, want) {
			t.Errorf("middle page RootFolder %q lacks %s", attrs, want)
		}
	}

	attrs, got = pageOf(t, post, `MaxEntriesReturned="5" Offset="3" BasePoint="Beginning"`)
	wantEq(t, "the last page's items", strings.Join(got, ","), strings.Join(all[3:], ","))
	if !strings.Contains(attrs, `IncludesLastItemInRange="true"`) || !strings.Contains(attrs, `IndexedPagingOffset="5"`) {
		t.Errorf("last page RootFolder %q, want IncludesLastItemInRange true at offset 5", attrs)
	}

	_, got = pageOf(t, post, `MaxEntriesReturned="2" Offset="0" BasePoint="End"`)
	wantEq(t, "the page counted from the end", strings.Join(got, ","), strings.Join(all[3:], ","))

	for _, view := range []string{
		`MaxEntriesReturned="2" Offset="-4" BasePoint="Beginning"`,
		`MaxEntriesReturned="2" Offset="99" BasePoint="Beginning"`,
		`MaxEntriesReturned="0" Offset="0" BasePoint="Beginning"`,
		`MaxEntriesReturned="-1" Offset="0" BasePoint="End"`,
		`MaxEntriesReturned="2" Offset="99" BasePoint="End"`,
	} {
		if _, got := pageOf(t, post, view); len(got) > 5 {
			t.Errorf("view %s listed %d items, more than the folder holds", view, len(got))
		}
	}
}

// TestFindItemCapsAnUnboundedView builds a view of one item more than the
// server returns at once. Exchange caps one FindItem at 1000 items
// (EWSFindCountLimit) whether or not the client sent a page size, and says so
// with IncludesLastItemInRange false, so a client pages on instead of the server
// rendering a folder of any size into one response.
func TestFindItemCapsAnUnboundedView(t *testing.T) {
	full := func() *findItemRoot {
		return &findItemRoot{TotalItemsInView: maxFindItems + 1, Items: itemsWrap{BaseItems: make([]oxews.Item, maxFindItems+1)}}
	}
	root := full()
	pageFindRoot(root, nil)
	wantEq(t, "items returned without a view", len(root.Items.BaseItems), maxFindItems)
	wantEq(t, "IncludesLastItemInRange", root.IncludesLastItemInRange, false)
	wantEq(t, "TotalItemsInView", root.TotalItemsInView, maxFindItems+1)

	root = full()
	pageFindRoot(root, &indexedPageView{MaxEntries: 5000, BasePoint: "Beginning"})
	wantEq(t, "items returned for a page size past the cap", len(root.Items.BaseItems), maxFindItems)
}
