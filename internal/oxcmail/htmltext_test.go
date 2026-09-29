package oxcmail

import "testing"

// TestHTMLText proves the text of an HTML body reads as a browser shows it:
// hidden elements drop out, block elements break lines, source whitespace
// collapses, entities decode and a pre element keeps its layout.
func TestHTMLText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"inline", "<b>bold</b> and <i>it</i>alic", "bold and italic"},
		{"source whitespace", "one\n   two\t\tthree", "one two three"},
		{"line break", "first<br>second<br/>third", "first\nsecond\nthird"},
		{"paragraphs", "<p>one</p><p>two</p>", "one\n\ntwo"},
		{"divs", "<div>a</div><div>b</div>", "a\nb"},
		{"hidden", "<script>var x=1</script><style>p{}</style>shown", "shown"},
		{"entities", "a&nbsp;&amp;&lt;b&gt;", "a &<b>"},
		{"table cells", "<table><tr><td>a</td><td>b</td></tr><tr><td>c</td></tr></table>", "a b\nc"},
		{"pre", "<pre>x  y\n  z</pre>", "x  y\n  z"},
	}
	for _, c := range cases {
		if got := HTMLText(c.in); got != c.want {
			t.Errorf("%s: HTMLText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
