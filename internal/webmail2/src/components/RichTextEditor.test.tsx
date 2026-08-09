import { describe, it, expect, vi, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { RichTextEditor } from './RichTextEditor'

// React only allows act() when the environment declares itself a test one.
;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// mount renders node into a detached container and returns both.
function mount(node: React.ReactElement): { container: HTMLElement; root: Root } {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => root.render(node))
  return { container, root }
}

// editorHTML returns the contentEditable's markup.
function editorHTML(container: HTMLElement): string {
  return container.querySelector('[contenteditable]')?.innerHTML ?? ''
}

describe('RichTextEditor', () => {
  afterEach(() => vi.restoreAllMocks())

  // The editor writes its value straight into a live contentEditable, through
  // dangerouslySetInnerHTML and through an innerHTML assignment. It has several
  // callers (the composer, the signature editor, the template editor) and the
  // value always comes from the server, so the sanitization belongs here: a sink
  // that relies on each caller remembering grows the hole back every time someone
  // adds a caller.
  it('strips an event handler out of the value it is handed', () => {
    const { container, root } = mount(
      <RichTextEditor value={'<img src=x onerror="alert(1)">'} onChange={() => {}} />,
    )
    const html = editorHTML(container)
    expect(html).not.toContain('onerror')
    expect(html).not.toContain('alert(1)')
    act(() => root.unmount())
    container.remove()
  })

  it('strips a script element out of the value', () => {
    const { container, root } = mount(
      <RichTextEditor value={'<p>hi</p><script>alert(1)</script>'} onChange={() => {}} />,
    )
    expect(editorHTML(container)).not.toContain('<script')
    act(() => root.unmount())
    container.remove()
  })

  it('drops a javascript: link while keeping a real one', () => {
    const { container, root } = mount(
      <RichTextEditor
        value={'<a href="javascript:alert(1)">x</a><a href="https://example.com">y</a>'}
        onChange={() => {}}
      />,
    )
    const html = editorHTML(container)
    expect(html.toLowerCase()).not.toContain('javascript:')
    expect(html).toContain('https://example.com')
    act(() => root.unmount())
    container.remove()
  })

  it('sanitizes a value that arrives after the first render, not just the initial one', () => {
    const { container, root } = mount(<RichTextEditor value="<p>first</p>" onChange={() => {}} />)
    act(() => {
      root.render(<RichTextEditor value={'<img src=x onerror="alert(1)">'} onChange={() => {}} />)
    })
    expect(editorHTML(container)).not.toContain('onerror')
    act(() => root.unmount())
    container.remove()
  })

  it('keeps the formatting a signature or quoted reply legitimately carries', () => {
    const { container, root } = mount(
      <RichTextEditor value={'<p>Regards,<br><b>Alice</b></p>'} onChange={() => {}} />,
    )
    const html = editorHTML(container)
    expect(html).toContain('<b>Alice</b>')
    expect(html).toContain('<br>')
    act(() => root.unmount())
    container.remove()
  })
})
