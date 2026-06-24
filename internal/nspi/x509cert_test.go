package nspi

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestGetPropsX509Cert proves the address book serves a GAL entry's published
// S/MIME certificate (PR_EMS_AB_X509_CERT) as a multi-value binary, so Outlook can
// encrypt to the recipient.
func TestGetPropsX509Cert(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	certDER := []byte{0x30, 0x82, 0x01, 0x02, 1, 2, 3, 4, 5, 6, 7}
	if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{Mode: "browser", Cert: certDER}); err != nil {
		t.Fatalf("set identity: %v", err)
	}
	st.Close()

	s := NewServer(maskedGAL{
		{DisplayName: "alice@hermex.test", Address: "alice@hermex.test", StorePath: dir},
	}, testGUID)
	cols := []mapi.PropTag{mapi.PrEmsAbX509Cert}
	result, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase, codePage: 1252}, cols)))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	v, ok := row.Get(mapi.PrEmsAbX509Cert)
	if !ok {
		t.Fatal("PR_EMS_AB_X509_CERT not in row")
	}
	got, ok := v.([][]byte)
	if !ok || len(got) != 1 || !bytes.Equal(got[0], certDER) {
		t.Errorf("cert = %#v, want one entry %v", v, certDER)
	}
}

// TestGetPropsX509CertAbsent proves a mailbox with no published cert yields the
// PT_ERROR(ecNotFound) marker, not an empty value.
func TestGetPropsX509CertAbsent(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.Close() // no identity

	s := NewServer(maskedGAL{
		{DisplayName: "alice@hermex.test", Address: "alice@hermex.test", StorePath: dir},
	}, testGUID)
	cols := []mapi.PropTag{mapi.PrEmsAbX509Cert}
	result, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase, codePage: 1252}, cols)))
	if result != ecWarnWithErrors {
		t.Fatalf("result = %#x, want ecWarnWithErrors", result)
	}
	if v, _ := row.Get(errorTag(mapi.PrEmsAbX509Cert)); v != ecNotFound {
		t.Errorf("absent cert marker = %v, want ecNotFound", v)
	}
}
