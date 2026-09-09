package ext

import (
	"encoding/binary"

	"hermex/internal/mapi"
)

// patchU16 overwrites two already-written bytes at offset with v (little-endian).
// It is used to backpatch a length prefix once the body it counts has been
// written, using a reserve-then-rewrite technique.
func (p *Push) patchU16(offset int, v uint16) {
	binary.LittleEndian.PutUint16(p.buf[offset:], v)
}

// --- RULE_ACTIONS ---

// RuleActions writes a rule action list: a uint16 block count
// (at least one) followed by each action block.
func (p *Push) RuleActions(r mapi.RuleActions) error {
	if len(r.Blocks) == 0 || len(r.Blocks) > 0xFFFF {
		return ErrFormat
	}
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
	p.Uint16(uint16(len(r.Blocks)))
	for _, b := range r.Blocks {
		if err := p.actionBlock(b); err != nil {
			return err
		}
	}
	return nil
}

// RuleActions reads a rule action list; the block count must be
// at least one.
func (p *Pull) RuleActions() (mapi.RuleActions, error) {
	count, err := p.Uint16()
	if err != nil {
		return mapi.RuleActions{}, err
	}
	if count == 0 {
		return mapi.RuleActions{}, ErrFormat
	}
	if err := p.checkCount(uint32(count)); err != nil {
		return mapi.RuleActions{}, err
	}
	r := mapi.RuleActions{Blocks: make([]mapi.ActionBlock, count)}
	for i := range r.Blocks {
		if r.Blocks[i], err = p.actionBlock(); err != nil {
			return r, err
		}
	}
	return r, nil
}

// --- ACTION_BLOCK ---

// actionBlock writes one action block. The leading uint16 length counts every
// byte after itself (type, flavor, flags, and the per-type payload) and is
// backpatched once the payload is written.
func (p *Push) actionBlock(a mapi.ActionBlock) error {
	lenOff := p.Len()
	p.Uint16(0) // length placeholder
	p.Uint8(a.Type)
	p.Uint32(a.Flavor)
	p.Uint32(a.Flags)
	if err := p.actionData(a); err != nil {
		return err
	}
	body := p.Len() - (lenOff + 2)
	if body > 0xFFFF {
		return ErrFormat
	}
	// #nosec G115 -- the range check on the line above refuses a body the 16-bit length cannot carry
	p.patchU16(lenOff, uint16(body))
	return nil
}

func (p *Push) actionData(a mapi.ActionBlock) error {
	write, ok := actionWriters[a.Type]
	if !ok {
		return ErrFormat
	}
	return write(p, a.Data)
}

// actionWriters is the rule-action vocabulary the encoder writes, keyed by action
// type. A type absent from it is refused rather than guessed at. It is populated
// in init because a tag action writes a property value, whose encoder reads the
// property table back.
var actionWriters map[uint8]func(p *Push, v any) error

func init() {
	pushReply := pushTyped(func(p *Push, r mapi.ReplyAction) error {
		p.Uint64(uint64(r.TemplateFolderID))
		p.Uint64(uint64(r.TemplateMessageID))
		p.GUID(r.TemplateGUID)
		return nil
	})
	pushMoveCopy := pushTyped((*Push).moveCopyAction)
	pushForward := pushTyped((*Push).forwardDelegateAction)
	noData := func(*Push, any) error { return nil }

	actionWriters = map[uint8]func(p *Push, v any) error{
		mapi.OpMove:        pushMoveCopy,
		mapi.OpCopy:        pushMoveCopy,
		mapi.OpReply:       pushReply,
		mapi.OpOOFReply:    pushReply,
		mapi.OpForward:     pushForward,
		mapi.OpDelegate:    pushForward,
		mapi.OpDeferAction: pushTyped(func(p *Push, b []byte) error { p.Raw(b); return nil }),
		mapi.OpBounce:      pushTyped(func(p *Push, code uint32) error { p.Uint32(code); return nil }),
		mapi.OpTag:         pushTyped((*Push).TaggedPropVal),
		mapi.OpDelete:      noData,
		mapi.OpMarkAsRead:  noData,
	}
}

func (p *Pull) actionBlock() (mapi.ActionBlock, error) {
	var a mapi.ActionBlock
	length, err := p.Uint16()
	if err != nil {
		return a, err
	}
	if a.Type, err = p.Uint8(); err != nil {
		return a, err
	}
	if a.Flavor, err = p.Uint32(); err != nil {
		return a, err
	}
	if a.Flags, err = p.Uint32(); err != nil {
		return a, err
	}
	read, ok := actionReaders[a.Type]
	if !ok {
		return a, ErrFormat
	}
	a.Data, err = read(p, length)
	return a, err
}

// actionReaders is the rule-action vocabulary the decoder reads. length is the
// block length the header carried, which only the deferred-action payload needs.
var actionReaders map[uint8]func(p *Pull, length uint16) (any, error)

