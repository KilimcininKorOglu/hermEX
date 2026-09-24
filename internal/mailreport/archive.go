package mailreport

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"strings"

	"golang.org/x/text/encoding/htmlindex"

	"hermex/internal/mime"
)

// format is how one attachment carries a report.
type format int

const (
	formatNone format = iota
	formatZip
	formatGzip
	formatXML
	formatTLSGzip
	formatTLSJSON
)

// typeFormats maps an attachment's media type to its format. The TLS types are
// registered by RFC 8460 §5.3; the aggregate report has no registered type, so
// reporters label it with the generic archive and XML types listed here.
var typeFormats = map[string]format{
	"application/tlsrpt+gzip":      formatTLSGzip,
	"application/tlsrpt+json":      formatTLSJSON,
	"application/zip":              formatZip,
	"application/x-zip":            formatZip,
	"application/x-zip-compressed": formatZip,
	"application/gzip":             formatGzip,
	"application/x-gzip":           formatGzip,
	"text/xml":                     formatXML,
	"application/xml":              formatXML,
}

// nameFormats maps a file name suffix to its format, for reporters that send the
// attachment as application/octet-stream. The order matters: ".json.gz" must be
// seen before ".gz", because a gzipped TLS report is not an aggregate report.
var nameFormats = []struct {
	suffix string
	f      format
}{
	{".json.gz", formatTLSGzip},
	{".json", formatTLSJSON},
	{".zip", formatZip},
	{".gz", formatGzip},
	{".xml", formatXML},
}

// classify decides how an attachment carries a report. A gzipped TLS report is
// often labelled with the generic gzip type, so a ".json.gz" name wins over that
// type. Otherwise the media type decides, and the file name is the fallback.
func classify(p *mime.Part) format {
	byName := classifyName(strings.ToLower(p.Filename()))
	if byName == formatTLSGzip || byName == formatTLSJSON {
		return byName
	}
	if f, ok := typeFormats[p.Type+"/"+p.Subtype]; ok {
		return f
	}
	return byName
}

// classifyName maps a lowercased file name to its format.
func classifyName(name string) format {
	for _, nf := range nameFormats {
		if strings.HasSuffix(name, nf.suffix) {
			return nf.f
		}
	}
	return formatNone
}

// gunzip decompresses a gzip document, refusing one that expands past
// maxReportBytes.
func gunzip(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return readBounded(zr)
}

// unzipXML returns the first XML entry of a zip archive. It examines at most
// maxZipEntries entries, so an archive of many empty entries costs nothing.
func unzipXML(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for i, f := range zr.File {
		if i >= maxZipEntries {
			break
		}
		if strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
			return readZipEntry(f)
		}
	}
	return nil, ErrNotReport
}

// readZipEntry reads one archive entry, refusing one that expands past
// maxReportBytes. The declared size in the archive is not trusted.
func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readBounded(rc)
}

// readBounded reads r to the end, or fails with ErrTooLarge once it passes
// maxReportBytes. It reads one byte past the limit, so a document of exactly the
// limit is accepted and one byte more is not.
func readBounded(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxReportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxReportBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

// charsetReader converts an XML document that declares a charset other than
// UTF-8, which encoding/xml refuses on its own.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	enc, err := htmlindex.Get(label)
	if err != nil {
		return nil, err
	}
	return enc.NewDecoder().Reader(input), nil
}
