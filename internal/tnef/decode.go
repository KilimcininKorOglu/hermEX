// Package tnef decodes a Transport Neutral Encapsulation Format stream
// ([MS-OXTNEF]), the winmail.dat body part Outlook and Exchange attach to mail
// whose properties MIME cannot carry. It returns the encapsulated message,
// recipient and attachment properties; mapping them onto a stored message is the
// caller's job.
//
// The stream is sender-controlled, so every count is checked against the bytes
// that remain before anything is allocated, and embedded messages nest to a fixed
// depth only.
package tnef

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"hermex/internal/mapi"
)

// signature opens every TNEF stream.
const signature = 0x223E9F78

// maxDepth bounds how deeply attached messages nest.
const maxDepth = 8

// Attribute levels and ids ([MS-OXTNEF] 2.1.3.2).
const (
	levelMessage    = 0x01
	levelAttachment = 0x02

	attTnefVersion             = 0x00089006
	attOemCodepage             = 0x00069007
	attMessageClass            = 0x00078008
	attOriginalMessageClass    = 0x00070600
	attSubject                 = 0x00018004
	attDateSent                = 0x00038005
	attDateRecd                = 0x00038006
	attMessageID               = 0x00018009
	attBody                    = 0x0002800C
	attPriority                = 0x0004800D
	attDateModified            = 0x00038020
	attDateStart               = 0x00030006
	attDateEnd                 = 0x00030007
	attMsgProps                = 0x00069003
	attRecipTable              = 0x00069004
	attAttachRendData          = 0x00069002
	attAttachData              = 0x0006800F
	attAttachTitle             = 0x00018010
	attAttachCreateDate        = 0x00038012
	attAttachModifyDate        = 0x00038013
	attAttachTransportFilename = 0x00069001
	attAttachment              = 0x00069005
)

// tnefVersion is the only attTnefVersion value a reader accepts.
const tnefVersion = 0x00010000

// Message carries the decoded content of one stream.
type Message struct {
	Props       []Prop
	Recipients  [][]Prop
	Attachments []Attachment
}

// Attachment is one attachment's properties.
type Attachment struct {
	Props []Prop
}

// ErrMalformed wraps every reason a stream is refused.
var ErrMalformed = errors.New("tnef: malformed stream")

// ANSIDecoder converts an 8-bit string in the given Windows code page to UTF-8.
type ANSIDecoder func(raw []byte, codepage uint32) string

// decoder holds the state one stream is read with.
type decoder struct {
	codepage uint32
	decode   ANSIDecoder
	depth    int
}

// Decode reads a whole TNEF stream. decodeANSI converts the stream's 8-bit strings
// from its declared code page; nil keeps them as bytes read as Latin-1.
func Decode(data []byte, decodeANSI ANSIDecoder) (*Message, error) {
	return decodeAt(data, decodeANSI, 0)
}

// decodeAt reads a stream at a nesting depth.
func decodeAt(data []byte, decodeANSI ANSIDecoder, depth int) (*Message, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%w: attached messages nest too deeply", ErrMalformed)
	}
	d := &decoder{decode: decodeANSI, depth: depth}
	m, err := d.stream(&reader{b: data})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	return m, nil
}

// ansi converts one 8-bit string.
func (d *decoder) ansi(raw []byte) string {
	if d.decode != nil {
		return d.decode(raw, d.codepage)
	}
	runes := make([]rune, len(raw))
	for i, c := range raw {
		runes[i] = rune(c)
	}
	return string(runes)
}

// stream reads the header, then every attribute to the end of the data.
func (d *decoder) stream(r *reader) (*Message, error) {
	sig, err := r.u32()
	if err != nil {
		return nil, err
	}
	if sig != signature {
		return nil, errors.New("not a TNEF stream")
	}
	if _, err := r.u16(); err != nil { // the legacy key
		return nil, err
	}
	m := &Message{}
	sawVersion := false
	for r.remaining() > 0 {
		id, err := d.attribute(r, m)
		if err != nil {
			return nil, err
		}
		sawVersion = sawVersion || id == attTnefVersion
	}
	if !sawVersion {
		return nil, errors.New("no attTnefVersion")
	}
	return m, nil
}

// attribute reads one attribute and applies it, returning its id.
func (d *decoder) attribute(r *reader, m *Message) (uint32, error) {
	level, id, data, err := readAttribute(r)
	if err != nil {
		return 0, err
	}
	switch level {
	case levelMessage:
		return id, d.messageAttribute(id, data, m)
	case levelAttachment:
		return id, d.attachmentAttribute(id, data, m)
	}
	return 0, fmt.Errorf("attribute %#x: level %d", id, level)
}

