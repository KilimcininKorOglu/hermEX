package nspi

import (
	"errors"
	"slices"

	"hermex/internal/ext"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// getPropsRequest is the decoded GetProps body ([MS-OXNSPI] 2.2.4 /
// [MS-OXCMAPIHTTP] 2.2.5.4): flags, an optional STAT (whose cur_rec selects the
// entry and whose code page is echoed), and an optional explicit column set.
// hasTags distinguishes an absent column set (return the default property bag)
// from a present-but-empty one.
type getPropsRequest struct {
	stat     stat
	proptags []mapi.PropTag
	hasTags  bool
}

func pullGetProps(body []byte) (getPropsRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r getPropsRequest
	if err := p.Skip(4); err != nil { // flags (fEphID; v1 emits permanent EIDs)
		return r, err
	}
	hasStat, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasStat != 0 {
		if r.stat, err = pullStat(p); err != nil {
			return r, err
		}
	}
	hasTags, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasTags != 0 {
		r.hasTags = true
		if r.proptags, err = p.PropTagsLong(); err != nil {
			return r, err
		}
	}
	return r, skipAuxIn(p)
}

// GetProps handles the NSPI GetProps request ([MS-OXNSPI] 2.2.4): it returns the
// property values of the single entry addressed by STAT.cur_rec. With an
// explicit column set, a requested-but-absent property is returned as a
// PT_ERROR(ecNotFound) marker and the result is ecWarnWithErrors; with no column
// set, the entry's full default bag is returned. An unknown entry yields a row
// of PT_ERROR markers ([MS-OXNSPI] 3.1.4.1.7 point 11).
func (s *Server) GetProps(body []byte, caller string) []byte {
	req, err := pullGetProps(body)
	if err != nil {
		return s.encodeGetProps(ecError, 0, nil)
	}
	r := s.getPropsCore(req, caller)
	return s.encodeGetProps(r.result, r.codePage, r.row)
}

// getPropsResult is the transport-neutral outcome of GetProps: a result code,
// the echoed code page, and the single property row (PT_ERROR markers ride
// inside it on a warn-with-errors).
type getPropsResult struct {
	result   uint32
	codePage uint32
	row      mapi.PropertyValues
}

// getPropsCore runs the GetProps semantics on a decoded request,
// transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share it.
func (s *Server) getPropsCore(req getPropsRequest, caller string) getPropsResult {
	if req.stat.codePage == cpWinUnicode {
		return getPropsResult{result: ecNotSupported, codePage: req.stat.codePage}
	}
	if len(req.proptags) > 100 {
		return getPropsResult{result: ecTableTooBig, codePage: req.stat.codePage}
	}

	g := s.snapshot(caller)
	u, ok := g.resolveEntry(req.stat.curRec)
	if !ok {
		tags := req.proptags
		if !req.hasTags {
			tags = defaultColumns
		}
		row := projectRow(nil, tags)
		return getPropsResult{result: ecWarnWithErrors, codePage: req.stat.codePage, row: row}
	}

	bag := galUserProps(u)
	if !req.hasTags {
		return getPropsResult{result: ecSuccess, codePage: req.stat.codePage, row: bag}
	}
	bag = append(bag, s.lazyProps(u, req.proptags)...)
	row, hasErr := projectProps(bag, req.proptags)
	result := ecSuccess
	if hasErr {
		result = ecWarnWithErrors
	}
	return getPropsResult{result: result, codePage: req.stat.codePage, row: row}
}

// lazyProps serves the properties that cost a read of their own, and only when
// the client asked for them, so an ordinary table walk pays nothing for them.
func (s *Server) lazyProps(u galUser, requested []mapi.PropTag) []mapi.TaggedPropVal {
	var extra []mapi.TaggedPropVal
	// The portrait comes from the mailbox's cross-protocol user-photo property.
	if u.storePath != "" && slices.Contains(requested, mapi.PrEmsAbThumbnailPhoto) {
		photo, err := userPhoto(u.storePath)
		s.logMailboxReadFail("photo", u, err)
		if err == nil && photo != nil {
			extra = append(extra, mapi.TaggedPropVal{Tag: mapi.PrEmsAbThumbnailPhoto, Value: photo})
		}
	}
	// The published S/MIME certificate lets Outlook encrypt to a GAL recipient.
	// The value is a multi-value binary carrying the one cert DER.
	if u.storePath != "" && slices.Contains(requested, mapi.PrEmsAbX509Cert) {
		cert, err := userX509Cert(u.storePath)
		s.logMailboxReadFail("cert", u, err)
		if err == nil && cert != nil {
			extra = append(extra, mapi.TaggedPropVal{Tag: mapi.PrEmsAbX509Cert, Value: [][]byte{cert}})
		}
	}
	return append(extra, s.profileProps(u, requested)...)
}

// logMailboxReadFail records a failed mailbox read the row is served without. A
// mailbox that was never provisioned has no portrait and no certificate, which is
// an answer rather than a failure, so it records nothing.
func (s *Server) logMailboxReadFail(what string, u galUser, err error) {
	if err == nil || errors.Is(err, objectstore.ErrNotProvisioned) {
		return
	}
	s.Logger.Emit(logging.Event{
		Level:     logging.LevelWarn,
		Subsystem: logging.NSPI,
		Name:      "gal." + what + ".read.fail",
		Fields:    logging.Fields{"address": u.smtp},
		Err:       err.Error(),
	})
}

// profileProps reads the directory profile fields (title, department, phones,
// etc.) the client asked for, so Outlook's GAL detail shows them. The optional
// reader is absent for the static directory.
func (s *Server) profileProps(u galUser, requested []mapi.PropTag) []mapi.TaggedPropVal {
	pr, ok := s.gal.(userPropertyReader)
	if !ok || !anyTagRequested(requested, galProfileProptags) {
		return nil
	}
	props, err := pr.GetUserProperties(u.smtp)
	if err != nil {
		return nil
	}
	var extra []mapi.TaggedPropVal
	for _, tag := range galProfileProptags {
		if v := props[uint32(tag)]; v != "" && slices.Contains(requested, tag) {
			extra = append(extra, mapi.TaggedPropVal{Tag: tag, Value: v})
		}
	}
	return extra
}

// userPropertyReader is the optional gal-source capability to read a user's MAPI
// properties; SQLDirectory satisfies it, the static directory does not.
type userPropertyReader interface {
	GetUserProperties(username string) (map[uint32]string, error)
}

// galProfileProptags are the person properties served in the GAL from a user's
// directory profile (PtUnicode strings), so Outlook's detail view shows them.
var galProfileProptags = []mapi.PropTag{
	mapi.PrTitle, mapi.PrDepartmentName, mapi.PrCompanyName, mapi.PrOfficeLocation,
	mapi.PrGivenName, mapi.PrSurname, mapi.PrBusinessTelephoneNumber, mapi.PrMobileTelephoneNumber,
}

// anyTagRequested reports whether any of want appears in requested.
func anyTagRequested(requested, want []mapi.PropTag) bool {
	for _, t := range want {
		if slices.Contains(requested, t) {
			return true
		}
	}
	return false
}

// projectProps builds a GetProps row: each requested tag maps to the entry's
// value, or a PT_ERROR(ecNotFound) marker when the entry has no such property.
// hasErr reports whether any marker was produced.
func projectProps(bag mapi.PropertyValues, tags []mapi.PropTag) (row mapi.PropertyValues, hasErr bool) {
	for _, tag := range tags {
		if !bag.Has(tag) {
			hasErr = true
			break
		}
	}
	return projectRow(bag, tags), hasErr
}

// projectRow builds the row projectProps does, for a caller that needs only the
// row: each requested tag's value, or PT_ERROR(ecNotFound) where the bag lacks it.
func projectRow(bag mapi.PropertyValues, tags []mapi.PropTag) mapi.PropertyValues {
	var row mapi.PropertyValues
	for _, tag := range tags {
		if v, ok := bag.Get(tag); ok {
			row = append(row, mapi.TaggedPropVal{Tag: tag, Value: v})
		} else {
			row = append(row, mapi.TaggedPropVal{Tag: errorTag(tag), Value: ecNotFound})
		}
	}
	return row
}

// errorTag rewrites a proptag's type to PT_ERROR, the form a row carries for a
// requested-but-absent property (its value is then the SCODE).
func errorTag(tag mapi.PropTag) mapi.PropTag {
	return mapi.PropTag(uint32(tag)&0xFFFF0000 | uint32(mapi.PtError))
}

// userPhoto reads a mailbox's portrait bytes from its object store, or nil when
// none is set. A store that cannot be opened, read or closed is an error; a
// missing mailbox answers objectstore.ErrNotProvisioned and is never created by
// this read.
func userPhoto(storePath string) (photo []byte, err error) {
	st, err := objectstore.OpenExisting(storePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, st.Close())
	}()
	return st.UserPhoto()
}

