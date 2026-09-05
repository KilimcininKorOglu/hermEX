// Pulling the pictures out of the clipboard's RTF flavour.
//
// Word writes its HTML flavour with `file:///` picture references that no
// browser will load; the picture bytes exist only in the RTF flavour, as hex
// inside `\pict` groups. So a Word paste arrives as text with gaps unless the
// pictures are read out of here.
//
// Only the formats a browser can render are taken. Word emits each picture
// TWICE: once as a modern PNG or JPEG inside `{\*\shppict …}`, and once as a
// legacy Windows metafile inside `{\nonshppict …}`. Taking both would double
// every picture, and the metafile is unrenderable anyway, so the `\nonshppict`
// group is skipped whole.

// RENDERABLE maps the RTF picture-format control word to its media type. A
// format not listed here (a metafile, a device-dependent bitmap) is skipped,
// because a browser cannot display it and an <img> pointing at one shows the
// same gap the paste already had.
const RENDERABLE: Record<string, string> = {
  pngblip: "image/png",
  jpegblip: "image/jpeg",
}

// extractRtfImages returns one data: URI per renderable picture, in the order
// the pictures appear, which is the order the pasted markup places them in.
export function extractRtfImages(rtf: string): string[] {
  if (!rtf || rtf.indexOf("\\pict") < 0) return []
  const out: string[] = []
  let i = 0
  while (i < rtf.length) {
    if (rtf[i] !== "{") {
      i++
      continue
    }
    if (startsWith(rtf, i, "{\\nonshppict")) {
      i = endOfGroup(rtf, i)
      continue
    }
    if (startsWith(rtf, i, "{\\pict")) {
      const end = endOfGroup(rtf, i)
      const uri = pictureToDataURI(rtf.slice(i, end))
      if (uri) out.push(uri)
      i = end
      continue
    }
    i++
  }
  return out
}

function startsWith(s: string, at: number, prefix: string): boolean {
  return s.startsWith(prefix, at)
}

// endOfGroup returns the index just past the group that opens at `start`,
// honouring the `\{` and `\}` escapes so a brace inside text does not end it.
function endOfGroup(s: string, start: number): number {
  let depth = 0
  for (let i = start; i < s.length; i++) {
    const c = s[i]
    if (c === "\\") {
      i++ // whatever follows a backslash is escaped, including a brace
      continue
    }
    if (c === "{") depth++
    else if (c === "}") {
      depth--
      if (depth === 0) return i + 1
    }
  }
  return s.length
}

// pictureToDataURI reads one `\pict` group. It returns "" for a picture this
// cannot use, which is a format no browser renders or a payload stored as
// binary rather than hex.
function pictureToDataURI(group: string): string {
  // A `\bin` payload is raw bytes in the middle of the text, which does not
  // survive being carried as a clipboard string. Skip it rather than decode
  // whatever the mangling left.
  if (/\\bin\d/.test(group)) return ""

  let mediaType = ""
  for (const word of Object.keys(RENDERABLE)) {
    if (new RegExp("\\\\" + word + "\\b").test(group)) {
      mediaType = RENDERABLE[word]
      break
    }
  }
  if (!mediaType) return ""

  const hex = hexPayload(group)
  if (hex.length < 2) return ""
  const bytes = hexToBytes(hex)
  if (bytes.length === 0) return ""
  return `data:${mediaType};base64,${bytesToBase64(bytes)}`
}

// hexPayload collects the picture's hex digits. Nested groups are dropped first:
// `{\*\blipuid …}` carries hex of its own, and folding it in would corrupt every
// picture that has one.
function hexPayload(group: string): string {
  const body = stripNestedGroups(group)
  // The data follows the LAST control word. Everything before it is the picture's
  // geometry, and a digit there is a measurement, not image data: taking the whole
  // group as hex would prepend `100`, `50` and every other dimension to the file.
  // A control word ends at its optional single delimiting space, so the payload
  // starts where the last match ends.
  let dataStart = 0
  const control = /\\[a-zA-Z]+-?\d*\s?/g
  for (let m = control.exec(body); m !== null; m = control.exec(body)) {
    dataStart = m.index + m[0].length
  }
  const tail = body.slice(dataStart)
  let hex = ""
  for (const c of tail) {
    if ((c >= "0" && c <= "9") || (c >= "a" && c <= "f") || (c >= "A" && c <= "F")) hex += c
  }
  return hex.length % 2 === 0 ? hex : hex.slice(0, -1)
}

// stripNestedGroups removes every `{…}` inside the picture group, leaving its own
// control words and hex payload.
function stripNestedGroups(group: string): string {
  const inner = group.slice(1, -1) // drop this group's own braces
  let out = ""
  let depth = 0
  for (let i = 0; i < inner.length; i++) {
    const c = inner[i]
    if (c === "\\") {
      if (depth === 0) out += c + (inner[i + 1] ?? "")
      i++
      continue
    }
    if (c === "{") depth++
    else if (c === "}") depth--
    else if (depth === 0) out += c
  }
  return out
}

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2)
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.substr(i * 2, 2), 16)
  return out
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = ""
  // Chunked, because a single spread of a multi-megabyte picture overflows the
  // argument list.
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(binary)
}
