// Package wbxml implements the WBXML (WAP Binary XML) codec used by Exchange
// ActiveSync: the fixed four-field header, multi-byte integers, the global
// tokens (SWITCH_PAGE, END, STR_I, OPAQUE), and the ActiveSync code-page tag
// tables (MS-ASWBXML). It marshals and unmarshals a small element tree. The
// string table and tag attributes are unused by ActiveSync and are rejected.
//
// The byte layer mirrors internal/ext: a bounds-checked reader and an appending
// writer, with table-free tag dispatch driven by the typed Tag constants in
// tokens.go (a Tag carries its code page in the high byte and its token in the
// low byte, so the codec never needs a name table to frame the bytes).
package wbxml
