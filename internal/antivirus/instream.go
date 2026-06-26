// Package antivirus scans raw message bytes for malware through a ClamAV clamd
// daemon over its INSTREAM socket protocol. It is a fail-open sibling to
// internal/antispam: Scan returns an error when it cannot reach a verdict, and
// the caller (the MTA) decides the policy: temp-fail unauthenticated inbound,
// fail-open on authenticated submission.
package antivirus

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
)

// chunkSize is the INSTREAM chunk payload size. clamd reassembles the stream, so
// this only trades syscalls for memory; 64 KiB matches common clients.
const chunkSize = 64 * 1024

// writeStream sends a z-framed INSTREAM command, the payload as 4-byte
// big-endian length-prefixed chunks, then the zero-length terminator.
func writeStream(conn net.Conn, raw []byte) error {
	if _, err := io.WriteString(conn, "zINSTREAM\x00"); err != nil {
		return err
	}
	var hdr [4]byte
	for off := 0; off < len(raw); {
		end := min(off+chunkSize, len(raw))
		binary.BigEndian.PutUint32(hdr[:], uint32(end-off))
		if _, err := conn.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := conn.Write(raw[off:end]); err != nil {
			return err
		}
		off = end
	}
	binary.BigEndian.PutUint32(hdr[:], 0) // zero-length chunk ends the stream
	_, err := conn.Write(hdr[:])
	return err
}

// parseReply maps a clamd INSTREAM reply record to a Result. z-framing makes the
// record NUL-terminated; clamd answers "stream: OK", "stream: <name> FOUND", or
// "stream: <msg> ERROR". A FOUND or OK reply is a completed scan (nil error); an
// ERROR or unrecognized reply is a scan failure.
func parseReply(b []byte) (Result, error) {
	line := strings.TrimRight(string(b), "\x00\n\r ")
	switch {
	case strings.HasSuffix(line, " FOUND"):
		name := strings.TrimSuffix(line, " FOUND")
		name = strings.TrimPrefix(name, "stream: ")
		return Result{VirusName: strings.TrimSpace(name)}, nil
	case strings.HasSuffix(line, " ERROR"):
		return Result{}, fmt.Errorf("antivirus: clamd: %s", line)
	case strings.HasSuffix(line, " OK"):
		return Result{Clean: true}, nil
	default:
		return Result{}, fmt.Errorf("antivirus: unexpected clamd reply %q", line)
	}
}
