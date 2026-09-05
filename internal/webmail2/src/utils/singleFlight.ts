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
}

export function singleFlight(onBusyChange?: (busy: boolean) => void): Gate {
  let running = false
  const set = (v: boolean) => {
    running = v
    onBusyChange?.(v)
  }
  return {
    async run(fn: () => Promise<void>): Promise<boolean> {
      if (running) return false
      set(true)
      try {
        await fn()
      } finally {
        // The gate reopens even when fn threw, because a refused action must be
        // retryable; leaving it shut would strand the dialog.
        set(false)
      }
      return true
    },
    busy: () => running,
  }
}
