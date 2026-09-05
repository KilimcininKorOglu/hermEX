import { useRef, useState } from "react"
import { singleFlight } from "@/utils/singleFlight"

// useBusyGate pairs a dialog's busy state with a guard that admits one mutation
// at a time.
//
// A button's `disabled={busy}` is not the guard: setting state is asynchronous,
// so a second click arriving before React re-renders enters the handler again
// and starts a second mutation. On a create dialog that is a duplicate record.
//
// Use it as the two lines a handler already has:
//
//   if (!begin()) return        // where setBusy(true) was
//   try { ... } finally { end() } // where setBusy(false) was
export function useBusyGate(): { busy: boolean; begin: () => boolean; end: () => void } {
  const [busy, setBusy] = useState(false)
  const gate = useRef(singleFlight(setBusy)).current
  return { busy, begin: gate.begin, end: gate.end }
}
