// singleFlight gates an action so a second attempt made while the first is still
// running is dropped rather than queued.
//
// A disabled button is not enough on its own: a keyboard shortcut reaches the
// handler directly, and React has not re-rendered with the new state by the time
// a second click or keypress arrives. Both paths must consult the same gate, and
// that is why the flag lives here rather than in component state.
export interface Gate {
  // run invokes fn unless one is already running. It reports whether fn ran, so a
  // caller can tell "dropped" from "finished".
  run(fn: () => Promise<void>): Promise<boolean>
  // busy reports whether a run is in flight, for a button's disabled state.
  busy(): boolean
  // begin admits one runner and reports whether it was admitted. It pairs with
  // end for a handler that already has its own try/finally, so the guard is two
  // lines rather than a rewrite of the handler's shape.
  begin(): boolean
  end(): void
}

export function singleFlight(onBusyChange?: (busy: boolean) => void): Gate {
  let running = false
  const set = (v: boolean) => {
    running = v
    onBusyChange?.(v)
  }
  const gate: Gate = {
    async run(fn: () => Promise<void>): Promise<boolean> {
      if (!gate.begin()) return false
      try {
        await fn()
      } finally {
        // The gate reopens even when fn threw, because a refused action must be
        // retryable; leaving it shut would strand the dialog.
        gate.end()
      }
      return true
    },
    busy: () => running,
    begin: () => {
      if (running) return false
      set(true)
      return true
    },
    end: () => set(false),
  }
  return gate
}
