import { describe, it, expect } from 'vitest'
import { DRAG_TYPE, MAX_DRAG_BYTES, fileFromDrag, setAttachmentDrag } from './attachmentDrag'

// fakeTransfer stands in for a DataTransfer, which jsdom does not construct.
function fakeTransfer(entries: Record<string, string> = {}): DataTransfer {
  const store: Record<string, string> = { ...entries }
  return {
    getData: (t: string) => store[t] ?? '',
    setData: (t: string, v: string) => { store[t] = v },
    types: Object.keys(store),
  } as unknown as DataTransfer
}

const helloB64 = btoa('hello')

describe('setAttachmentDrag', () => {
  it('carries the payload and a plain-text name', () => {
    const data = fakeTransfer()
    setAttachmentDrag(data, { name: 'report.pdf', type: 'application/pdf', content: helloB64 })
    expect(JSON.parse(data.getData(DRAG_TYPE)).name).toBe('report.pdf')
    expect(data.getData('text/plain')).toBe('report.pdf')
  })
})

describe('fileFromDrag', () => {
  it('rebuilds the dragged attachment', async () => {
    const data = fakeTransfer()
    setAttachmentDrag(data, { name: 'report.pdf', type: 'application/pdf', content: helloB64 })
    const file = fileFromDrag(data)
    expect(file).not.toBeNull()
    expect(file!.name).toBe('report.pdf')
    expect(file!.type).toBe('application/pdf')
    // jsdom's File has no text(); read the bytes the way a browser upload would.
    const buf = await new Promise<string>((resolve) => {
      const reader = new FileReader()
      reader.onload = () => resolve(String(reader.result))
      reader.readAsText(file!)
    })
    expect(buf).toBe('hello')
  })

  it('ignores a drag that carries no payload', () => {
    expect(fileFromDrag(fakeTransfer())).toBeNull()
    expect(fileFromDrag(fakeTransfer({ 'text/plain': 'just text' }))).toBeNull()
  })

  it('ignores a payload that is not readable', () => {
    expect(fileFromDrag(fakeTransfer({ [DRAG_TYPE]: 'not json' }))).toBeNull()
    expect(fileFromDrag(fakeTransfer({ [DRAG_TYPE]: JSON.stringify({ name: 'a' }) }))).toBeNull()
    expect(fileFromDrag(fakeTransfer({ [DRAG_TYPE]: JSON.stringify({ name: 'a', type: 'text/plain', content: '' }) }))).toBeNull()
  })

  // The payload is written by whatever page the user dragged from, so a path in
  // the name must not reach the composer and travel on into the send request.
  it('reduces the name to its basename', () => {
    for (const [name, want] of [
      ['../../etc/passwd', 'passwd'],
      ['C:\\Windows\\System32\\evil.dll', 'evil.dll'],
      ['/absolute/path/report.pdf', 'report.pdf'],
      ['..', 'attachment'],
      ['', 'attachment'],
    ] as const) {
      const data = fakeTransfer({ [DRAG_TYPE]: JSON.stringify({ name, type: 'text/plain', content: helloB64 }) })
      expect(fileFromDrag(data)!.name).toBe(want)
    }
  })

  it('strips control characters from the name', () => {
    const data = fakeTransfer({
      [DRAG_TYPE]: JSON.stringify({ name: 'a\r\nContent-Type: evil', type: 'text/plain', content: helloB64 }),
    })
    const name = fileFromDrag(data)!.name
    expect(name).not.toContain('\r')
    expect(name).not.toContain('\n')
  })

  // A crafted media type would otherwise travel on as a Content-Type.
  it('refuses a media type that is not one', () => {
    for (const type of ['not a type', 'text/plain\r\nX: y', '', 'text']) {
      const data = fakeTransfer({ [DRAG_TYPE]: JSON.stringify({ name: 'a.bin', type, content: helloB64 }) })
      expect(fileFromDrag(data)!.type).toBe('application/octet-stream')
    }
  })

  it('refuses a payload larger than the cap', () => {
    const big = btoa('x'.repeat(MAX_DRAG_BYTES + 1))
    const data = fakeTransfer({ [DRAG_TYPE]: JSON.stringify({ name: 'big.bin', type: 'application/octet-stream', content: big }) })
    expect(fileFromDrag(data)).toBeNull()
  })
})