func init() {
	pullMoveCopy := func(p *Pull, _ uint16) (any, error) { return p.moveCopyAction() }
	pullForward := func(p *Pull, _ uint16) (any, error) { return p.forwardDelegateAction() }
	pullReply := func(p *Pull, _ uint16) (any, error) {
		var r mapi.ReplyAction
		folder, err := p.Uint64()
		if err != nil {
			return nil, err
		}
		msg, err := p.Uint64()
		if err != nil {
			return nil, err
		}
		if r.TemplateGUID, err = p.GUID(); err != nil {
			return nil, err
		}
		r.TemplateFolderID, r.TemplateMessageID = mapi.EID(folder), mapi.EID(msg)
		return r, nil
	}
	noData := func(*Pull, uint16) (any, error) { return nil, nil }

	actionReaders = map[uint8]func(p *Pull, length uint16) (any, error){
		mapi.OpMove:     pullMoveCopy,
		mapi.OpCopy:     pullMoveCopy,
		mapi.OpReply:    pullReply,
		mapi.OpOOFReply: pullReply,
		mapi.OpForward:  pullForward,
		mapi.OpDelegate: pullForward,
		mapi.OpDeferAction: func(p *Pull, length uint16) (any, error) {
			// The payload occupies the block length minus the fixed type(1) +
			// flavor(4) + flags(4) header that the length also counts.
			if length < 9 {
				return nil, ErrFormat
			}
			return p.Raw(int(length) - 9)
		},
		mapi.OpBounce:     func(p *Pull, _ uint16) (any, error) { return p.Uint32() },
		mapi.OpTag:        func(p *Pull, _ uint16) (any, error) { return p.TaggedPropVal() },
		mapi.OpDelete:     noData,
		mapi.OpMarkAsRead: noData,
	}
}

// --- MOVECOPY_ACTION ---

func (p *Push) moveCopyAction(m mapi.MoveCopyAction) error {
	if m.SameStore {
		p.Uint8(1)
		// The store eid is absent in the same-store form; a one-byte placeholder
		// is written with eid_size = 1 and skipped on read.
		p.Uint16(1)
		p.Uint8(0)
		svr, err := asType[mapi.SVREID](m.FolderEID)
		if err != nil {
			return err
		}
		return p.SVREID(svr)
	}
	p.Uint8(0)
	if m.StoreEID == nil {
		return ErrFormat
	}
	lenOff := p.Len()
	p.Uint16(0) // eid_size placeholder
	p.StoreEntryID(*m.StoreEID)
	eidSize := p.Len() - (lenOff + 2)
	if eidSize > 0xFFFF {
		return ErrFormat
	}
	// #nosec G115 -- the range check on the line above refuses a body the 16-bit length cannot carry
	p.patchU16(lenOff, uint16(eidSize))
	bin, err := asType[[]byte](m.FolderEID)
	if err != nil {
		return err
	}
	return p.Bin(bin)
}

func (p *Pull) moveCopyAction() (mapi.MoveCopyAction, error) {
	var m mapi.MoveCopyAction
	ss, err := p.Uint8()
	if err != nil {
		return m, err
	}
	eidSize, err := p.Uint16()
	if err != nil {
		return m, err
	}
	if ss != 0 {
		m.SameStore = true
		if _, err := p.Raw(int(eidSize)); err != nil { // skip the placeholder store eid
			return m, err
		}
		svr, err := p.SVREID()
		if err != nil {
			return m, err
		}
		m.FolderEID = svr
		return m, nil
	}
	se, err := p.StoreEntryID()
	if err != nil {
		return m, err
	}
	m.StoreEID = &se
	bin, err := p.Bin()
	if err != nil {
		return m, err
	}
	m.FolderEID = bin
	return m, nil
}

// --- FORWARDDELEGATE_ACTION / RECIPIENT_BLOCK ---

func (p *Push) forwardDelegateAction(fd mapi.ForwardDelegateAction) error {
	if len(fd.Recipients) == 0 || len(fd.Recipients) > 0xFFFF {
		return ErrFormat
	}
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
	p.Uint16(uint16(len(fd.Recipients)))
	for _, rb := range fd.Recipients {
		if err := p.recipientBlock(rb); err != nil {
			return err
		}
	}
	return nil
}

func (p *Pull) forwardDelegateAction() (mapi.ForwardDelegateAction, error) {
	count, err := p.Uint16()
	if err != nil {
		return mapi.ForwardDelegateAction{}, err
	}
	if count == 0 {
		return mapi.ForwardDelegateAction{}, ErrFormat
	}
	if err := p.checkCount(uint32(count)); err != nil {
		return mapi.ForwardDelegateAction{}, err
	}
	fd := mapi.ForwardDelegateAction{Recipients: make([]mapi.RecipientBlock, count)}
	for i := range fd.Recipients {
		if fd.Recipients[i], err = p.recipientBlock(); err != nil {
			return fd, err
		}
	}
	return fd, nil
}

func (p *Push) recipientBlock(rb mapi.RecipientBlock) error {
	if len(rb.PropVals) == 0 || len(rb.PropVals) > 0xFFFF {
		return ErrFormat
	}
	p.Uint8(0) // reserved
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
	p.Uint16(uint16(len(rb.PropVals)))
	for _, pv := range rb.PropVals {
		if err := p.TaggedPropVal(pv); err != nil {
			return err
		}
	}
	return nil
}

func (p *Pull) recipientBlock() (mapi.RecipientBlock, error) {
	if _, err := p.Uint8(); err != nil { // reserved, ignored
		return mapi.RecipientBlock{}, err
	}
	count, err := p.Uint16()
	if err != nil {
		return mapi.RecipientBlock{}, err
	}
	if count == 0 {
		return mapi.RecipientBlock{}, ErrFormat
	}
	if err := p.checkCount(uint32(count)); err != nil {
		return mapi.RecipientBlock{}, err
	}
	rb := mapi.RecipientBlock{PropVals: make([]mapi.TaggedPropVal, count)}
	for i := range rb.PropVals {
		if rb.PropVals[i], err = p.TaggedPropVal(); err != nil {
			return rb, err
		}
	}
	return rb, nil
}
