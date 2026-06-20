package nspi

import (
	"bytes"
	"encoding/binary"
	"strings"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// ModLinkAtt flags and address-book EntryID type markers ([MS-OXNSPI] 2.2.9).
const (
	modFlagDelete uint32 = 0x00000001 // remove the entry ids rather than add them

	entryidTypeEphemeral  byte   = 0x87       // EphemeralEntryID first byte
	entryidTypePermanent  uint32 = 0x00000000 // PermanentEntryID flags word
	permanentEIDHeaderLen        = 28         // flags(4) + provider GUID(16) + version(4) + display type(4)
)

// modLinkAttRequest is the decoded NspiModLinkAtt body ([MS-OXCMAPIHTTP] 2.2.5.10):
// flags, the property tag being modified, the target MId, and the array of entry
// ids to add or remove.
type modLinkAttRequest struct {
	flags    uint32
	proptag  uint32
	mid      uint32
	entryIDs [][]byte
}

// pullModLinkAtt decodes a ModLinkAtt request body: flags, proptag, mid, an
// optional binary-array of entry ids (a present byte then count + each {cb, bytes}),
// then the AuxiliaryBuffer.
func pullModLinkAtt(body []byte) (modLinkAttRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r modLinkAttRequest
	var err error
	if r.flags, err = p.Uint32(); err != nil {
		return r, err
	}
	if r.proptag, err = p.Uint32(); err != nil {
		return r, err
	}
	if r.mid, err = p.Uint32(); err != nil {
		return r, err
	}
	hasEntries, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasEntries != 0 {
		count, err := p.Uint32()
		if err != nil {
			return r, err
		}
		for range count {
			cb, err := p.Uint32()
			if err != nil {
				return r, err
			}
			b, err := p.Raw(int(cb))
			if err != nil {
				return r, err
			}
			r.entryIDs = append(r.entryIDs, b)
		}
	}
	return r, skipAuxIn(p)
}

// ModLinkAtt handles NspiModLinkAtt ([MS-OXNSPI] 3.1.4.1.6 / [MS-OXCMAPIHTTP]
// 2.2.5.10): it edits the public-delegate list of the mailbox at mid. user is the
// authenticated caller, who may edit only their own list. The MAPI/HTTP transport
// alone carries this op; RPC/HTTP leaves it unsupported.
func (s *Server) ModLinkAtt(body []byte, user string) []byte {
	req, err := pullModLinkAtt(body)
	if err != nil {
		return s.encodeModLinkAtt(ecError)
	}
	return s.encodeModLinkAtt(s.modLinkAttCore(req, user))
}

// modLinkAttCore runs the ModLinkAtt semantics, transport-neutral. Only the
// public-delegates property is supported; the caller must be the target mailbox's
// owner; each resolvable entry id is added (deduped) or, with MOD_FLAG_DELETE,
// removed; the updated list is persisted.
func (s *Server) modLinkAttCore(req modLinkAttRequest, user string) uint32 {
	if req.proptag != uint32(mapi.PrEmsAbPublicDelegates) {
		return ecNotSupported
	}
	if req.mid == 0 {
		return ecInvalidObject
	}
	writer, ok := s.gal.(delegateWriter)
	if !ok {
		return ecNotSupported
	}
	g := s.snapshot()
	target, ok := g.byMID(req.mid)
	if !ok {
		return ecInvalidObject
	}
	// A caller may edit only their own delegate list: the target mailbox must be
	// the authenticated principal. The compare is against the primary SMTP, so a
	// caller authenticated by an alias is denied (safe — the admin editor is the
	// fallback; see the internal spec).
	if !strings.EqualFold(strings.TrimSpace(user), target.smtp) {
		return ecAccessDenied
	}
	list, err := writer.Delegates(target.smtp)
	if err != nil {
		return ecError
	}
	del := req.flags&modFlagDelete != 0
	for _, eid := range req.entryIDs {
		addr, ok := g.entryIDToAddress(eid)
		if !ok {
			continue
		}
		if del {
			list = removeAddr(list, addr)
		} else if !containsAddr(list, addr) {
			list = append(list, addr)
		}
	}
	if err := writer.SetDelegates(target.smtp, list); err != nil {
		return ecError
	}
	return ecSuccess
}

// entryIDToAddress reverses an address-book EntryID to its SMTP address. An
// ephemeral id (32 bytes, first byte 0x87) carries the MId at offset 28; a
// permanent id (flags word 0) carries the X500 DN, NUL-terminated, after the
// 28-byte header. An id of any other type, too short, or with an unterminated DN
// yields no address — a malformed id from a client must not panic.
func (g gal) entryIDToAddress(eid []byte) (string, bool) {
	if len(eid) < 20 {
		return "", false
	}
	if len(eid) == 32 && eid[0] == entryidTypeEphemeral {
		mid := binary.LittleEndian.Uint32(eid[28:32])
		if u, ok := g.byMID(mid); ok {
			return u.smtp, true
		}
		return "", false
	}
	if len(eid) > permanentEIDHeaderLen && binary.LittleEndian.Uint32(eid[:4]) == entryidTypePermanent {
		// The DN runs from the header to its NUL terminator; an unterminated DN
		// (found == false) is rejected rather than read past the buffer.
		if dn, _, found := bytes.Cut(eid[permanentEIDHeaderLen:], []byte{0}); found {
			if smtp, ok := dnToSMTP(string(dn)); ok {
				return smtp, true
			}
		}
	}
	return "", false
}

// removeAddr returns list without any (case-insensitive) occurrence of addr,
// clearing a pre-existing duplicate fully. It does not alias the input.
func removeAddr(list []string, addr string) []string {
	var out []string
	for _, a := range list {
		if !strings.EqualFold(a, addr) {
			out = append(out, a)
		}
	}
	return out
}

// containsAddr reports whether list already holds addr (case-insensitive).
func containsAddr(list []string, addr string) bool {
	for _, a := range list {
		if strings.EqualFold(a, addr) {
			return true
		}
	}
	return false
}

// encodeModLinkAtt frames a ModLinkAtt response: status + result + an empty
// AuxiliaryBuffer.
func (s *Server) encodeModLinkAtt(result uint32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status
	p.Uint32(result) // result
	p.Uint32(0)      // AuxiliaryBufferSize
	return p.Bytes()
}
