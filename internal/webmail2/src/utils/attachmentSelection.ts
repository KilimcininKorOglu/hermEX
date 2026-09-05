// Selecting several attachments, with Explorer's semantics.
//
// A plain click opens the attachment; ctrl or meta adds one to the selection and
// shift extends from the last one clicked. A drag or a menu started INSIDE the
// selection covers the whole selection, one started outside covers only that
// item and leaves the selection alone, which is what every file manager does and
// is why no checkbox column is needed.

export interface Selection {
  // indexes holds the selected attachment indexes, in the order the message
  // lists them.
  indexes: number[]
  // anchor is the item shift extends from.
  anchor: number | null
}

export const emptySelection: Selection = { indexes: [], anchor: null }

export interface ClickModifiers {
  ctrlKey?: boolean
  metaKey?: boolean
  shiftKey?: boolean
}

// selectOnClick applies one click to the selection. It reports whether the click
// was a selection gesture; a plain click is not, and the caller opens the
// attachment instead.
export function selectOnClick(current: Selection, index: number, mod: ClickModifiers): { selection: Selection; selected: boolean } {
  if (mod.shiftKey && current.anchor !== null) {
    const [lo, hi] = current.anchor <= index ? [current.anchor, index] : [index, current.anchor]
    const range: number[] = []
    for (let i = lo; i <= hi; i++) range.push(i)
    return { selection: { indexes: range, anchor: current.anchor }, selected: true }
  }
  if (mod.ctrlKey || mod.metaKey) {
    const has = current.indexes.includes(index)
    const indexes = has ? current.indexes.filter((i) => i !== index) : [...current.indexes, index].sort((a, b) => a - b)
    return { selection: { indexes, anchor: index }, selected: true }
  }
  return { selection: current, selected: false }
}

// dragSet returns the indexes a gesture on `index` acts on: the whole selection
// when the gesture starts inside it, that one item otherwise.
export function dragSet(current: Selection, index: number): number[] {
  return current.indexes.includes(index) ? current.indexes : [index]
}
