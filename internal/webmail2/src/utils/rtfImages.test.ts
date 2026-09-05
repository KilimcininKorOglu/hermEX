import { describe, it, expect } from 'vitest'
import { extractRtfImages } from './rtfImages'

// A real PNG signature, so the test asserts on bytes a decoder would accept.
const PNG_BYTES = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]
const PNG_HEX = PNG_BYTES.map((b) => b.toString(16).padStart(2, '0')).join('')
const JPEG_HEX = 'ffd8ffe000104a464946'

function decode(uri: string): { type: string; bytes: number[] } {
  const [meta, payload] = uri.split(',')
  const bin = atob(payload)
  return { type: meta.replace('data:', '').replace(';base64', ''), bytes: Array.from(bin, (c) => c.charCodeAt(0)) }
}

// wordPicture is the shape Word puts on the clipboard: the usable PNG inside a
// \*\shppict, and the same picture again as an unrenderable metafile inside a
// \nonshppict. Taking both would double every picture.
const wordPicture = `{\\rtf1\\ansi
{\\*\\shppict{\\pict{\\*\\blipuid deadbeefdeadbeefdeadbeefdeadbeef}\\pngblip\\picw100\\pich50\\picwgoal1500\\pichgoal750
${PNG_HEX}
}}{\\nonshppict{\\pict\\wmetafile8\\picw100\\pich50
aabbccdd
}}
\\par}`

describe('extractRtfImages', () => {
  it('takes the picture Word hides in the RTF flavour', () => {
    const out = extractRtfImages(wordPicture)
    expect(out).toHaveLength(1)
    const { type, bytes } = decode(out[0])
    expect(type).toBe('image/png')
    expect(bytes).toEqual(PNG_BYTES)
  })

  // \*\blipuid carries hex of its own. Folding it into the payload corrupts
  // every picture that has one, and Word writes one on every picture.
  it('does not fold the blip uid into the picture', () => {
    const { bytes } = decode(extractRtfImages(wordPicture)[0])
    expect(bytes.length).toBe(PNG_BYTES.length)
  })

  // The \nonshppict copy is not always an unrenderable metafile: some producers
  // put a second PNG there. Then only skipping the group keeps the picture from
  // being taken twice, and a doubled picture is what the reader would see.
  it('skips a renderable duplicate inside nonshppict', () => {
    const rtf = `{\\rtf1{\\*\\shppict{\\pict\\pngblip\\picw1\\pich1 ${PNG_HEX}}}{\\nonshppict{\\pict\\pngblip\\picw1\\pich1 ${PNG_HEX}}}}`
    expect(extractRtfImages(rtf)).toHaveLength(1)
  })

  it('reads a jpeg too', () => {
    const rtf = `{\\rtf1{\\pict\\jpegblip\\picw10\\pich10 ${JPEG_HEX}}}`
    const { type } = decode(extractRtfImages(rtf)[0])
    expect(type).toBe('image/jpeg')
  })

  it('keeps the pictures in the order they appear', () => {
    const rtf = `{\\rtf1{\\pict\\pngblip\\picw1\\pich1 ${PNG_HEX}}{\\pict\\jpegblip\\picw1\\pich1 ${JPEG_HEX}}}`
    const out = extractRtfImages(rtf).map((u) => decode(u).type)
    expect(out).toEqual(['image/png', 'image/jpeg'])
  })

  // A browser cannot render a metafile or a device-dependent bitmap, so an <img>
  // pointing at one shows the same gap the paste already had.
  it('skips a format no browser renders', () => {
    for (const word of ['wmetafile8', 'emfblip', 'dibitmap', 'wbitmap']) {
      expect(extractRtfImages(`{\\rtf1{\\pict\\${word}\\picw1\\pich1 aabbccdd}}`)).toEqual([])
    }
  })

  // \bin holds raw bytes in the middle of the text, which does not survive being
  // carried as a clipboard string.
  it('skips a binary payload rather than decoding mangled bytes', () => {
    expect(extractRtfImages(`{\\rtf1{\\pict\\pngblip\\picw1\\pich1\\bin8 ????????}}`)).toEqual([])
  })

  it('returns nothing for rtf without pictures, and for junk', () => {
    expect(extractRtfImages(`{\\rtf1\\ansi hello\\par}`)).toEqual([])
    expect(extractRtfImages('')).toEqual([])
    expect(extractRtfImages('not rtf at all')).toEqual([])
  })

  // An unterminated group must end the scan rather than run past the end of the
  // string or loop.
  it('survives a truncated group', () => {
    const rtf = `{\\rtf1{\\pict\\pngblip\\picw1\\pich1 ${PNG_HEX}`
    expect(() => extractRtfImages(rtf)).not.toThrow()
  })
})
