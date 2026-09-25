export interface CategoryOption {
  name: string
  color?: string
}

const DEFAULT_COLOR = "#3b82f6"

/**
 * CategoryChips shows the master category list as toggle chips, filled when the
 * category is on the edited item. onToggle receives the name and whether it
 * was on before the click.
 */
export function CategoryChips({
  categories,
  selected,
  onToggle,
}: {
  categories: CategoryOption[]
  selected: string[]
  onToggle: (name: string, on: boolean) => void
}) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {categories.map((cat) => {
        const on = selected.includes(cat.name)
        const color = cat.color ?? DEFAULT_COLOR
        return (
          <button
            key={cat.name}
            type="button"
            className="rounded-full border px-2.5 py-0.5 text-xs transition-colors"
            style={{
              borderColor: color,
              color,
              backgroundColor: on ? `${color}15` : "transparent",
              opacity: on ? 1 : 0.5,
            }}
            onClick={() => onToggle(cat.name, on)}
          >
            {cat.name}
          </button>
        )
      })}
    </div>
  )
}

// toggledCategories returns selected with name removed when it was on, or
// added when it was off.
export function toggledCategories(selected: string[], name: string, on: boolean): string[] {
  return on ? selected.filter((c) => c !== name) : [...selected, name]
}
