import { useState } from "react"
import { Switch } from "@/components/ui/switch"
import { clampPageSize, MAX_PAGE_SIZE, MIN_PAGE_SIZE } from "@/utils/inboxNavigation"

// Every presentational component here lives at module scope on purpose. A
// component declared inside a page is a NEW component type on every render, so
// React unmounts the whole settings tree and mounts a fresh one for any state
// change. The page height then collapses for one frame and the browser clamps
// the scroll position to the top, which moved the reader away from the control
// they had just used.

export const SELECT_CLASS =
  "max-w-[16rem] rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"

export function SettingSection({
  icon: Icon,
  title,
  description,
  children,
}: {
  icon: React.ElementType
  title: string
  description: string
  children: React.ReactNode
}) {
  return (
    <div className="rounded-lg border bg-card">
      <div className="flex items-center gap-4 p-6 pb-4">
        <div className="rounded-full bg-muted p-2">
          <Icon className="h-5 w-5" />
        </div>
        <div>
          <h3 className="font-semibold">{title}</h3>
          <p className="text-sm text-muted-foreground">{description}</p>
        </div>
      </div>
      <div className="px-6 pb-6">{children}</div>
    </div>
  )
}

// SettingRow is a titled switch.
export function SettingRow({
  title,
  description,
  checked,
  onChange,
}: {
  title: string
  description: string
  checked: boolean
  onChange: () => void
}) {
  return (
    <div className="flex items-center justify-between py-3">
      <div>
        <p className="font-medium">{title}</p>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      <Switch checked={checked} onCheckedChange={onChange} />
    </div>
  )
}

// FieldRow puts a title and description beside any control.
export function FieldRow({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children: React.ReactNode
}) {
  return (
    <div className="flex items-center justify-between gap-4">
      <div>
        <p className="font-medium">{title}</p>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      {children}
    </div>
  )
}

export interface SelectOption {
  value: string
  label: string
}

// SelectField is a titled select over string values.
export function SelectField({
  title,
  description,
  value,
  options,
  onChange,
  disabled,
  className = SELECT_CLASS,
}: {
  title: string
  description: string
  value: string
  options: SelectOption[]
  onChange: (value: string) => void
  disabled?: boolean
  className?: string
}) {
  return (
    <FieldRow title={title} description={description}>
      <select value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)} className={className}>
        {options.map((o) => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
    </FieldRow>
  )
}

// CheckboxField is a titled checkbox.
export function CheckboxField({
  title,
  description,
  checked,
  onChange,
}: {
  title: string
  description: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <FieldRow title={title} description={description}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
    </FieldRow>
  )
}

// PageSizeInput edits the inbox page size as text and saves it only when the
// field is left or Enter is pressed. A save per keystroke clamps every partial
// value, so typing "25" first stored "2" as the minimum and the digits that
// followed built on it. The parent keys it by the stored value, so a stored
// change starts a fresh draft.
export function PageSizeInput({ value, onCommit }: { value: number; onCommit: (n: number) => void }) {
  const [draft, setDraft] = useState(String(value))
  const commit = () => {
    const n = Number(draft)
    if (draft.trim() === "" || !Number.isFinite(n)) {
      setDraft(String(value))
      return
    }
    const next = clampPageSize(n)
    setDraft(String(next))
    if (next !== value) onCommit(next)
  }
  return (
    <input
      type="number"
      min={MIN_PAGE_SIZE}
      max={MAX_PAGE_SIZE}
      step={10}
      className="w-24 rounded-md border bg-background px-3 py-2 text-sm"
      value={draft}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => { if (e.key === "Enter") commit() }}
    />
  )
}

// numberOptions lists numeric choices as select options.
export function numberOptions(values: number[], label: (n: number) => string): SelectOption[] {
  return values.map((n) => ({ value: String(n), label: label(n) }))
}
