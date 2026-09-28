package webmail2api

import (
	"net/http"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// Read receipts in webmail follow the three choices Outlook on the web offers,
// stored per mailbox (objectstore.ReadReceiptConfig): "always" sends the receipt
// when a message goes from unread to read, "never" sends nothing, and "ask" shows
// the reader a prompt when they open a message whose receipt is still pending.
// A delegate reading a shared mailbox follows the owner's setting and the receipt
// names the owner as the reader, as a read through EWS does. Answering a prompt
// needs the right to change the message, the same right that marks it read.

// readReceiptResponses maps the stored response to its wire name.
var readReceiptResponses = map[objectstore.ReadReceiptResponse]string{
	objectstore.ReadReceiptAsk:    "ask",
	objectstore.ReadReceiptAlways: "always",
	objectstore.ReadReceiptNever:  "never",
}

// readReceiptSettingsJSON is the read-receipt settings resource. A PUT carries the
// whole object.
type readReceiptSettingsJSON struct {
	Response           string `json:"response"`
	SuppressActiveSync bool   `json:"suppressActiveSync"`
}

// handleGetReadReceiptSettings returns the caller's read-receipt settings.
func (s *Server) handleGetReadReceiptSettings(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	cfg, err := st.GetReadReceiptConfig()
	if err != nil {
		logError("read-receipt-settings", err, logging.Fields{})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "settings unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, readReceiptSettingsJSON{
		Response: readReceiptResponses[cfg.Response], SuppressActiveSync: cfg.SuppressActiveSync,
	})
}

// handlePutReadReceiptSettings replaces the caller's read-receipt settings. A
// response other than the three choices is refused.
func (s *Server) handlePutReadReceiptSettings(w http.ResponseWriter, r *http.Request) {
	var in readReceiptSettingsJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	cfg := objectstore.ReadReceiptConfig{Response: -1, SuppressActiveSync: in.SuppressActiveSync}
	for resp, name := range readReceiptResponses {
		if name == in.Response {
			cfg.Response = resp
		}
	}
	if !cfg.Response.Valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown response"})
		return
	}
	st, c, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	if err := st.SetReadReceiptConfig(cfg); err != nil {
		logError("read-receipt-settings", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save settings"})
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// receiptOnRead applies the reader's setting to a message they just read. For
// "always" a message taken from unread to read sends its receipt; for "ask" the
// detail reports whether a receipt is still pending, so the reader is prompted.
// Under "always" the reader is prompted too when the receipt could not go out on
// its own, which RFC 8098 asks for a request naming an address other than the
// Return-Path. d is nil when there is no detail to fill (a flag change from the
// list).
func (s *Server) receiptOnRead(d *mailDetailJSON, mb *mailboxCtx, messageID int64, becameRead bool) {
	if d == nil && !becameRead {
		return
	}
	cfg, err := mb.st.GetReadReceiptConfig()
	if err != nil {
		logError("read-receipt-settings", err, logging.Fields{"user": mb.user})
		return
	}
	if cfg.Response == objectstore.ReadReceiptAlways && becameRead {
		s.sendReceipt(mb, messageID, mta.ReceiptAutomatic)
	}
	if cfg.Response == objectstore.ReadReceiptNever || d == nil {
		return
	}
	pending, err := mta.ReceiptPending(mb.st, messageID)
	if err != nil {
		logError("read-receipt", err, logging.Fields{"user": mb.user})
	}
	d.ReceiptRequested = pending
}

// sendReceipt sends a message's pending read receipt from the caller's mailbox,
// reporting false when it failed; a failure is logged. An automatic receipt RFC
// 8098 does not allow is not a failure, and leaves the request pending.
func (s *Server) sendReceipt(mb *mailboxCtx, messageID int64, mode mta.ReceiptMode) bool {
	err := mta.SendRequestedReceipt(s.accounts, s.spool, mb.st, messageID, mb.identity(), mode, time.Now())
	if err != nil {
		logError("read-receipt", err, logging.Fields{"user": mb.user, "message": messageID})
		return false
	}
	return true
}

// handleMailReadReceipt answers the reader's reply to a receipt prompt: send
// sends the receipt the message asks for, otherwise the request is declined and
// no later read on any protocol sends it.
func (s *Server) handleMailReadReceipt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Send bool   `json:"send"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	mb, fid, uid, ok := s.locateMailbox(w, r, req.ID, accessWrite)
	if !ok {
		return
	}
	defer mb.st.Close()
	m, err := mb.st.MessageByUID(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if req.Send {
		if !s.sendReceipt(mb, m.ID, mta.ReceiptManual) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "receipt failed"})
			return
		}
	} else if err := mta.ConsumeReceiptRequest(mb.st, m.ID); err != nil {
		logError("read-receipt", err, logging.Fields{"user": mb.user})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not decline"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
