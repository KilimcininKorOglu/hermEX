package mta

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mailreport"
	"hermex/internal/serve"
)

// The two media types RFC 8460 §3 gives a report posted over HTTPS.
const (
	tlsrptGzipType = "application/tlsrpt+gzip"
	tlsrptJSONType = "application/tlsrpt+json"
)

// TLSReportHandler accepts SMTP TLS reports posted over HTTPS (RFC 8460 §3), the
// endpoint a domain names with rua=https:. It stores a report the way a mailed
// one is stored, under the one hosted domain every policy in it names.
//
// Anyone may post here, with no credential, so the body is bounded before it is
// read and a report naming a domain this server does not host is refused. That is
// the same rule the mail path applies, where the report must name the postmaster
// domain it was sent to. Every answer carries a fixed message; the cause of a
// refusal goes to the log only.
type TLSReportHandler struct {
	reports ReportRecorder
	logger  *logging.Logger
	maxBody atomic.Int64
}

// NewTLSReportHandler returns a handler storing through reports, with the
// built-in body cap in force until SetMaxBodyBytes installs the operator's.
func NewTLSReportHandler(reports ReportRecorder, logger *logging.Logger) *TLSReportHandler {
	h := &TLSReportHandler{reports: reports, logger: logger}
	h.maxBody.Store(directory.DefaultTLSReportBytes)
	return h
}

// SetMaxBodyBytes installs the body cap. A value below 1 leaves the cap as it is,
// so a missing setting never lifts the bound.
func (h *TLSReportHandler) SetMaxBodyBytes(n int64) {
	if n > 0 {
		h.maxBody.Store(n)
	}
}

// ServeHTTP reads, checks and stores one posted report. RFC 8460 §3 has the
// reporter treat any 2xx as delivered, so a stored report and a resent copy of
// one already stored both answer 200.
func (h *TLSReportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gzipped, ok := tlsrptBodyType(r.Header.Get("Content-Type"))
	if !ok {
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody.Load()))
	if err != nil {
		h.refuseRead(w, r, err)
		return
	}
	res, err := mailreport.ParseTLS(body, gzipped)
	if err != nil {
		h.emit(r, logging.LevelWarn, "report.parse_failed", "", logging.Fields{}, err)
		http.Error(w, "the body is not a TLS report", http.StatusBadRequest)
		return
	}
	h.store(w, r, res)
}

// tlsrptBodyType reports whether a Content-Type is one of the two report types,
// and whether it is the gzipped one.
func tlsrptBodyType(contentType string) (gzipped, ok bool) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false, false
	}
	switch strings.ToLower(mediaType) {
	case tlsrptGzipType:
		return true, true
	case tlsrptJSONType:
		return false, true
	}
	return false, false
}

// refuseRead answers a body that could not be read: 413 past the cap, 400
// otherwise.
func (h *TLSReportHandler) refuseRead(w http.ResponseWriter, r *http.Request, err error) {
	if tooBig := new(http.MaxBytesError); errors.As(err, &tooBig) {
		h.emit(r, logging.LevelWarn, "report.too_large", "", logging.Fields{"limit": tooBig.Limit}, nil)
		http.Error(w, "the report is too large", http.StatusRequestEntityTooLarge)
		return
	}
	h.emit(r, logging.LevelWarn, "report.read_failed", "", logging.Fields{}, err)
	http.Error(w, "the body could not be read", http.StatusBadRequest)
}

// store writes the parsed report under the one hosted domain it names.
func (h *TLSReportHandler) store(w http.ResponseWriter, r *http.Request, res mailreport.Result) {
	fields := logging.Fields{"kind": string(res.Kind), "report_id": res.ReportID()}
	domain, ok := soleDomain(res)
	if !ok {
		fields["report_domains"] = strings.Join(res.Domains(), ",")
		h.emit(r, logging.LevelWarn, "report.domain_mismatch", "", fields, nil)
		http.Error(w, "the report does not name one domain", http.StatusForbidden)
		return
	}
	src := directory.ReportSource{ReceivedAt: time.Now().Unix(), Via: directory.ViaHTTPS, RemoteAddr: serve.ClientAddr(r)}
	outcome, err := storeReport(h.reports, res, domain, src)
	switch outcome {
	case reportStored:
		h.emit(r, logging.LevelInfo, "report.stored", domain, fields, nil)
		w.WriteHeader(http.StatusOK)
	case reportDuplicate:
		h.emit(r, logging.LevelInfo, "report.duplicate", domain, fields, nil)
		w.WriteHeader(http.StatusOK)
	case reportNotHosted:
		h.emit(r, logging.LevelWarn, "report.domain_mismatch", domain, fields, err)
		http.Error(w, "the report names a domain not hosted here", http.StatusForbidden)
	default:
		h.emit(r, logging.LevelError, "report.store_failed", domain, fields, err)
		http.Error(w, "the report could not be stored", http.StatusInternalServerError)
	}
}

// emit records one outcome of a posted report. User names the postmaster address
// of the report's domain, as on the mail path, so the log viewer filters one
// domain's reports the same way whichever way they arrived.
func (h *TLSReportHandler) emit(r *http.Request, level logging.Level, name, domain string, fields logging.Fields, err error) {
	fields["via"] = directory.ViaHTTPS
	e := logging.Event{Level: level, Subsystem: logging.MTA, Name: name, RemoteAddr: serve.ClientAddr(r), Fields: fields}
	if domain != "" {
		e.User = reportMailbox + "@" + domain
	}
	if err != nil {
		e.Err = err.Error()
	}
	h.logger.Emit(e)
}
