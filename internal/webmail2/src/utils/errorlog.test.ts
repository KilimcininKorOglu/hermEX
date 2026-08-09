import { describe, it, expect, vi, afterEach } from 'vitest'
import { describeError, installGlobalErrorHandlers } from './errorlog'

describe('describeError', () => {
  it('keeps the stack of a real Error', () => {
    const err = new Error('boom')
    expect(describeError(err)).toContain('boom')
  })

  it('renders a non-Error rejection reason readably', () => {
    expect(describeError('plain string')).toBe('plain string')
    expect(describeError({ code: 42 })).toBe('{"code":42}')
    // A value JSON cannot serialize still produces something, never a throw.
    const cyclic: Record<string, unknown> = {}
    cyclic.self = cyclic
    expect(describeError(cyclic)).toBe('[object Object]')
  })
})

describe('installGlobalErrorHandlers', () => {
  afterEach(() => vi.restoreAllMocks())

  // A bare EventTarget stands in for the window: dispatching an unhandled
  // 'error' on the real one reaches the environment's default handler and
  // fails the run for reasons that have nothing to do with the assertion.
  const fakeWindow = () => new EventTarget() as unknown as Window

  it('records an uncaught error and an unhandled rejection', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const target = fakeWindow()
    const remove = installGlobalErrorHandlers(target)

    target.dispatchEvent(new ErrorEvent('error', { error: new Error('render blew up') }))
    target.dispatchEvent(new Event('unhandledrejection'))

    expect(spy).toHaveBeenCalledTimes(2)
    expect(spy.mock.calls[0].join(' ')).toContain('render blew up')
    remove()
  })

  it('stops recording once removed, so nothing outlives the app', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const target = fakeWindow()
    const remove = installGlobalErrorHandlers(target)
    remove()

    target.dispatchEvent(new ErrorEvent('error', { error: new Error('after removal') }))
    expect(spy).not.toHaveBeenCalled()
  })
})
