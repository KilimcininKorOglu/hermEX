import { describe, it, expect } from 'vitest'
import { singleFlight } from './singleFlight'

// defer returns a promise plus its resolver, so a test can hold an action open
// and start a second one while the first is still running.
function defer(): { promise: Promise<void>; resolve: () => void; reject: (e: Error) => void } {
  let resolve!: () => void
  let reject!: (e: Error) => void
  const promise = new Promise<void>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('singleFlight', () => {
  // This is the defect the gate exists for: a second send or draft save started
  // before the first answers. Two sends deliver the message twice; two draft
  // saves each post without a draft id and create a SEPARATE draft.
  it('drops a second run started while the first is in flight', async () => {
    const gate = singleFlight()
    const first = defer()
    let calls = 0

    const running = gate.run(async () => {
      calls++
      await first.promise
    })
    expect(gate.busy()).toBe(true)

    expect(await gate.run(async () => { calls++ })).toBe(false)
    expect(calls).toBe(1)

    first.resolve()
    expect(await running).toBe(true)
    expect(calls).toBe(1)
  })

  it('reopens once the first run finishes', async () => {
    const gate = singleFlight()
    let calls = 0
    await gate.run(async () => { calls++ })
    expect(await gate.run(async () => { calls++ })).toBe(true)
    expect(calls).toBe(2)
    expect(gate.busy()).toBe(false)
  })

  // A refused send must stay retryable: the reader fixes the recipient and sends
  // again, so a gate that stayed shut on failure would strand the dialog.
  it('reopens when the run throws', async () => {
    const gate = singleFlight()
    await expect(gate.run(async () => { throw new Error('refused') })).rejects.toThrow('refused')
    expect(gate.busy()).toBe(false)
    expect(await gate.run(async () => {})).toBe(true)
  })

  // begin/end is the same gate for a handler that already has its own
  // try/finally, so it must drop a second caller exactly as run does.
  it('admits one caller through begin and reopens on end', () => {
    const gate = singleFlight()
    expect(gate.begin()).toBe(true)
    expect(gate.begin()).toBe(false)
    expect(gate.busy()).toBe(true)
    gate.end()
    expect(gate.busy()).toBe(false)
    expect(gate.begin()).toBe(true)
  })

  it('reports every busy transition to the observer', async () => {
    const seen: boolean[] = []
    const gate = singleFlight((b) => seen.push(b))
    await gate.run(async () => {})
    expect(seen).toEqual([true, false])
  })
})
