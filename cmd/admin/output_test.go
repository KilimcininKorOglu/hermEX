package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// refusingWriter is a stream that takes nothing, the state of a closed pipe.
type refusingWriter struct{ err error }

func (w refusingWriter) Write([]byte) (int, error) { return 0, w.err }

// useStreams points the report writers at the given streams for one test.
func useStreams(t *testing.T, out, errOut *reportWriter) {
	t.Helper()
	prevOut, prevErr := stdout, stderr
	stdout, stderr = out, errOut
	t.Cleanup(func() { stdout, stderr = prevOut, prevErr })
}

// TestRefusedReportIsNotTakenForDelivered: a command whose report the terminal or
// pipe refused must not exit 0 as if the operator had read it. The refusal is
// kept even when a later line goes through, and it names the stream; a report
// that was written leaves no error.
func TestRefusedReportIsNotTakenForDelivered(t *testing.T) {
	var out, errOut bytes.Buffer
	useStreams(t, &reportWriter{w: &out}, &reportWriter{w: &errOut})
	outf("domain %s created\n", "example.test")
	errln("this is a private key")
	if err := outputErr(); err != nil {
		t.Fatalf("outputErr after delivered writes = %v, want nil", err)
	}
	if out.String() != "domain example.test created\n" || errOut.String() != "this is a private key\n" {
		t.Errorf("stdout %q, stderr %q; want each line on its stream", out.String(), errOut.String())
	}

	pipeErr := errors.New("broken pipe")
	refused := &reportWriter{w: refusingWriter{pipeErr}}
	useStreams(t, refused, &reportWriter{w: &errOut})
	outln("password set")
	refused.w = &out // the stream recovers; the lost line stays lost
	outf("second line\n")
	err := outputErr()
	if !errors.Is(err, pipeErr) {
		t.Fatalf("outputErr = %v, want the refused write", err)
	}
	if !strings.Contains(err.Error(), "stdout") {
		t.Errorf("outputErr = %v, want the stream named", err)
	}
}
