import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { RichTextEditor } from './RichTextEditor'
import { I18nProvider } from '@/hooks/useI18n'

// React only allows act() when the environment declares itself a test one.
;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// mount renders node into a detached container and returns both. The editor's
// link dialog reads its labels through i18n, so the provider wraps every mount.
function mount(node: React.ReactElement): { container: HTMLElement; root: Root } {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => root.render(<I18nProvider>{node}</I18nProvider>))
  return { container, root }
}

// editorHTML returns the contentEditable's markup.
function editorHTML(container: HTMLElement): string {
  return container.querySelector('[contenteditable]')?.innerHTML ?? ''
}

describe('RichTextEditor', () => {
  afterEach(() => vi.restoreAllMocks())

  // The editor writes its value straight into a live contentEditable through an
  // innerHTML assignment. It has several
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
      root.render(<I18nProvider><RichTextEditor value={'<img src=x onerror="alert(1)">'} onChange={() => {}} /></I18nProvider>)
    })
    expect(editorHTML(container)).not.toContain('onerror')
    act(() => root.unmount())
    container.remove()
  })

  // A caller feeds every edit back as the value (the composer does). Writing that
  // value back into the DOM replaces the node the caret sits in and puts the caret
  // at the start, so each typed character landed before the previous one.
  it('leaves the typed content in place when the value it reported comes back', () => {
    function Controlled() {
      const [value, setValue] = useState('')
      return <RichTextEditor value={value} onChange={setValue} />
    }
    const { container, root } = mount(<Controlled />)
    const editor = container.querySelector('[contenteditable]') as HTMLElement
    const typed = document.createTextNode('a')
    editor.appendChild(typed)
    act(() => editor.dispatchEvent(new Event('input', { bubbles: true })))
    expect(editor.firstChild).toBe(typed)
    act(() => root.unmount())
    container.remove()
  })

  describe('link dialog', () => {
    let exec: ReturnType<typeof vi.fn>
    beforeEach(() => {
      exec = vi.fn(() => true)
      document.execCommand = exec as unknown as typeof document.execCommand
    })

    // typeInto sets an input's value the way a keystroke does, so React's onChange runs.
    const typeInto = (input: HTMLInputElement, value: string) => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    }

    // openWithSelection mounts the editor, selects its text and opens the dialog
    // from the toolbar, returning the dialog's URL field.
    const openWithSelection = () => {
      const mounted = mount(<RichTextEditor value="<p>hello</p>" onChange={() => {}} />)
      const text = mounted.container.querySelector('[contenteditable] p')?.firstChild as Text
      const range = document.createRange()
      range.selectNodeContents(text)
      window.getSelection()?.removeAllRanges()
      window.getSelection()?.addRange(range)
      const button = mounted.container.querySelector('button[title="Insert link"]') as HTMLButtonElement
      act(() => { button.dispatchEvent(new MouseEvent('mousedown', { bubbles: true })) })
      const input = document.getElementById('link-dialog-url') as HTMLInputElement
      return { ...mounted, input }
    }

    const submit = (input: HTMLInputElement) => {
      act(() => { input.form?.requestSubmit() })
    }

    const close = ({ container, root }: { container: HTMLElement; root: Root }) => {
      act(() => root.unmount())
      container.remove()
    }

    it('links the selected text to a safe address', () => {
      const mounted = openWithSelection()
      expect(mounted.input).not.toBeNull()
      act(() => typeInto(mounted.input, ' https://example.com '))
      submit(mounted.input)
      expect(exec).toHaveBeenCalledWith('createLink', false, 'https://example.com')
      close(mounted)
    })

    it('refuses a javascript: address and says why, without touching the body', () => {
      const mounted = openWithSelection()
      act(() => typeInto(mounted.input, 'javascript:alert(1)'))
      submit(mounted.input)
      expect(exec).not.toHaveBeenCalled()
      expect(document.querySelector('[role="alert"]')).not.toBeNull()
      close(mounted)
    })

    it('changes nothing when cancelled', () => {
      const mounted = openWithSelection()
      act(() => typeInto(mounted.input, 'https://example.com'))
      const cancel = Array.from(document.querySelectorAll('button')).find((b) => b.type === 'button' && ['Cancel', 'common.cancel'].includes(b.textContent ?? ''))
      act(() => { cancel?.click() })
      expect(exec).not.toHaveBeenCalled()
      expect(document.getElementById('link-dialog-url')).toBeNull()
      close(mounted)
    })
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
