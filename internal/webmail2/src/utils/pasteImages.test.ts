import { describe, it, expect } from 'vitest'
import { countMissingImages, fillMissingImages, imageFilesFrom, imagesFragment, readAsDataURL } from './pasteImages'

// wordPaste is the shape Word puts on the clipboard: the picture is a local file
// reference the browser cannot load, and the sanitizer strips that scheme, so the
// element arrives with no usable source at all.
const wordPaste = `<p>text before</p><img src="file:///C:/Users/x/AppData/clip_image001.png"><p>after</p>`
const sanitizedWordPaste = `<p>text before</p><img><p>after</p>`

// fakeTransfer stands in for a DataTransfer, which jsdom does not construct.
function fakeTransfer(files: File[], items?: DataTransferItem[]): DataTransfer {
  return { files, items: items ?? [] } as unknown as DataTransfer
}

describe('countMissingImages', () => {
  it('counts the images a paste cannot load', () => {
    expect(countMissingImages(sanitizedWordPaste)).toBe(1)
    expect(countMissingImages(wordPaste)).toBe(1)
    expect(countMissingImages(`<img src="https://example.org/a.png">`)).toBe(0)
    expect(countMissingImages(`<img src="cid:a@b">`)).toBe(0)
    expect(countMissingImages(`<img src="data:image/png;base64,AAAA">`)).toBe(0)
    expect(countMissingImages(`<p>no pictures</p>`)).toBe(0)
  })
})

describe('fillMissingImages', () => {
  // This is the defect: the text arrives and the picture does not.
  it('puts a recovered picture where the markup placed it', () => {
    const out = fillMissingImages(sanitizedWordPaste, ['data:image/png;base64,AAAA'])
    expect(out).toContain('src="data:image/png;base64,AAAA"')
    expect(out.indexOf('text before')).toBeLessThan(out.indexOf('<img'))
    expect(out.indexOf('<img')).toBeLessThan(out.indexOf('after'))
  })

  it('leaves an image that already loads alone', () => {
    const html = `<img src="https://example.org/a.png">`
    expect(fillMissingImages(html, ['data:image/png;base64,AAAA'])).toContain('https://example.org/a.png')
  })

  // Extra sources belong to pictures the markup never placed. Appending them
  // would put pictures where the author had none.
  it('ignores sources beyond the number of holes', () => {
    const out = fillMissingImages(sanitizedWordPaste, ['data:image/png;base64,AAAA', 'data:image/png;base64,BBBB'])
    expect(out).toContain('AAAA')
    expect(out).not.toContain('BBBB')
  })

  it('fills several holes in order', () => {
    const out = fillMissingImages(`<img><p>x</p><img>`, ['data:image/png;base64,AAAA', 'data:image/png;base64,BBBB'])
    expect(out.indexOf('AAAA')).toBeLessThan(out.indexOf('BBBB'))
  })

  it('returns the markup untouched when there is nothing to fill', () => {
    expect(fillMissingImages(sanitizedWordPaste, [])).toBe(sanitizedWordPaste)
  })
})

describe('imagesFragment', () => {
  it('renders a bare picture for a paste that carries no markup', () => {
    expect(imagesFragment(['data:image/png;base64,AAAA'])).toBe('<img src="data:image/png;base64,AAAA">')
  })

  // The source is built into an attribute, so a quote in it must not be able to
  // close that attribute and start another one.
  it('escapes a source that would break out of the attribute', () => {
    const out = imagesFragment(['x" onerror="alert(1)'])
    expect(out).not.toContain('onerror="')
    expect(out).toContain('&quot;')
  })
})

describe('imageFilesFrom', () => {
  it('takes the image files and skips everything else', () => {
    const png = new File([new Uint8Array([1, 2])], 'a.png', { type: 'image/png' })
    const txt = new File(['hello'], 'a.txt', { type: 'text/plain' })
    expect(imageFilesFrom(fakeTransfer([png, txt]))).toEqual([png])
  })

  // Safari reports a pasted screenshot through items rather than files.
  it('falls back to the items list', () => {
    const png = new File([new Uint8Array([1, 2])], 'a.png', { type: 'image/png' })
    const items = [
      { kind: 'string', type: 'text/html', getAsFile: () => null },
      { kind: 'file', type: 'image/png', getAsFile: () => png },
    ] as unknown as DataTransferItem[]
    expect(imageFilesFrom(fakeTransfer([], items))).toEqual([png])
  })
})

describe('readAsDataURL', () => {
  it('encodes a blob as a data: URI', async () => {
    const url = await readAsDataURL(new Blob([new Uint8Array([1, 2, 3])], { type: 'image/png' }))
    expect(url.startsWith('data:image/png;base64,')).toBe(true)
  })
})
