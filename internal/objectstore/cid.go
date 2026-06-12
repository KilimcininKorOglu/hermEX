package objectstore

import (
	"crypto/sha3"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"

	"hermex/internal/mapi"
)

// Large property values (message bodies, HTML, attachment data) are not stored
// inline; they live in content files under <mailbox>/cid/, addressed by the
// SHA3-256 of their uncompressed bytes and stored zstd-compressed. Identical
// content is written once (dedup).

// isCIDProp reports whether a property value is offloaded to a content file
// rather than stored inline in the property tables: message bodies (plain,
// HTML, RTF), the captured transport headers, and attachment payloads.
func isCIDProp(tag mapi.PropTag) bool {
	switch tag {
	case mapi.PrBody, mapi.PrBodyA,
		mapi.PrHTML, mapi.PrRTFCompressed,
		mapi.PrTransportMessageHeaders, mapi.PrTransportMessageHeadersA,
		mapi.PrAttachDataBin, mapi.PrAttachDataObj:
		return true
	default:
		return false
	}
}

// cidEncoder/cidDecoder are stateless and safe for concurrent EncodeAll/DecodeAll.
var (
	cidEncoder, _ = zstd.NewWriter(nil)
	cidDecoder, _ = zstd.NewReader(nil)
)

// cidString computes the content id for data: "S-" + the first hash byte in hex
// + "/" + the remaining 31 hash bytes in hex. The leading byte snakes the cid
// into a fan-out subdirectory; the full hash is the dedup key.
func cidString(data []byte) string {
	sum := sha3.Sum256(data)
	return "S-" + hex.EncodeToString(sum[:1]) + "/" + hex.EncodeToString(sum[1:])
}

// cidPath maps a content id to its on-disk file (the "/" in the id becomes a
// fan-out directory level).
func (s *Store) cidPath(cid string) string {
	return filepath.Join(s.dir, "cid", filepath.FromSlash(cid)+".zst")
}

// putContent stores data as a content file and returns its content id. If an
// identical file already exists it is reused (content-addressed dedup). The
// write is atomic (temp file + rename).
func (s *Store) putContent(data []byte) (string, error) {
	cid := cidString(data)
	path := s.cidPath(cid)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return cid, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".cid-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(cidEncoder.EncodeAll(data, nil)); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return cid, os.Rename(tmp.Name(), path)
}

// getContent reads and decompresses the content file for cid.
func (s *Store) getContent(cid string) ([]byte, error) {
	compressed, err := os.ReadFile(s.cidPath(cid))
	if err != nil {
		return nil, err
	}
	return cidDecoder.DecodeAll(compressed, nil)
}
