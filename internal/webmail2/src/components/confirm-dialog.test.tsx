import { describe, it, expect, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ConfirmProvider, useConfirm, type ConfirmOptions } from './confirm-dialog'
import { I18nProvider } from '@/hooks/useI18n'

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type Ask = (o: ConfirmOptions) => Promise<boolean>

// mountConfirm renders the provider and hands back the confirm function a
// component inside it receives.
function mountConfirm(): { ask: Ask; root: Root; container: HTMLElement } {
  let ask: Ask | null = null
  function Grab() {
    ask = useConfirm()
    return null
  }
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => root.render(<I18nProvider><ConfirmProvider><Grab /></ConfirmProvider></I18nProvider>))
  return { ask: ask as unknown as Ask, root, container }
}

// button finds a dialog button by its visible label.
function button(label: string): HTMLButtonElement | undefined {
  return Array.from(document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')).find((b) => b.textContent === label)
}

const question: ConfirmOptions = { title: 'Recall', message: 'Recall this message?' }

describe('useConfirm', () => {
  let mounted: ReturnType<typeof mountConfirm> | null = null
  afterEach(() => {
    if (!mounted) return
    act(() => mounted?.root.unmount())
    mounted.container.remove()
    mounted = null
  })

  it('asks in an app dialog and answers yes when confirmed', async () => {
    mounted = mountConfirm()
    let answer: Promise<boolean> = Promise.resolve(false)
    act(() => { answer = mounted!.ask(question) })
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Recall this message?')
    act(() => { button('Recall')?.click() })
    await expect(answer).resolves.toBe(true)
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })

  it('answers no when cancelled', async () => {
    mounted = mountConfirm()
    let answer: Promise<boolean> = Promise.resolve(true)
    act(() => { answer = mounted!.ask(question) })
    const cancel = button('Cancel') ?? button('common.cancel')
    act(() => { cancel?.click() })
    await expect(answer).resolves.toBe(false)
  })

  it('answers no when the dialog is dismissed with Escape', async () => {
    mounted = mountConfirm()
    let answer: Promise<boolean> = Promise.resolve(true)
    act(() => { answer = mounted!.ask(question) })
    const dialog = document.querySelector('[role="dialog"]') as HTMLElement
    act(() => { dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })) })
    await expect(answer).resolves.toBe(false)
  })

  it('answers an earlier question no when a new one replaces it', async () => {
    mounted = mountConfirm()
    let first: Promise<boolean> = Promise.resolve(true)
    act(() => { first = mounted!.ask(question) })
    act(() => { void mounted!.ask({ title: 'Discard', message: 'Discard this email?' }) })
    await expect(first).resolves.toBe(false)
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Discard this email?')
  })
})
