import { useCallback, useMemo, useState } from "react"

/**
 * Multi-select state for a message list: a set of selected message ids plus the
 * toggle/select-all/clear helpers every list view used to hand-roll. Pairs with
 * the shared BulkActionBar so the contextual action toolbar (and its Export as
 * zip) lives in one place across every folder view.
 */
export interface BulkSelection {
  /** The selected message ids, for checkbox `checked` state. */
  selected: Set<string>
  /** The selected ids as an array, for passing to api calls. */
  ids: string[]
  /** Number of selected messages. */
  count: number
  isSelected: (id: string) => boolean
  /** Add or remove a single id. */
  toggle: (id: string) => void
  /** Select every id when not all are selected, otherwise clear (header checkbox). */
  toggleAll: (allIds: string[]) => void
  /** Whether every id in the list is currently selected. */
  allSelected: (allIds: string[]) => boolean
  clear: () => void
}

/**
 * Manages the selected-id set for a list view. Each page keeps its own instance,
 * so selection clears naturally when the page unmounts on navigation.
 */
export function useBulkSelection(): BulkSelection {
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const toggle = useCallback((id: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }, [])

  const toggleAll = useCallback((allIds: string[]) => {
    setSelected((prev) => (allIds.length > 0 && prev.size === allIds.length ? new Set() : new Set(allIds)))
  }, [])

  const clear = useCallback(() => setSelected(new Set()), [])
  const isSelected = useCallback((id: string) => selected.has(id), [selected])
  const allSelected = useCallback(
    (allIds: string[]) => allIds.length > 0 && selected.size === allIds.length,
    [selected],
  )
  const ids = useMemo(() => [...selected], [selected])

  return { selected, ids, count: selected.size, isSelected, toggle, toggleAll, allSelected, clear }
}
