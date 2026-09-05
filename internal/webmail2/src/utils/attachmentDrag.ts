// Dragging an attachment out of a message and into a composer.
//
// A drag started inside the browser cannot carry a File: `dataTransfer.files` is
// populated only by a drag from the operating system. So the attachment travels
// as a self-contained payload under a private type, and the drop side rebuilds a
// File from it. That is also what makes the drag work between two tabs, where
// the two pages share no state at all.
//
// The payload arrives over a drag and is NOT trusted. It can be written by any
// page the user drags from, so the filename is reduced to its basename, the
// media type has to look like one, and the decoded size is bounded.

// DRAG_TYPE is the private clipboard type the payload rides under. A drop from
// anywhere else simply does not carry it.
export const DRAG_TYPE = "application/x-hermex-attachment"

// MAX_DRAG_BYTES bounds what one dragged attachment may decode to. A drag holds
// the whole file in memory twice while it is decoded, and the composer would
// then post it, so an unbounded payload is a cost the reader never chose.
export const MAX_DRAG_BYTES = 25 * 1024 * 1024

export interface DragPayload {
  name: string
  type: string
  content: string // base64
}

// setAttachmentDrag puts one attachment on the drag.
export function setAttachmentDrag(data: DataTransfer, payload: DragPayload): void {
  data.setData(DRAG_TYPE, JSON.stringify(payload))
  // A plain-text flavour so a drop onto something that only understands text
  // still says what was dragged rather than arriving empty.
  data.setData("text/plain", payload.name)
  data.effectAllowed = "copy"
}

// fileFromDrag rebuilds the dragged attachment as a File, or returns null when
// the drag carries no payload or one that cannot be trusted.
export function fileFromDrag(data: DataTransfer): File | null {
  const raw = data.getData(DRAG_TYPE)
  if (!raw) return null
  let payload: DragPayload
  try {
    payload = JSON.parse(raw) as DragPayload
  } catch {
    return null
  }
  if (!payload || typeof payload.content !== "string") return null

  const bytes = decodeBase64(payload.content)
  if (!bytes || bytes.length === 0 || bytes.length > MAX_DRAG_BYTES) return null
  return new File([bytes], safeName(payload.name), { type: safeType(payload.type) })
}

// safeName reduces a name to its basename. A path in it would otherwise reach
// the composer, and from there the send request, as a filename with directories.
function safeName(name: unknown): string {
  const s = typeof name === "string" ? name : ""
  const base = s.split(/[\\/]/).pop() ?? ""
  // Control characters are dropped by codepoint rather than by a pattern: a
  // regular expression carrying them is itself a lint finding, and this says the
  // same thing plainly.
  const cleaned = Array.from(base)
    .filter((c) => {
      const cp = c.codePointAt(0) ?? 0
      return cp >= 0x20 && cp !== 0x7f
    })
    .join("")
    .trim()
  return cleaned === "" || cleaned === "." || cleaned === ".." ? "attachment" : cleaned
}

// safeType keeps a media type that looks like one and falls back to the generic
// binary type, so a crafted value cannot travel on as a Content-Type.
function safeType(type: unknown): string {
  const s = typeof type === "string" ? type : ""
  return /^[a-z0-9!#$&^_.+-]+\/[a-z0-9!#$&^_.+-]+$/i.test(s) ? s : "application/octet-stream"
}

function decodeBase64(b64: string): Uint8Array<ArrayBuffer> | null {
  try {
    const binary = atob(b64)
    const out = new Uint8Array(new ArrayBuffer(binary.length))
    for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i)
    return out
  } catch {
    return null
  }
}

// blobToBase64 encodes a fetched attachment for the drag payload. It goes
// through FileReader rather than a spread over the bytes, because a multi-
// megabyte attachment overflows the argument list.
export function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const url = String(reader.result ?? "")
      resolve(url.slice(url.indexOf(",") + 1))
    }
    reader.onerror = () => reject(reader.error ?? new Error("could not read the attachment"))
    reader.readAsDataURL(blob)
  })
}
