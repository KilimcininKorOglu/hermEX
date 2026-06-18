package objectstore

import (
	"crypto/sha3"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"

	"hermex/internal/mapi"
)

// Large property values (message bodies, HTML, attachment data) are not stored
// inline; they live in content files under <mailbox>/cid/, addressed by the
// SHA3-256 of their uncompressed bytes and stored zstd-compressed. Identical
// content is written once (dedup).

// cidPropTags are the property tags whose values are offloaded to a content file
// rather than stored inline: message bodies (plain, HTML, RTF), the captured
// transport headers, attachment payloads, and the preserved original of an S/MIME
// or iCalendar message. This is the single source of truth shared by isCIDProp
// (the offload decision) and the content sweep (the reference scan).
var cidPropTags = []mapi.PropTag{
	mapi.PrBody, mapi.PrBodyA,
	mapi.PrHTML, mapi.PrRTFCompressed,
	mapi.PrTransportMessageHeaders, mapi.PrTransportMessageHeadersA,
	mapi.PrAttachDataBin, mapi.PrAttachDataObj,
	mapi.PrSmimeOriginal, mapi.PrIcalOriginal,
}

// isCIDProp reports whether a property value is offloaded to a content file
// rather than stored inline in the property tables.
func isCIDProp(tag mapi.PropTag) bool {
	return slices.Contains(cidPropTags, tag)
}

// propertyTables is every table that can hold a content-offloaded property value
// (a content id). The sweep scans all of them so a file referenced from any one
// is never reclaimed.
var propertyTables = []string{
	"store_properties", "folder_properties", "message_properties",
	"recipients_properties", "attachment_properties",
}

// SweepOrphanContent reclaims content files that no property references, returning
// the number removed. The content store is content-addressed and deduplicated —
// one file can back several properties across different messages — so deleting a
// file the moment one referencing property is removed would corrupt the others.
// Mark-and-sweep is therefore the only safe reclamation, and it is offered as an
// explicit maintenance pass rather than an inline delete.
//
// It must run with no concurrent writes to the mailbox: a write that dedup-reuses
// an existing file and inserts its property row between the on-disk snapshot and
// the reference scan could otherwise race that file into deletion. The on-disk
// files are snapshotted before references are collected, so a file created during
// the pass is never a deletion candidate.
func (s *Store) SweepOrphanContent() (int, error) {
	root := filepath.Join(s.dir, "cid")
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	var files []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".zst") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	referenced, err := s.referencedContentIDs()
	if err != nil {
		return 0, err
	}
	var removed int
	for _, path := range files {
		cid, ok := cidFromPath(root, path)
		if !ok {
			continue // not a content file the store manages
		}
		if _, used := referenced[cid]; used {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// referencedContentIDs gathers every content id still referenced by a property in
// any property table.
func (s *Store) referencedContentIDs() (map[string]struct{}, error) {
	ph := make([]string, len(cidPropTags))
	args := make([]any, len(cidPropTags))
	for i, t := range cidPropTags {
		ph[i] = "?"
		args[i] = int64(uint32(t))
	}
	in := strings.Join(ph, ",")
	refs := make(map[string]struct{})
	for _, table := range propertyTables {
		// table is an internal constant (propertyTables), never caller input.
		rows, err := s.objdb.Query(`SELECT propval FROM `+table+` WHERE proptag IN (`+in+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var col any
			if err := rows.Scan(&col); err != nil {
				rows.Close()
				return nil, err
			}
			if cid, err := asString(col); err == nil {
				refs[cid] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return refs, nil
}

// cidFromPath reverses cidPath: it turns a content file path back into its content
// id, reporting false for a file the store does not address.
func cidFromPath(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", false
	}
	cid := filepath.ToSlash(strings.TrimSuffix(rel, ".zst"))
	if !strings.HasPrefix(cid, "S-") {
		return "", false
	}
	return cid, true
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
