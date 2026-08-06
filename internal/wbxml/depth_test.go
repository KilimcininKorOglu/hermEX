package wbxml

import (
	"errors"
	"testing"
)

// nestedDocument builds a WBXML document nesting one element inside another
// depth times: the fixed header, then depth open tags carrying content, then the
// matching END bytes. A nested level costs two bytes, which is why the transport's
// body cap alone bounds nothing useful.
func nestedDocument(depth int) []byte {
	doc := []byte{version, byte(publicID), byte(charsetUTF8), byte(stringTableLen)}
	// Any token with the content bit set opens an element that may hold children.
	const openTag = byte(0x05) | cbContent
	for range depth {
		doc = append(doc, openTag)
	}
	for range depth {
		doc = append(doc, gEnd)
	}
	return doc
}

// TestUnmarshalRejectsDeepNesting proves a document nested past the bound is
// refused. The decoder is recursive descent, so without the bound an
// authenticated device could exhaust the goroutine stack with one request, and a
// Go stack overflow is a process-wide fatal error: it takes down every other
// session the daemon is serving, not just this one.
func TestUnmarshalRejectsDeepNesting(t *testing.T) {
	_, err := Unmarshal(nestedDocument(maxNestingDepth + 2))
	if err == nil {
		t.Fatal("a document nested past the bound was accepted")
	}
	if !errors.Is(err, ErrTooDeep) {
		t.Errorf("error = %v, want ErrTooDeep", err)
	}
}

// TestUnmarshalRejectsPathologicalNesting is the shape an attacker actually
// sends: nesting far beyond anything a client needs, cheap to produce because
// each level is two bytes. It must be refused rather than recursed into.
func TestUnmarshalRejectsPathologicalNesting(t *testing.T) {
	if _, err := Unmarshal(nestedDocument(200000)); !errors.Is(err, ErrTooDeep) {
		t.Errorf("error = %v, want ErrTooDeep", err)
	}
}

// TestUnmarshalAcceptsRealisticNesting is the control: the bound must be well
// clear of what ActiveSync clients legitimately send, which is around a dozen
// levels at the deepest.
func TestUnmarshalAcceptsRealisticNesting(t *testing.T) {
	root, err := Unmarshal(nestedDocument(16))
	if err != nil {
		t.Fatalf("a realistically nested document was rejected: %v", err)
	}
	// Walk down to confirm the whole chain decoded, not just the first level.
	n, levels := root, 1
	for len(n.Children) > 0 {
		n = n.Children[0]
		levels++
	}
	if levels != 16 {
		t.Errorf("decoded %d levels, want 16", levels)
	}
}
