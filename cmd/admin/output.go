package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// reportWriter writes a command's report lines and keeps the first write error,
// so a report the terminal or a pipe refused is never taken for a delivered one.
// The command runs to its end; main then reads the error and exits non-zero.
type reportWriter struct {
	w   io.Writer
	err error
}

func (r *reportWriter) printf(format string, args ...any) {
	if _, err := fmt.Fprintf(r.w, format, args...); err != nil && r.err == nil {
		r.err = err
	}
}

func (r *reportWriter) println(args ...any) {
	if _, err := fmt.Fprintln(r.w, args...); err != nil && r.err == nil {
		r.err = err
	}
}

var (
	stdout = &reportWriter{w: os.Stdout}
	stderr = &reportWriter{w: os.Stderr}
)

func outf(format string, args ...any) { stdout.printf(format, args...) }
func outln(args ...any)               { stdout.println(args...) }
func errf(format string, args ...any) { stderr.printf(format, args...) }
func errln(args ...any)               { stderr.println(args...) }

// outputErr reports the first refused write to stdout or stderr, naming the
// stream.
func outputErr() error {
	var errs []error
	if stdout.err != nil {
		errs = append(errs, fmt.Errorf("write stdout: %w", stdout.err))
	}
	if stderr.err != nil {
		errs = append(errs, fmt.Errorf("write stderr: %w", stderr.err))
	}
	return errors.Join(errs...)
}