// readAttribute reads one attribute's level, id and data and checks its checksum.
// A reader ignores the checksum of a class attribute: legacy writers got it wrong.
func readAttribute(r *reader) (level byte, id uint32, data []byte, err error) {
	if level, err = r.u8(); err != nil {
		return 0, 0, nil, err
	}
	if id, err = r.u32(); err != nil {
		return 0, 0, nil, err
	}
	n, err := r.count(1)
	if err != nil {
		return 0, 0, nil, err
	}
	if data, err = r.bytes(n); err != nil {
		return 0, 0, nil, err
	}
	sum, err := r.u16()
	if err != nil {
		return 0, 0, nil, err
	}
	if id != attMessageClass && id != attOriginalMessageClass && sum != checksum(data) {
		return 0, 0, nil, fmt.Errorf("attribute %#x: checksum mismatch", id)
	}
	return level, id, data, nil
}

// checksum is the sum of the data bytes, modulo 65536.
func checksum(data []byte) uint16 {
	var s uint16
	for _, c := range data {
		s += uint16(c)
	}
	return s
}

// messageAttribute applies one message-level attribute.
func (d *decoder) messageAttribute(id uint32, data []byte, m *Message) error {
	switch id {
	case attTnefVersion:
		if len(data) != 4 || binary.LittleEndian.Uint32(data) != tnefVersion {
			return errors.New("unsupported attTnefVersion")
		}
	case attOemCodepage:
		if len(data) >= 4 {
			d.codepage = binary.LittleEndian.Uint32(data)
		}
	case attMsgProps:
		props, err := d.propList(&reader{b: data})
		if err != nil {
			return err
		}
		m.Props = append(m.Props, props...)
	case attRecipTable:
		return d.recipTable(data, m)
	default:
		if p, ok := d.legacyMessageProp(id, data); ok {
			m.Props = append(m.Props, p)
		}
	}
	return nil
}

// legacyMessageProp maps a message attribute that has a property counterpart.
// Attributes the MIME wrapper carries more reliably (attFrom, the meeting owner
// attributes) are not read ([MS-OXTNEF] 2.1.3.5.2).
func (d *decoder) legacyMessageProp(id uint32, data []byte) (Prop, bool) {
	switch id {
	case attMessageClass:
		return Prop{Tag: mapi.PrMessageClass, Value: messageClass(d.ansi(trimNul(data)))}, true
	case attOriginalMessageClass:
		return Prop{Tag: mapi.PrOriginalMessageClass, Value: messageClass(d.ansi(trimNul(data)))}, true
	case attSubject:
		return Prop{Tag: mapi.PrSubject, Value: d.ansi(trimNul(data))}, true
	case attBody:
		return Prop{Tag: mapi.PrBody, Value: d.ansi(trimNul(data))}, true
	case attMessageID:
		if b, err := hex.DecodeString(string(trimNul(data))); err == nil {
			return Prop{Tag: mapi.PrSearchKey, Value: b}, true
		}
	case attPriority:
		if len(data) >= 2 {
			return Prop{Tag: mapi.PrImportance, Value: importance(binary.LittleEndian.Uint16(data))}, true
		}
	}
	return dateProp(id, data, messageDates)
}

// messageDates maps the message-level date attributes to their properties.
var messageDates = map[uint32]mapi.PropTag{
	attDateSent: mapi.PrClientSubmitTime, attDateRecd: mapi.PrMessageDeliveryTime,
	attDateModified: mapi.PrLastModificationTime, attDateStart: mapi.PrStartDate,
	attDateEnd: mapi.PrEndDate,
}

// attachmentDates maps the attachment-level date attributes to their properties.
var attachmentDates = map[uint32]mapi.PropTag{
	attAttachCreateDate: mapi.PrCreationTime, attAttachModifyDate: mapi.PrLastModificationTime,
}

// dateProp decodes a Date Time Record attribute into the property it maps to.
func dateProp(id uint32, data []byte, dates map[uint32]mapi.PropTag) (Prop, bool) {
	tag, ok := dates[id]
	if !ok || len(data) < 12 {
		return Prop{}, false
	}
	f := func(i int) int { return int(binary.LittleEndian.Uint16(data[i*2:])) }
	t := time.Date(f(0), time.Month(f(1)), f(2), f(3), f(4), f(5), 0, time.UTC)
	return Prop{Tag: tag, Value: mapi.UnixToNTTime(t)}, true
}

// importance maps an attPriority value to PidTagImportance.
func importance(v uint16) int32 {
	switch v {
	case 1:
		return mapi.ImportanceHigh
	case 3:
		return mapi.ImportanceLow
	}
	return mapi.ImportanceNormal
}

