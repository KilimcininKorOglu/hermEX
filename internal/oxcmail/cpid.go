package oxcmail

import "strings"

// PR_INTERNET_CPID records the code page of the HTML body, which is stored as
// raw bytes in its original charset rather than converted to UTF-8. These maps
// bridge MIME charset names and the well-known Windows code page numbers for the
// charsets the core path handles.

// cpUTF8 is the code page Export labels a body with when the stored code page is
// unrecognized. The reference uses an "ANSI code page" sentinel there, but a
// server has no single ANSI code page, so UTF-8 is the sane default.
const cpUTF8 = 65001

// cpidToName maps a code page identifier to the canonical charset name Export
// emits.
var cpidToName = map[int32]string{
	65001: "utf-8",
	20127: "us-ascii",
	28591: "iso-8859-1",
	28592: "iso-8859-2",
	28599: "iso-8859-9",
	1250:  "windows-1250",
	1251:  "windows-1251",
	1252:  "windows-1252",
	1254:  "windows-1254",
	932:   "shift_jis",
	936:   "gbk",
	949:   "euc-kr",
	950:   "big5",
}

// nameToCPID resolves a charset name (with common aliases) to its code page.
var nameToCPID = map[string]int32{
	"utf-8": 65001, "utf8": 65001,
	"us-ascii": 20127, "ascii": 20127,
	"iso-8859-1": 28591, "iso8859-1": 28591, "latin1": 28591,
	"iso-8859-2": 28592,
	"iso-8859-9": 28599, "latin5": 28599,
	"windows-1250": 1250, "cp1250": 1250,
	"windows-1251": 1251, "cp1251": 1251,
	"windows-1252": 1252, "cp1252": 1252,
	"windows-1254": 1254, "cp1254": 1254,
	"shift_jis": 932, "shift-jis": 932, "sjis": 932,
	"gbk": 936, "gb2312": 936,
	"euc-kr": 949, "cp949": 949,
	"big5": 950,
}

// csetToCPID maps a charset name to its code page identifier, defaulting to
// UTF-8 for unrecognized names.
func csetToCPID(charset string) int32 {
	if id, ok := nameToCPID[strings.ToLower(strings.TrimSpace(charset))]; ok {
		return id
	}
	return cpUTF8
}

// cpidToCset maps a code page identifier to a charset name, defaulting to UTF-8.
func cpidToCset(cpid int32) string {
	if name, ok := cpidToName[cpid]; ok {
		return name
	}
	return "utf-8"
}
