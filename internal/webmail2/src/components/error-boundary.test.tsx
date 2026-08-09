import { describe, it, expect, vi, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ErrorBoundary } from './error-boundary'

// React only allows act() when the environment declares itself a test one.
;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// Boom throws during render, which is exactly what the boundary exists for.
function Boom(): React.ReactElement {
  throw new Error('component exploded')
}

// mount renders node into a detached container and returns both, so each test
// cleans up after itself.
function mount(node: React.ReactElement): { container: HTMLElement; root: Root } {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => root.render(node))
  return { container, root }
}

describe('ErrorBoundary', () => {
  afterEach(() => vi.restoreAllMocks())

  it('shows a recoverable screen instead of the blank page React would leave', () => {
    // React logs the caught error itself; silence it so the run stays readable.
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { container, root } = mount(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    )

    expect(container.textContent).toContain('Something went wrong')
    expect(container.querySelectorAll('button')).toHaveLength(2)
    // The message is logged, never rendered: it can quote the user's mail.
    expect(container.textContent).not.toContain('component exploded')
    expect(spy.mock.calls.flat().join(' ')).toContain('component exploded')

    act(() => root.unmount())
    container.remove()
  })

  it('renders its children untouched when nothing throws', () => {
    const { container, root } = mount(
      <ErrorBoundary>
        <p>inbox</p>
      </ErrorBoundary>,
    )
    expect(container.textContent).toBe('inbox')
    expect(container.textContent).not.toContain('Something went wrong')

    act(() => root.unmount())
    container.remove()
  })
})
