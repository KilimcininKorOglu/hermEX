package oxcmail

import "strings"

// PR_INTERNET_CPID records the code page of the HTML body, which is stored as
// raw bytes in its original charset rather than converted to UTF-8. These maps
// bridge MIME charset names and the well-known Windows code page numbers.
//
// Coverage here is a correctness requirement, not a nicety. The label and the
// stored bytes must agree: every consumer (webmail rendering, EWS and MAPI
// export, re-export to outgoing MIME) decodes PR_HTML by this code page, and
// Export writes it straight into the outgoing Content-Type charset. A charset
// missing from these tables used to be relabeled UTF-8 over bytes that were
// nothing of the sort, so Greek, Hebrew, Arabic, Cyrillic and Japanese mail
// rendered and forwarded as garbage. The set below is therefore the full range
// mail actually declares, not just the Western European core.

// cpUTF8 is the code page Export labels a body with when the stored code page is
// unrecognized. The reference uses an "ANSI code page" sentinel there, but a
// server has no single ANSI code page, so UTF-8 is the sane default.
const cpUTF8 = 65001

// cpidToName maps a code page identifier to the canonical charset name Export
// emits.
var cpidToName = map[int32]string{
	65001: "utf-8",
	65000: "utf-7",
	1200:  "utf-16le",
	1201:  "utf-16be",
	20127: "us-ascii",
	28591: "iso-8859-1",
	28592: "iso-8859-2",
	28593: "iso-8859-3",
	28594: "iso-8859-4",
	28595: "iso-8859-5",
	28596: "iso-8859-6",
	28597: "iso-8859-7",
	28598: "iso-8859-8",
	28599: "iso-8859-9",
	28603: "iso-8859-13",
	28605: "iso-8859-15",
	1250:  "windows-1250",
	1251:  "windows-1251",
	1252:  "windows-1252",
	1253:  "windows-1253",
	1254:  "windows-1254",
	1255:  "windows-1255",
	1256:  "windows-1256",
	1257:  "windows-1257",
	1258:  "windows-1258",
	20866: "koi8-r",
	21866: "koi8-u",
	866:   "cp866",
	10000: "macintosh",
	932:   "shift_jis",
	936:   "gbk",
	949:   "euc-kr",
	950:   "big5",
	50220: "iso-2022-jp",
	50225: "iso-2022-kr",
	51932: "euc-jp",
	52936: "hz-gb-2312",
	54936: "gb18030",
	874:   "windows-874",
	1361:  "johab",
}

// nameToCPID resolves a charset name (with common aliases) to its code page.
var nameToCPID = map[string]int32{
	"utf-8": 65001, "utf8": 65001,
	"utf-7": 65000, "utf7": 65000,
	"utf-16": 1200, "utf-16le": 1200, "utf-16be": 1201,
	"us-ascii": 20127, "ascii": 20127, "iso-ir-6": 20127, "ansi_x3.4-1968": 20127,
	"iso-8859-1": 28591, "iso8859-1": 28591, "latin1": 28591, "l1": 28591, "iso_8859-1": 28591,
	"iso-8859-2": 28592, "iso8859-2": 28592, "latin2": 28592,
	"iso-8859-3": 28593, "latin3": 28593,
	"iso-8859-4": 28594, "latin4": 28594,
	"iso-8859-5": 28595, "cyrillic": 28595,
	"iso-8859-6": 28596, "arabic": 28596,
	"iso-8859-7": 28597, "greek": 28597, "greek8": 28597,
	"iso-8859-8": 28598, "hebrew": 28598, "iso-8859-8-i": 28598,
	"iso-8859-9": 28599, "latin5": 28599, "iso8859-9": 28599,
	"iso-8859-13": 28603, "latin7": 28603,
	"iso-8859-15": 28605, "latin9": 28605, "iso8859-15": 28605,
	"windows-1250": 1250, "cp1250": 1250,
	"windows-1251": 1251, "cp1251": 1251,
	"windows-1252": 1252, "cp1252": 1252,
	"windows-1253": 1253, "cp1253": 1253,
	"windows-1254": 1254, "cp1254": 1254,
	"windows-1255": 1255, "cp1255": 1255,
	"windows-1256": 1256, "cp1256": 1256,
	"windows-1257": 1257, "cp1257": 1257,
	"windows-1258": 1258, "cp1258": 1258,
	"koi8-r": 20866, "koi8r": 20866,
	"koi8-u": 21866, "koi8u": 21866,
	"ibm866": 866, "cp866": 866,
	"macintosh": 10000, "mac": 10000, "x-mac-roman": 10000,
	"shift_jis": 932, "shift-jis": 932, "sjis": 932, "ms_kanji": 932, "windows-31j": 932, "cp932": 932,
	"gbk": 936, "gb2312": 936, "cp936": 936, "csgb2312": 936,
	"gb18030":    54936,
	"hz-gb-2312": 52936,
	"euc-kr":     949, "cp949": 949, "ks_c_5601-1987": 949, "korean": 949,
	"big5": 950, "big5-hkscs": 950, "csbig5": 950,
	"iso-2022-jp": 50220, "csiso2022jp": 50220,
	"iso-2022-kr": 50225,
	"euc-jp":      51932, "eucjp": 51932, "x-euc-jp": 51932,
	"windows-874": 874, "tis-620": 874, "iso-8859-11": 874,
	"johab": 1361,
}

// csetToCPID maps a charset name to its code page identifier. ok is false for a
// name no table above covers, which is the caller's cue that labeling the bytes
// with the UTF-8 default would be a lie: it must make them UTF-8 first.
func csetToCPID(charset string) (int32, bool) {
	id, ok := nameToCPID[strings.ToLower(strings.TrimSpace(charset))]
	return id, ok
}

// cpidToCset maps a code page identifier to a charset name, defaulting to UTF-8.
func cpidToCset(cpid int32) string {
	if name, ok := cpidToName[cpid]; ok {
		return name
	}
	return "utf-8"
}
