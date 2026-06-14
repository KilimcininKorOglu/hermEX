package activesync

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"

	"hermex/internal/wbxml"
)

// maxRequestBody caps a command's WBXML request body.
const maxRequestBody = 4 << 20

// readWBXML reads and decodes the WBXML request body.
func readWBXML(r *http.Request) (*wbxml.Node, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		return nil, err
	}
	return wbxml.Unmarshal(body)
}

// writeWBXML encodes a response tree and writes it with the EAS content type.
func writeWBXML(w http.ResponseWriter, root *wbxml.Node) {
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	_, _ = w.Write(wbxml.Marshal(root))
}

// defaultProtocol is the single EAS protocol version v1 implements and
// advertises. Clients negotiate down to it via the MS-ASProtocolVersion header.
const defaultProtocol = "14.1"

// asRequest holds the command parameters carried in the request line, from
// either the plain query string or the MS-ASHTTP base64-packed form.
type asRequest struct {
	cmd        string
	user       string
	deviceID   string
	deviceType string
	policyKey  string
}

// commandNames maps the MS-ASHTTP base64 command codes to their command names.
var commandNames = map[byte]string{
	0:  "Sync",
	1:  "SendMail",
	2:  "SmartForward",
	3:  "SmartReply",
	4:  "GetAttachment",
	9:  "FolderSync",
	10: "FolderCreate",
	11: "FolderDelete",
	12: "FolderUpdate",
	13: "MoveItems",
	14: "GetItemEstimate",
	15: "MeetingResponse",
	16: "Search",
	17: "Settings",
	18: "Ping",
	19: "ItemOperations",
	20: "Provision",
	21: "ResolveRecipients",
	22: "ValidateCert",
}

// protocolVersion reports the negotiated protocol version from the request
// header, falling back to the single version v1 supports.
func protocolVersion(r *http.Request) string {
	if v := r.Header.Get("MS-ASProtocolVersion"); v != "" {
		return v
	}
	return defaultProtocol
}

// parseQuery extracts the command parameters from the request. ActiveSync sends
// them either as a plain query string (Cmd=...&User=...&DeviceId=...) or, since
// 12.1, as a single base64-packed token (MS-ASHTTP §2.2.1.1.1.1).
func parseQuery(r *http.Request) (asRequest, error) {
	if cmd := r.URL.Query().Get("Cmd"); cmd != "" {
		q := r.URL.Query()
		return asRequest{
			cmd:        cmd,
			user:       q.Get("User"),
			deviceID:   q.Get("DeviceId"),
			deviceType: q.Get("DeviceType"),
			policyKey:  q.Get("PolicyKey"),
		}, nil
	}
	if r.URL.RawQuery == "" {
		return asRequest{}, errors.New("missing command")
	}
	return parseBase64Query(r.URL.RawQuery)
}

// parseBase64Query decodes the MS-ASHTTP base64-packed command string. The
// decoded layout is: protocol version (1), command code (1), locale (2),
// device id (length-prefixed), policy key (length-prefixed, 0 or 4 bytes),
// device type (length-prefixed), then optional command-parameter TLVs (ignored
// here — the variant fields below are not needed to route a command).
func parseBase64Query(raw string) (asRequest, error) {
	data, err := decodeBase64(raw)
	if err != nil {
		return asRequest{}, err
	}
	p := &packed{b: data}
	p.skip(1) // protocol version (the header is authoritative)
	code, ok := p.byte()
	if !ok {
		return asRequest{}, errBadPacked
	}
	p.skip(2) // locale
	devID, ok := p.lenPrefixed()
	if !ok {
		return asRequest{}, errBadPacked
	}
	policy, ok := p.lenPrefixed()
	if !ok {
		return asRequest{}, errBadPacked
	}
	devType, ok := p.lenPrefixed()
	if !ok {
		return asRequest{}, errBadPacked
	}
	req := asRequest{
		cmd:        commandNames[code],
		deviceID:   string(devID),
		deviceType: string(devType),
	}
	if len(policy) == 4 {
		key := uint32(policy[0]) | uint32(policy[1])<<8 | uint32(policy[2])<<16 | uint32(policy[3])<<24
		req.policyKey = strconv.FormatUint(uint64(key), 10)
	}
	return req, nil
}

// errBadPacked reports a malformed base64-packed command string.
var errBadPacked = errors.New("malformed base64 command")

// decodeBase64 decodes the packed query, trying the encodings ActiveSync
// clients use: URL-safe and standard, each with and without padding.
func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.URLEncoding, base64.RawURLEncoding,
		base64.StdEncoding, base64.RawStdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errBadPacked
}

// packed reads the MS-ASHTTP base64-packed structure with bounds checks.
type packed struct {
	b   []byte
	off int
}

func (p *packed) byte() (byte, bool) {
	if p.off >= len(p.b) {
		return 0, false
	}
	v := p.b[p.off]
	p.off++
	return v, true
}

func (p *packed) skip(n int) {
	p.off += n
}

// lenPrefixed reads a single length byte then that many value bytes.
func (p *packed) lenPrefixed() ([]byte, bool) {
	n, ok := p.byte()
	if !ok {
		return nil, false
	}
	if p.off+int(n) > len(p.b) {
		return nil, false
	}
	v := p.b[p.off : p.off+int(n)]
	p.off += int(n)
	return v, true
}

// handleOptions answers an EAS OPTIONS request with the capability headers: the
// supported protocol versions and the advertised command set (MS-ASHTTP §3.1).
func (s *Server) handleOptions(w http.ResponseWriter) {
	w.Header().Set("MS-Server-ActiveSync", defaultProtocol)
	w.Header().Set("MS-ASProtocolVersions", defaultProtocol)
	w.Header().Set("MS-ASProtocolCommands", "Provision,FolderSync,Sync,GetItemEstimate,Ping,SendMail,SmartForward,SmartReply,Settings")
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}
