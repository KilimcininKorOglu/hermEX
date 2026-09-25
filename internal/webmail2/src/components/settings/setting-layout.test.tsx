import { describe, it, expect, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { TextDraftInput } from './setting-layout'

// React only allows act() when the environment declares itself a test one.
;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// typeInto sets an input's value the way a keystroke does, so React's onChange runs.
function typeInto(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
  setter?.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function mountInput(onCommit: (v: string) => void) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => root.render(<TextDraftInput value="a@x.test" onCommit={onCommit} />))
  const input = container.querySelector('input') as HTMLInputElement
  const cleanup = () => {
    act(() => root.unmount())
    container.remove()
  }
  return { input, cleanup }
}

describe('TextDraftInput', () => {
  it('saves once when the field is left, not on each keystroke', () => {
    const onCommit = vi.fn()
    const { input, cleanup } = mountInput(onCommit)
    act(() => {
      typeInto(input, 'a@x.test, b')
      typeInto(input, 'a@x.test, b@x.test')
    })
    expect(onCommit).not.toHaveBeenCalled()
    act(() => { input.dispatchEvent(new FocusEvent('focusout', { bubbles: true })) })
    expect(onCommit).toHaveBeenCalledTimes(1)
    expect(onCommit).toHaveBeenCalledWith('a@x.test, b@x.test')
    cleanup()
  })

  it('saves on Enter and sends nothing when the value did not change', () => {
    const onCommit = vi.fn()
    const { input, cleanup } = mountInput(onCommit)
    act(() => { input.dispatchEvent(new FocusEvent('focusout', { bubbles: true })) })
    expect(onCommit).not.toHaveBeenCalled()
    act(() => typeInto(input, 'c@x.test'))
    act(() => { input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })) })
    expect(onCommit).toHaveBeenCalledWith('c@x.test')
    cleanup()
  })
})
