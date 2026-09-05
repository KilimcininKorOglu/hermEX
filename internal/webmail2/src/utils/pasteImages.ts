// Recovering the pictures in a paste.
//
// A clipboard carries the same content in several flavours. The HTML flavour is
// the one an editor wants, but its pictures are not always reachable: Word
// writes `file:///C:/...` references that no browser will load, and the
// sanitizer drops that scheme, leaving an <img> with no src at all. The picture
// bytes are elsewhere on the clipboard, as files or inside the RTF flavour.
//
// So the HTML decides the LAYOUT and the other flavours supply the BYTES.

// USABLE_SRC lists the schemes an <img> can actually load in the composer.
// Anything else, and an <img> with no src at all, is a hole to fill.
const USABLE_SRC = /^(?:https?:|cid:|data:)/i

// imageFilesFrom returns the image files a paste or drop carries. A screenshot
// arrives this way, and so does an image copied out of another page.
export function imageFilesFrom(data: DataTransfer): File[] {
  const out: File[] = []
  for (const file of Array.from(data.files ?? [])) {
    if (file.type.startsWith("image/")) out.push(file)
  }
  if (out.length > 0) return out
  // Safari reports a pasted screenshot through items rather than files.
  for (const item of Array.from(data.items ?? [])) {
    if (item.kind !== "file" || !item.type.startsWith("image/")) continue
    const file = item.getAsFile()
    if (file) out.push(file)
  }
  return out
}

// readAsDataURL encodes one file as a data: URI. The send path converts those
// into inline attachments, so nothing leaves the browser as a data: URI.
export function readAsDataURL(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ""))
    reader.onerror = () => reject(reader.error ?? new Error("could not read the pasted image"))
    reader.readAsDataURL(file)
  })
}

// countMissingImages reports how many <img> elements in a fragment have no
// loadable source. That is how many pictures another clipboard flavour has to
// supply for the paste to arrive whole.
export function countMissingImages(html: string): number {
  return withFragment(html, (doc) => imagesNeedingSource(doc).length)
}

// fillMissingImages assigns the given sources, in order, to the <img> elements
// that have no loadable one. Extra sources are ignored: they belong to pictures
// the pasted markup did not place, and appending them would put pictures where
// the author never had any.
export function fillMissingImages(html: string, sources: string[]): string {
  if (sources.length === 0) return html
  return withFragment(html, (doc) => {
    const holes = imagesNeedingSource(doc)
    for (let i = 0; i < holes.length && i < sources.length; i++) {
      holes[i].setAttribute("src", sources[i])
    }
    return doc.body.innerHTML
  })
}

// imagesFragment renders bare pictures, for a paste that carries no markup at
// all (a screenshot).
export function imagesFragment(sources: string[]): string {
  return sources.map((src) => `<img src="${escapeAttr(src)}">`).join("")
}

function imagesNeedingSource(doc: Document): HTMLImageElement[] {
  return Array.from(doc.querySelectorAll("img")).filter((img) => {
    const src = img.getAttribute("src") ?? ""
    return !USABLE_SRC.test(src.trim())
  })
}

// withFragment parses the markup once, so a caller reads and writes the same
// tree instead of matching patterns against the markup text, where a pattern
// also matches inside an attribute.
function withFragment<T>(html: string, fn: (doc: Document) => T): T {
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, "text/html")
  return fn(doc)
}

function escapeAttr(v: string): string {
  return v.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
}
