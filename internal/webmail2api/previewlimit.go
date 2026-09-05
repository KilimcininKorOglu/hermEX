package webmail2api

import "sync/atomic"

// defaultMaxPreviewBytes is the largest attachment the SPA previews inline on its
// own when a message opens; a larger one waits for a click. A PDF preview renders
// through an <object>, which the browser does not defer the way it defers a lazy
// <img>, so an unbounded preview downloads every PDF in the message before the
// reader has asked for any of them.
const defaultMaxPreviewBytes = 2 * 1024 * 1024

// previewLimit holds the operator-set inline-preview cap (bytes; 0 = use the
// default), set by SetMaxPreviewBytes and read live when the appearance settings
// are served, so the webmail daemon's poll can apply an edit without a restart.
var previewLimit atomic.Int64

// SetMaxPreviewBytes sets the inline-preview cap in bytes (0 restores the built-in
// default). It is safe to call concurrently with request handling.
func SetMaxPreviewBytes(n int64) {
	if n < 0 {
		n = 0
	}
	previewLimit.Store(n)
}

// maxPreviewBytes is the cap in force right now.
func maxPreviewBytes() int64 {
	if n := previewLimit.Load(); n > 0 {
		return n
	}
	return defaultMaxPreviewBytes
}