// legacyClasses maps the Microsoft Mail class names to MAPI message classes
// ([MS-OXTNEF] 2.3.3.4).
var legacyClasses = map[string]string{
	"IPM.Microsoft Mail.Note":         "IPM.Note",
	"IPM.Microsoft Mail.Read Receipt": "Report.IPM.Note.IPNRN",
	"IPM.Microsoft Mail.Non-Delivery": "Report.IPM.Note.NDR",
	"IPM.Microsoft Schedule.MtgRespP": "IPM.Schedule.Meeting.Resp.Pos",
	"IPM.Microsoft Schedule.MtgRespN": "IPM.Schedule.Meeting.Resp.Neg",
	"IPM.Microsoft Schedule.MtgRespA": "IPM.Schedule.Meeting.Resp.Tent",
	"IPM.Microsoft Schedule.MtgReq":   "IPM.Schedule.Meeting.Request",
	"IPM.Microsoft Schedule.MtgCncl":  "IPM.Schedule.Meeting.Canceled",
}

// messageClass maps a class attribute to the message class it stands for.
func messageClass(v string) string {
	v = strings.TrimPrefix(v, "Microsoft Mail v3.0 ")
	if c, ok := legacyClasses[v]; ok {
		return c
	}
	return v
}

// recipTable decodes attRecipTable: a counted list of property lists.
func (d *decoder) recipTable(data []byte, m *Message) error {
	r := &reader{b: data}
	n, err := r.count(4)
	if err != nil {
		return err
	}
	for range n {
		row, err := d.propList(r)
		if err != nil {
			return err
		}
		m.Recipients = append(m.Recipients, row)
	}
	return nil
}

// attachmentAttribute applies one attachment-level attribute. attAttachRendData
// opens a new attachment; every other attribute belongs to the last one opened.
func (d *decoder) attachmentAttribute(id uint32, data []byte, m *Message) error {
	if id == attAttachRendData {
		m.Attachments = append(m.Attachments, Attachment{Props: rendDataProps(data)})
		return nil
	}
	if len(m.Attachments) == 0 {
		return fmt.Errorf("attachment attribute %#x before attAttachRendData", id)
	}
	att := &m.Attachments[len(m.Attachments)-1]
	switch id {
	case attAttachData:
		att.Props = append(att.Props, Prop{Tag: mapi.PrAttachDataBin, Value: data})
	case attAttachTitle:
		att.Props = append(att.Props, Prop{Tag: mapi.PrAttachLongFilename, Value: d.ansi(trimNul(data))})
	case attAttachTransportFilename:
		att.Props = append(att.Props, Prop{Tag: mapi.PrAttachTransportName, Value: d.ansi(trimNul(data))})
	case attAttachment:
		props, err := d.propList(&reader{b: data})
		if err != nil {
			return err
		}
		att.Props = append(att.Props, props...)
	default:
		if p, ok := dateProp(id, data, attachmentDates); ok {
			att.Props = append(att.Props, p)
		}
	}
	return nil
}

// attachTagOLE is the PidTagAttachTag value of an OLE attachment.
var attachTagOLE = []byte{0x2A, 0x86, 0x48, 0x86, 0xF7, 0x14, 0x03, 0x0A, 0x03, 0x02, 0x01}

// attachEncodingMacBinary is the PidTagAttachEncoding value of a MacBinary file.
var attachEncodingMacBinary = []byte{0x2A, 0x86, 0x48, 0x86, 0xF7, 0x14, 0x03, 0x0B, 0x01}

// rendDataProps decodes attAttachRendData ([MS-OXTNEF] 2.3.3.9): an OLE attachment
// is tagged as one, and a MacBinary file records its encoding.
func rendDataProps(data []byte) []Prop {
	if len(data) < 14 {
		return nil
	}
	var props []Prop
	if binary.LittleEndian.Uint16(data) == 2 {
		props = append(props, Prop{Tag: mapi.PrAttachTag, Value: attachTagOLE})
	}
	props = append(props, Prop{Tag: mapi.PrRenderingPosition, Value: int32(binary.LittleEndian.Uint32(data[2:]))})
	if binary.LittleEndian.Uint32(data[10:])&1 != 0 {
		props = append(props, Prop{Tag: mapi.PrAttachEncoding, Value: attachEncodingMacBinary})
	}
	return props
}

// DecodeEmbedded decodes the TNEF stream an attached message's object value holds.
func DecodeEmbedded(o Object, decodeANSI ANSIDecoder, depth int) (*Message, error) {
	if o.IID != IIDMessage {
		return nil, fmt.Errorf("%w: object %v is not a message", ErrMalformed, o.IID)
	}
	return decodeAt(o.Data, decodeANSI, depth+1)
}