// userX509Cert returns the mailbox's published S/MIME public certificate (raw
// DER), or nil when none is published. A store that cannot be opened, read or
// closed is an error; a missing mailbox answers objectstore.ErrNotProvisioned and
// is never created by this read.
func userX509Cert(storePath string) (cert []byte, err error) {
	st, err := objectstore.OpenExisting(storePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, st.Close())
	}()
	id, ok, err := st.GetSmimeIdentity()
	if err != nil || !ok || len(id.Cert) == 0 {
		return nil, err
	}
	return id.Cert, nil
}

// encodeGetProps frames a GetProps response: status + result + the echoed code
// page + the single row as a self-describing TPROPVAL_ARRAY on a success or a
// warn-with-errors (the PT_ERROR markers ride inside it), else a single 0, then
// an empty AuxiliaryBuffer.
func (s *Server) encodeGetProps(result, codePage uint32, row mapi.PropertyValues) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)        // status
	p.Uint32(result)   // result
	p.Uint32(codePage) // echoed code page
	if result != ecSuccess && result != ecWarnWithErrors {
		p.Uint8(0)
	} else {
		p.Uint8(0xFF)
		if err := p.PropertyValuesLong(row); err != nil {
			return s.encodeGetProps(ecError, codePage, nil)
		}
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}
